package dbpush

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

const (
	postgresIndexProbeTable     = "gco_push_index_probe"
	postgresIndexProbeSavepoint = "gco_push_index_probe"
	postgresIndexProbeOne       = "gco_push_index_probe_one"
)

// postgresIndexRebuild is a DropIndex immediately followed by an AddIndex of
// the same index, which the planner emits when the textual definitions differ.
type postgresIndexRebuild struct {
	drop, add int
}

// dropEquivalentPostgresIndexRebuilds removes index rebuilds whose target
// definition PostgreSQL deparses exactly like the live index.
//
// Index predicates and expressions are raw SQL that PostgreSQL rewrites when
// it stores them (operators, casts, parentheses, literal formats that depend
// on the session TimeZone, ...). Text normalization cannot cover all of that,
// so each candidate is created on an empty TEMP copy of its table inside a
// savepoint and compared through pg_get_indexdef. Nothing persists: the
// savepoint is always rolled back.
func dropEquivalentPostgresIndexRebuilds(ctx context.Context, db transactionalRunner, tx *sql.Tx, cs *migrate.Changeset, target *ir.Schema) (*migrate.Changeset, error) {
	rebuilds := postgresIndexRebuildCandidates(cs, target)
	if len(rebuilds) == 0 {
		return cs, nil
	}

	runner := tx
	if runner == nil {
		probeTx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("begin index probe transaction: %w", err)
		}
		defer probeTx.Rollback()
		runner = probeTx
	}

	gen := migrate.DDLGenerator{Dialect: "postgresql", Schema: target}
	equivalent := make(map[int]bool)
	byTable := make(map[string][]postgresIndexRebuild)
	var tables []string
	for _, r := range rebuilds {
		c := cs.Changes[r.add]
		key := postgresTableKey(c.Schema, c.Model)
		if _, ok := byTable[key]; !ok {
			tables = append(tables, key)
		}
		byTable[key] = append(byTable[key], r)
	}

	for _, key := range tables {
		group := byTable[key]
		first := cs.Changes[group[0].add]
		if err := probePostgresTableIndexes(ctx, runner, gen, cs, first.Schema, first.Model, group, equivalent); err != nil {
			return nil, err
		}
	}
	if len(equivalent) == 0 {
		return cs, nil
	}

	out := &migrate.Changeset{Old: cs.Old, New: cs.New, Changes: make([]migrate.Change, 0, len(cs.Changes))}
	for i, c := range cs.Changes {
		if !equivalent[i] {
			out.Changes = append(out.Changes, c)
		}
	}
	return out, nil
}

func probePostgresTableIndexes(ctx context.Context, tx *sql.Tx, gen migrate.DDLGenerator, cs *migrate.Changeset, schemaName, table string, group []postgresIndexRebuild, equivalent map[int]bool) (err error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+postgresIndexProbeSavepoint); err != nil {
		return fmt.Errorf("index probe: %w", err)
	}
	defer func() {
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+postgresIndexProbeSavepoint); rbErr != nil && err == nil {
			err = fmt.Errorf("index probe: %w", rbErr)
			return
		}
		if _, relErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+postgresIndexProbeSavepoint); relErr != nil && err == nil {
			err = fmt.Errorf("index probe: %w", relErr)
		}
	}()

	source := quotePostgresIdent(table)
	if schemaName != "" {
		source = quotePostgresIdent(schemaName) + "." + source
	}
	// LIKE copies column names, types, and collations; an empty copy makes
	// index creation instant regardless of the live table's size.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE TEMP TABLE %s (LIKE %s)", quotePostgresIdent(postgresIndexProbeTable), source)); err != nil {
		return nil // Leave the rebuilds in place; the push surfaces real errors.
	}

	for n, r := range group {
		live, ok, err := postgresIndexDefinition(ctx, tx, postgresQualifiedIndexName(schemaName, cs.Changes[r.drop].OldValue))
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		probeName := fmt.Sprintf("%s_%d", postgresIndexProbeTable, n)
		probe := cs.Changes[r.add]
		probe.Schema = "pg_temp"
		probe.Model = postgresIndexProbeTable
		probe.NewValue = probeName
		createSQL := gen.AddIndexSQL(probe)

		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+postgresIndexProbeOne); err != nil {
			return fmt.Errorf("index probe: %w", err)
		}
		_, createErr := tx.ExecContext(ctx, createSQL)
		var probed string
		var probedOK bool
		if createErr == nil {
			probed, probedOK, err = postgresIndexDefinition(ctx, tx, "pg_temp."+quotePostgresIdent(probeName))
		}
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+postgresIndexProbeOne); rbErr != nil {
			return fmt.Errorf("index probe: %w", rbErr)
		}
		if err != nil {
			return err
		}
		if createErr == nil && probedOK && probed == live {
			equivalent[r.drop] = true
			equivalent[r.add] = true
		}
	}
	return nil
}

