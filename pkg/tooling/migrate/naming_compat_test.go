package migrate

import (
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func longNameSchema(provider string) *ir.Schema {
	cols := []string{"a_really_long_column_name_number_one", "a_really_long_column_name_number_two"}
	model := &ir.Model{
		Name:   "Thing",
		DBName: "things_with_a_long_table_name",
		Fields: []*ir.Field{
			{Name: "id", ScalarType: "Int", IsID: true},
			{Name: cols[0], ScalarType: "String"},
			{Name: cols[1], ScalarType: "String"},
		},
		PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
		Indexes:    []*ir.Index{{Fields: cols}},
	}
	return &ir.Schema{Datasource: &ir.Datasource{Provider: provider}, Models: []*ir.Model{model}}
}

// Only PostgreSQL shortens long generated names: MySQL and SQLite keep the
// full names older versions created so existing databases keep matching.
func TestGeneratedIndexNamesAreShortenedOnlyOnPostgres(t *testing.T) {
	full := "idx_things_with_a_long_table_name_a_really_long_column_name_number_one_a_really_long_column_name_number_two"
	for _, tc := range []struct {
		provider string
		want     string
	}{
		{"postgresql", LimitIdentifier(full)},
		{"", LimitIdentifier(full)},
		{"sqlite", full},
		{"mysql", full},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			cs := Diff(nil, longNameSchema(tc.provider))
			gen := DDLGenerator{Dialect: tc.provider, Schema: cs.New}
			if tc.provider == "" {
				gen.Dialect = "postgresql"
			}
			sql := gen.GenerateUp(cs)
			if !strings.Contains(sql, `INDEX `+gen.quoteID(tc.want)+` ON`) {
				t.Fatalf("index name %q not used:\n%s", tc.want, sql)
			}

			// The planner keys indexes by the same name, so an unchanged
			// schema produces no index churn.
			if again := Diff(longNameSchema(tc.provider), longNameSchema(tc.provider)); len(again.Changes) != 0 {
				t.Fatalf("unchanged schema produced changes: %+v", again.Changes)
			}
		})
	}
	if got := LimitIdentifier(full); len(got) > MaxIdentifierBytes {
		t.Fatalf("postgres name %q exceeds %d bytes", got, MaxIdentifierBytes)
	}
}

// MySQL keeps the comma-joined composite foreign key name older versions
// created: it has no DROP FOREIGN KEY IF EXISTS to fall back on.
func TestMySQLForeignKeyKeepsLegacyName(t *testing.T) {
	gen := DDLGenerator{Dialect: "mysql"}
	change := Change{Type: AddFK, Model: "posts", Details: map[string]string{"fields": "a,b", "references": "x,y", "toModel": "users"}}
	if got := gen.addFKSQL(change); !strings.Contains(got, "CONSTRAINT `fk_posts_a,b` FOREIGN KEY") {
		t.Fatalf("addFKSQL() = %s", got)
	}
	change.Type = DropFK
	if got := gen.dropFKSQL(change); got != "ALTER TABLE `posts` DROP FOREIGN KEY `fk_posts_a,b`;" {
		t.Fatalf("dropFKSQL() = %s", got)
	}

	pg := DDLGenerator{Dialect: "postgresql"}
	change.Type = AddFK
	if got := pg.addFKSQL(change); !strings.Contains(got, `CONSTRAINT "fk_posts_a_b"`) {
		t.Fatalf("postgres addFKSQL() = %s", got)
	}
}

func TestMySQLUnsignedNativeTypes(t *testing.T) {
	mysql := DDLGenerator{Dialect: "mysql"}
	for name, want := range map[string]string{"UnsignedInt": "INT UNSIGNED", "UnsignedBigInt": "BIGINT UNSIGNED"} {
		if got := mysql.nativeTypeSQL(&ir.NativeType{Name: name}); got != want {
			t.Fatalf("mysql nativeTypeSQL(%s) = %q, want %q", name, got, want)
		}
	}
}