// postgresIndexDefinition returns pg_get_indexdef for an index without the
// "CREATE ... INDEX name ON table" prefix, keeping UNIQUE, the access method,
// columns, options, and predicate.
func postgresIndexDefinition(ctx context.Context, tx *sql.Tx, qualifiedName string) (string, bool, error) {
	var unique bool
	var def string
	err := tx.QueryRowContext(ctx, `
SELECT ix.indisunique, pg_catalog.pg_get_indexdef(ix.indexrelid)
FROM pg_catalog.pg_index ix
WHERE ix.indexrelid = pg_catalog.to_regclass($1)`, qualifiedName).Scan(&unique, &def)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("index probe: read %s: %w", qualifiedName, err)
	}
	on := strings.Index(def, " ON ")
	if on < 0 {
		return "", false, nil
	}
	using := strings.Index(def[on:], " USING ")
	if using < 0 {
		return "", false, nil
	}
	return fmt.Sprintf("unique=%t%s", unique, def[on+using:]), true, nil
}

// postgresIndexRebuildCandidates finds DropIndex/AddIndex pairs for the same
// index on a table whose columns are otherwise left alone by the changeset.
func postgresIndexRebuildCandidates(cs *migrate.Changeset, target *ir.Schema) []postgresIndexRebuild {
	if cs == nil {
		return nil
	}
	unsafe := postgresIndexProbeUnsafeTables(cs, target)
	var out []postgresIndexRebuild
	for i := 0; i+1 < len(cs.Changes); i++ {
		drop, add := cs.Changes[i], cs.Changes[i+1]
		if drop.Type != migrate.DropIndex || add.Type != migrate.AddIndex {
			continue
		}
		if drop.Model != add.Model || drop.Schema != add.Schema || drop.OldValue == "" {
			continue
		}
		name := add.NewValue
		if name == "" {
			name = migrate.DefaultIndexNameFor("postgresql", add.Model, strings.Split(add.Details["fields"], ","), add.Details["unique"] == "true")
		}
		if name != drop.OldValue || unsafe[postgresTableKey(add.Schema, add.Model)] {
			continue
		}
		out = append(out, postgresIndexRebuild{drop: i, add: i + 1})
		i++
	}
	return out
}

// postgresIndexProbeUnsafeTables lists tables whose columns change in this
// push. Probing them against the current columns could hide a rebuild the new
// column definitions require.
func postgresIndexProbeUnsafeTables(cs *migrate.Changeset, target *ir.Schema) map[string]bool {
	unsafe := make(map[string]bool)
	changedEnums := make(map[string]bool)
	for _, c := range cs.Changes {
		switch c.Type {
		case migrate.AlterType, migrate.DropColumn, migrate.RenameColumn, migrate.AddColumn,
			migrate.SetTableSchema, migrate.RenameTable, migrate.DropTable, migrate.CreateTable:
			unsafe[postgresTableKey(c.Schema, c.Model)] = true
		case migrate.AlterEnum, migrate.RenameEnum, migrate.DropEnum, migrate.CreateEnum:
			for _, name := range []string{c.Model, c.OldValue, c.NewValue} {
				if name != "" {
					changedEnums[name] = true
				}
			}
		}
	}
	if len(changedEnums) > 0 && target != nil {
		for _, m := range target.Models {
			for _, f := range m.Fields {
				if f.Type == ir.FieldKindEnum && changedEnums[f.EnumType] {
					unsafe[postgresTableKey(m.Schema, m.TableName())] = true
					break
				}
			}
		}
	}
	return unsafe
}

func postgresQualifiedIndexName(schemaName, name string) string {
	if schemaName == "" {
		return quotePostgresIdent(name)
	}
	return quotePostgresIdent(schemaName) + "." + quotePostgresIdent(name)
}

func quotePostgresIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
