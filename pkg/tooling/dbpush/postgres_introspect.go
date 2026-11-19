package dbpush

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

// resolveSchemaName returns the PostgreSQL schema that unqualified names
// resolve to during db push: the datasource schema, a single datasource
// schemas entry, the first explicit search_path entry of the URL, or public.
func resolveSchemaName(schema *ir.Schema, dsn string) string {
	if schema != nil && schema.Datasource != nil && strings.TrimSpace(schema.Datasource.Schema) != "" {
		return strings.TrimSpace(schema.Datasource.Schema)
	}
	if schema != nil && schema.Datasource != nil && len(schema.Datasource.Schemas) == 1 {
		return schema.Datasource.Schemas[0]
	}
	if schemaName := postgresSchemaFromURL(dsn); schemaName != "" {
		return schemaName
	}
	return "public"
}

// postgresSchemaFromURL returns the schema named by the URL's schema
// parameter or the first search_path entry other than "$user".
func postgresSchemaFromURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	query := parsedURL.Query()
	if schemaName := strings.TrimSpace(query.Get("schema")); schemaName != "" {
		return schemaName
	}
	for _, entry := range splitPostgresSearchPath(query.Get("search_path")) {
		if entry != "$user" {
			return entry
		}
	}
	return ""
}

// splitPostgresSearchPath splits a search_path setting into schema names,
// removing double quotes from quoted identifiers.
func splitPostgresSearchPath(value string) []string {
	var names []string
	var b strings.Builder
	quoted, wasQuoted := false, false
	flush := func() {
		name := b.String()
		if !wasQuoted {
			name = strings.TrimSpace(name)
		}
		if name != "" {
			names = append(names, name)
		}
		b.Reset()
		wasQuoted = false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case quoted && ch == '"' && i+1 < len(value) && value[i+1] == '"':
			b.WriteByte('"')
			i++
		case ch == '"':
			quoted = !quoted
			wasQuoted = true
		case !quoted && ch == ',':
			flush()
		case !quoted && (ch == ' ' || ch == '\t'):
		default:
			b.WriteByte(ch)
		}
	}
	flush()
	return names
}

// postgresSearchPathSQL sets the push transaction's search_path to the
// default schema followed by public, where extensions are usually installed.
func postgresSearchPathSQL(defaultSchema string) string {
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	if defaultSchema == "public" {
		return "SET LOCAL search_path TO " + quote(defaultSchema)
	}
	return "SET LOCAL search_path TO " + quote(defaultSchema) + ", public"
}

// ensurePostgresSchema creates schemaName when it does not exist. Existence is
// checked first so roles without CREATE on the database can still push into
// an existing schema.
func ensurePostgresSchema(ctx context.Context, runner sqlRunner, schemaName string) error {
	var exists bool
	if err := runner.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)", schemaName).Scan(&exists); err != nil {
		return fmt.Errorf("check postgresql schema %q: %w", schemaName, err)
	}
	if exists {
		return nil
	}
	if _, err := runner.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+`"`+strings.ReplaceAll(schemaName, `"`, `""`)+`"`); err != nil {
		return fmt.Errorf("create postgresql schema %q: %w", schemaName, err)
	}
	return nil
}

// postgresManagesDefaultSchema reports whether db push manages the default
// schema. With a datasource schemas list, the default schema (often the public
// fallback) is managed only when it is listed or a model lives in it; otherwise
// unrelated tables there would be planned as drops.
func postgresManagesDefaultSchema(target *ir.Schema, defaultSchema string) bool {
	if target == nil || target.Datasource == nil || len(target.Datasource.Schemas) == 0 {
		return true
	}
	if strings.TrimSpace(target.Datasource.Schema) != "" {
		return true
	}
	for _, name := range target.Datasource.Schemas {
		if strings.TrimSpace(name) == defaultSchema {
			return true
		}
	}
	for _, model := range target.Models {
		if name := strings.TrimSpace(model.Schema); name == "" || name == defaultSchema {
			return true
		}
	}
	return false
}

// postgresIntrospectionSchemas lists the schemas db push reads: the default
// schema (when managed), the datasource schemas, and every model's @@schema.
// The default schema, when present, is always first.
func postgresIntrospectionSchemas(target *ir.Schema, defaultSchema string) []string {
	seen := map[string]bool{defaultSchema: true}
	var schemas []string
	managed := postgresManagesDefaultSchema(target, defaultSchema)
	if managed {
		schemas = append(schemas, defaultSchema)
	}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			schemas = append(schemas, name)
		}
	}
	if target != nil {
		if target.Datasource != nil {
			for _, name := range target.Datasource.Schemas {
				add(name)
			}
		}
		for _, model := range target.Models {
			add(model.Schema)
		}
	}
	if managed {
		sort.Strings(schemas[1:])
	} else {
		sort.Strings(schemas)
	}
	return schemas
}

// postgresTextArray renders a text[] literal for an ANY($1::text[]) argument.
func postgresTextArray(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// postgresCatalog collects the introspected tables of all schemas, keyed by
// physical schema and table name, together with the target model each one
// corresponds to.
type postgresCatalog struct {
	defaultSchema string
	models        map[string]*ir.Model
	targets       map[string]*ir.Model
}

func postgresTableKey(schemaName, table string) string {
	return schemaName + "\x00" + table
}

func (c *postgresCatalog) model(schemaName, table string) *ir.Model {
	return c.models[postgresTableKey(schemaName, table)]
}

func (c *postgresCatalog) target(schemaName, table string) *ir.Model {
	return c.targets[postgresTableKey(schemaName, table)]
}

// findPostgresTargetModel returns the target model stored in schemaName.
// Models without @@schema live in the default schema.
func findPostgresTargetModel(target *ir.Schema, defaultSchema, schemaName, table string) *ir.Model {
	if target == nil {
		return nil
	}
	for _, model := range target.Models {
		modelSchema := model.Schema
		if modelSchema == "" {
			modelSchema = defaultSchema
		}
		if modelSchema == schemaName && model.TableName() == table {
			return model
		}
	}
	return nil
}

func introspectPostgres(ctx context.Context, db sqlRunner, schemaName string) (*ir.Schema, error) {
	return introspectPostgresWithTarget(ctx, db, schemaName, nil)
}

// introspectPostgresWithTarget reads the tables of the default schema and of
// every schema the target uses. Tables in the default schema have an empty
// Model.Schema unless the target names that schema explicitly, matching how
// unqualified target models resolve. Enum types are read from the default
// schema, where unqualified CREATE TYPE statements place them.
func introspectPostgresWithTarget(ctx context.Context, db sqlRunner, defaultSchema string, target *ir.Schema) (*ir.Schema, error) {
	schema := &ir.Schema{Datasource: &ir.Datasource{Provider: "postgresql", Schema: defaultSchema}}
	schemas := postgresIntrospectionSchemas(target, defaultSchema)
	catalog := &postgresCatalog{defaultSchema: defaultSchema, models: map[string]*ir.Model{}, targets: map[string]*ir.Model{}}

	if err := loadPostgresTables(ctx, db, schemas, target, catalog); err != nil {
		return nil, err
	}
	enums, enumNames, err := loadPostgresEnums(ctx, db, defaultSchema, target)
	if err != nil {
		return nil, err
	}
	schema.Enums = enums
	if err := loadPostgresColumns(ctx, db, schemas, catalog, enums, enumNames); err != nil {
		return nil, err
	}
	if err := loadPostgresKeyConstraints(ctx, db, schemas, catalog); err != nil {
		return nil, err
	}
	if err := loadPostgresIndexes(ctx, db, schemas, catalog); err != nil {
		return nil, err
	}
	if err := loadPostgresForeignKeys(ctx, db, schemas, catalog); err != nil {
		return nil, err
	}
	if err := loadPostgresTriggers(ctx, db, schemas, catalog); err != nil {
		return nil, err
	}
	matchTruncatedPostgresNames(catalog)
	adoptDeclaredUniqueIndexes(catalog)

	keys := make([]string, 0, len(catalog.models))
	for key := range catalog.models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		schema.Models = append(schema.Models, catalog.models[key])
	}
	return schema, nil
}

// loadPostgresTables reads ordinary and partitioned tables. Partitions are
// storage of their parent and are not models.
func loadPostgresTables(ctx context.Context, db sqlRunner, schemas []string, target *ir.Schema, catalog *postgresCatalog) error {
	rows, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = ANY($1::text[]) AND c.relkind IN ('r', 'p') AND NOT c.relispartition
ORDER BY n.nspname, c.relname`, postgresTextArray(schemas))
	if err != nil {
		return err
	}
	defer rows.Close()

	type table struct{ schema, name string }
	var tables []table
	tableCount := map[string]int{}
	for rows.Next() {
		var t table
		if err := rows.Scan(&t.schema, &t.name); err != nil {
			return err
		}
		if isInternalTable(t.name) {
			continue
		}
		tables = append(tables, t)
		tableCount[t.name]++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range tables {
		key := postgresTableKey(t.schema, t.name)
		targetModel := findPostgresTargetModel(target, catalog.defaultSchema, t.schema, t.name)
		model := &ir.Model{Name: t.name, DBName: t.name, Schema: t.schema}
		if t.schema == catalog.defaultSchema && (targetModel == nil || targetModel.Schema == "") {
			model.Schema = ""
		}
		// Model names must be unique; tables sharing a name in other
		// schemas are named after their schema.
		if t.schema != catalog.defaultSchema && tableCount[t.name] > 1 {
			model.Name = t.schema + "." + t.name
		}
		catalog.models[key] = model
		if targetModel != nil {
			catalog.targets[key] = targetModel
		}
	}
	return nil
}

func loadPostgresEnums(ctx context.Context, db sqlRunner, schemaName string, targetSchema *ir.Schema) ([]*ir.Enum, map[string]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT typ.typname, enum.enumlabel
FROM pg_catalog.pg_type typ
JOIN pg_catalog.pg_namespace ns ON ns.oid = typ.typnamespace
JOIN pg_catalog.pg_enum enum ON enum.enumtypid = typ.oid
WHERE ns.nspname = $1
ORDER BY typ.typname, enum.enumsortorder`, schemaName)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	targetEnums := make(map[string]*ir.Enum)
	if targetSchema != nil {
		for _, enum := range targetSchema.Enums {
			targetEnums[postgresEnumDatabaseName(enum)] = enum
		}
	}

	byDatabaseName := make(map[string]*ir.Enum)
	var databaseNames []string
	for rows.Next() {
		var databaseName, label string
		if err := rows.Scan(&databaseName, &label); err != nil {
			return nil, nil, err
		}
		enum := byDatabaseName[databaseName]
		if enum == nil {
			enum = postgresIntrospectedEnum(databaseName, targetEnums[databaseName])
			byDatabaseName[databaseName] = enum
			databaseNames = append(databaseNames, databaseName)
		}
		enum.Values = append(enum.Values, postgresIntrospectedEnumValue(label, targetEnums[databaseName]))
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	enums := make([]*ir.Enum, 0, len(databaseNames))
	names := make(map[string]string, len(databaseNames))
	for _, databaseName := range databaseNames {
		enum := byDatabaseName[databaseName]
		enums = append(enums, enum)
		names[databaseName] = enum.Name
	}
	return enums, names, nil
}

// loadPostgresColumns reads columns from pg_attribute, which unlike
// information_schema is not filtered by column privileges. data_type and
// udt_name follow information_schema.columns conventions.
func loadPostgresColumns(ctx context.Context, db sqlRunner, schemas []string, catalog *postgresCatalog, enums []*ir.Enum, enumNames map[string]string) error {
	rows, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname, a.attname, NOT a.attnotnull,
       CASE WHEN t.typcategory = 'A' THEN 'ARRAY' WHEN t.typtype = 'e' THEN 'USER-DEFINED' ELSE format_type(a.atttypid, NULL) END,
       CASE WHEN t.typcategory = 'A' THEN '_' || et.typname ELSE t.typname END,
       tn.nspname,
       pg_get_expr(d.adbin, d.adrelid),
       a.attidentity::text,
       a.attgenerated::text,
       format_type(a.atttypid, a.atttypmod)
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_catalog.pg_type et ON t.typcategory = 'A' AND et.oid = t.typelem
JOIN pg_catalog.pg_namespace tn ON tn.oid = COALESCE(et.typnamespace, t.typnamespace)
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = ANY($1::text[]) AND c.relkind IN ('r', 'p') AND NOT c.relispartition
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY n.nspname, c.relname, a.attnum`, postgresTextArray(schemas))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var schemaName, tableName, columnName, dataType, udtName, udtSchema, identity, generated, formattedType string
		var nullable bool
		var columnDefault sql.NullString
		if err := rows.Scan(&schemaName, &tableName, &columnName, &nullable, &dataType, &udtName, &udtSchema, &columnDefault, &identity, &generated, &formattedType); err != nil {
			return err
		}
		model := catalog.model(schemaName, tableName)
		if model == nil {
			continue
		}
		field := &ir.Field{
			Name:       columnName,
			DBName:     columnName,
			Type:       ir.FieldKindScalar,
			IsOptional: nullable,
		}
		if generated == "s" {
			field.IsGenerated = true
			field.Default = &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{strings.TrimSpace(columnDefault.String)}}
		} else {
			field.Default = postgresDefaultValue(columnDefault.String, identity == "a" || identity == "d")
		}
		var targetField *ir.Field
		if targetModel := catalog.target(schemaName, tableName); targetModel != nil {
			targetField = findTargetFieldByColumn(targetModel, columnName)
		}
		adoptDbGeneratedDefault(field, targetField)
		columnEnums := enumNames
		if udtSchema != catalog.defaultSchema {
			columnEnums = nil
		}
		applyPostgresColumnType(field, dataType, udtName, formattedType, targetField, columnEnums)
		if field.Type == ir.FieldKindEnum && !field.IsGenerated {
			mapPostgresEnumDefault(field, enums)
		}
		model.Fields = append(model.Fields, field)
	}
	return rows.Err()
}

func applyPostgresColumnType(field *ir.Field, dataType, udtName, formattedType string, targetField *ir.Field, enumNames map[string]string) {
	databaseType := strings.TrimPrefix(udtName, "_")
	if enumName := enumNames[databaseType]; enumName != "" {
		field.Type = ir.FieldKindEnum
		field.EnumType = enumName
		field.IsList = strings.EqualFold(strings.TrimSpace(dataType), "ARRAY") || strings.HasPrefix(udtName, "_")
		return
	}

	field.ScalarType, field.IsList = postgresColumnType(dataType, udtName)
	field.NativeType = postgresNativeType(formattedType, field.ScalarType)
	// A column whose type is exactly the declared @db type was created from
	// that declaration, so it takes the declared scalar too (String @db.Uuid
	// has the uuid type, which otherwise maps to UUID).
	if targetField != nil && targetField.NativeType != nil && targetField.Type == ir.FieldKindScalar &&
		targetField.IsList == field.IsList && postgresNativeTypeMatches(formattedType, targetField.NativeType, targetField.IsList) {
		clone := *targetField.NativeType
		clone.Args = append([]string(nil), targetField.NativeType.Args...)
		field.NativeType = &clone
		field.ScalarType = targetField.ScalarType
	}
}

// postgresNativeType returns the @db type for a formatted column type that
// differs from the scalar's default DDL type, or nil.
func postgresNativeType(formatted, scalar string) *ir.NativeType {
	lower := strings.ToLower(strings.TrimSpace(formatted))
	name := ""
	switch {
	case strings.HasPrefix(lower, "character varying("):
		name = "VarChar"
	case strings.HasPrefix(lower, "character("):
		name = "Char"
	case strings.HasPrefix(lower, "numeric("):
		// DECIMAL(65,30) is the default DDL type for Decimal.
		if lower == "numeric(65,30)" && scalar == "Decimal" {
			return nil
		}
		name = "Decimal"
	case strings.HasPrefix(lower, "timestamp(") && strings.Contains(lower, "without time zone"):
		name = "Timestamp"
	case strings.HasPrefix(lower, "timestamp(") && strings.Contains(lower, "with time zone"):
		name = "Timestamptz"
	default:
		return nil
	}
	args := []string{}
	if start := strings.Index(lower, "("); start >= 0 {
		if end := strings.Index(lower[start:], ")"); end > 0 {
			args = strings.Split(lower[start+1:start+end], ",")
			for i := range args {
				args[i] = strings.TrimSpace(args[i])
			}
		}
	}
	return &ir.NativeType{Name: name, Args: args}
}

// postgresNativeTypeNames maps lower-case @db type names to the base names
// format_type reports.
var postgresNativeTypeNames = map[string]string{
	"varchar":          "character varying",
	"charactervarying": "character varying",
	"char":             "character",
	"character":        "character",
	"bpchar":           "character",
	"decimal":          "numeric",
	"timestamptz":      "timestamp with time zone",
	"timestamp":        "timestamp without time zone",
	"time":             "time without time zone",
	"timetz":           "time with time zone",
	"int":              "integer",
	"int4":             "integer",
	"int2":             "smallint",
	"int8":             "bigint",
	"doubleprecision":  "double precision",
	"double":           "double precision",
	"float8":           "double precision",
	"float4":           "real",
	"bool":             "boolean",
	"varbit":           "bit varying",
	"bitvarying":       "bit varying",
}

// postgresNativeTypeMatches reports whether a formatted column type is the
// type a @db declaration creates.
func postgresNativeTypeMatches(formatted string, native *ir.NativeType, isList bool) bool {
	if native == nil {
		return false
	}
	name := strings.ToLower(strings.ReplaceAll(native.Name, " ", ""))
	expected := postgresNativeTypeNames[name]
	if expected == "" {
		expected = name
	}
	expectedArgs := strings.Join(native.Args, ",")
	// CHAR and BIT without a length mean length 1.
	if expectedArgs == "" && (expected == "character" || expected == "bit") {
		expectedArgs = "1"
	}
	actual := strings.ToLower(strings.TrimSpace(formatted))
	actual = strings.ReplaceAll(actual, ", ", ",")
	// format_type places type modifiers between the base name and any
	// trailing qualifiers, e.g. "timestamp(6) without time zone".
	actualBase, actualArgs, actualIsList := splitPostgresFormattedTypeArgs(actual)
	if actualBase != expected || actualIsList != isList {
		return false
	}
	return actualArgs == expectedArgs
}

// loadPostgresKeyConstraints reads primary keys and unique constraints from
// pg_constraint in column order.
func loadPostgresKeyConstraints(ctx context.Context, db sqlRunner, schemas []string, catalog *postgresCatalog) error {
	rows, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname, con.conname, con.contype::text, a.attname
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS keys(attnum, ord) ON true
JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = keys.attnum
WHERE n.nspname = ANY($1::text[]) AND con.contype IN ('p', 'u')
  AND c.relkind IN ('r', 'p') AND NOT c.relispartition
ORDER BY n.nspname, c.relname, con.conname, keys.ord`, postgresTextArray(schemas))
	if err != nil {
		return err
	}
	defer rows.Close()

	type constraint struct {
		model   *ir.Model
		name    string
		kind    string
		columns []string
	}
	var constraints []*constraint
	for rows.Next() {
		var schemaName, tableName, name, kind, column string
		if err := rows.Scan(&schemaName, &tableName, &name, &kind, &column); err != nil {
			return err
		}
		model := catalog.model(schemaName, tableName)
		if model == nil {
			continue
		}
		if n := len(constraints); n > 0 && constraints[n-1].model == model && constraints[n-1].name == name {
			constraints[n-1].columns = append(constraints[n-1].columns, column)
			continue
		}
		constraints = append(constraints, &constraint{model: model, name: name, kind: kind, columns: []string{column}})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range constraints {
		if c.kind == "p" {
			c.model.PrimaryKey = &ir.PrimaryKey{Fields: c.columns, IsComposite: len(c.columns) > 1}
			if len(c.columns) == 1 {
				if field := findFieldByColumn(c.model, c.columns[0]); field != nil {
					field.IsID = true
				}
			}
			continue
		}
		if len(c.columns) == 1 {
			if field := findFieldByColumn(c.model, c.columns[0]); field != nil {
				field.IsUnique = true
			}
		}
		c.model.UniqueConstraints = append(c.model.UniqueConstraints, &ir.UniqueConstraint{Name: c.name, Fields: c.columns})
	}
	return nil
}

// loadPostgresIndexes reads indexes that do not back a primary key, unique,
// or exclusion constraint of their own table. Only the table's constraints
// count: a foreign key's conindid names the referenced unique index. Default
// operator classes and collations equal to the column's are reported empty,
// as schema declarations omit them.
func loadPostgresIndexes(ctx context.Context, db sqlRunner, schemas []string, catalog *postgresCatalog) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  ns.nspname,
  t.relname AS table_name,
  i.relname AS index_name,
  ix.indisunique,
  array_agg(COALESCE(a.attname, '') ORDER BY keys.ord) AS columns,
  array_agg(pg_get_indexdef(ix.indexrelid, keys.ord::int, false) ORDER BY keys.ord) AS column_defs,
  array_agg((ix.indoption::int2[])[keys.ord::int - 1] ORDER BY keys.ord) AS column_options,
  array_agg(CASE WHEN opc.opcdefault THEN '' ELSE opc.opcname END ORDER BY keys.ord) AS opclasses,
  array_agg(CASE
    WHEN coll.oid IS NULL OR index_collations.colloid = a.attcollation
      OR (a.attnum IS NULL AND coll.collname = 'default') THEN ''
    ELSE collns.nspname || '.' || coll.collname END ORDER BY keys.ord) AS collations,
  pg_get_expr(ix.indpred, ix.indrelid) AS predicate
FROM pg_catalog.pg_class t
JOIN pg_catalog.pg_namespace ns ON ns.oid = t.relnamespace
JOIN pg_catalog.pg_index ix ON t.oid = ix.indrelid
JOIN pg_catalog.pg_class i ON i.oid = ix.indexrelid
JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS keys(attnum, ord) ON true
LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = t.oid AND a.attnum = keys.attnum
JOIN LATERAL unnest(ix.indclass) WITH ORDINALITY AS classes(opcoid, ord) ON classes.ord = keys.ord
JOIN pg_catalog.pg_opclass opc ON opc.oid = classes.opcoid
JOIN LATERAL unnest(ix.indcollation) WITH ORDINALITY AS index_collations(colloid, ord) ON index_collations.ord = keys.ord
LEFT JOIN pg_catalog.pg_collation coll ON coll.oid = index_collations.colloid
LEFT JOIN pg_catalog.pg_namespace collns ON collns.oid = coll.collnamespace
LEFT JOIN pg_catalog.pg_constraint c ON c.conindid = ix.indexrelid AND c.conrelid = ix.indrelid AND c.contype IN ('p', 'u', 'x')
WHERE ns.nspname = ANY($1::text[])
  AND t.relkind IN ('r', 'p')
  AND NOT t.relispartition
  AND NOT ix.indisprimary
  AND c.oid IS NULL
GROUP BY ns.nspname, t.relname, i.relname, ix.indisunique, ix.indpred, ix.indrelid, ix.indexrelid
ORDER BY ns.nspname, t.relname, i.relname`, postgresTextArray(schemas))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var schemaName, tableName, indexName string
		var isUnique bool
		var columns, columnDefs, columnOptions, opclasses, collations []byte
		var predicate sql.NullString
		if err := rows.Scan(&schemaName, &tableName, &indexName, &isUnique, &columns, &columnDefs, &columnOptions, &opclasses, &collations, &predicate); err != nil {
			return err
		}
		model := catalog.model(schemaName, tableName)
		if model == nil {
			continue
		}
		fields := parsePostgresTextArray(string(columns))
		model.Indexes = append(model.Indexes, &ir.Index{
			Name:     indexName,
			Fields:   fields,
			Columns:  parsePostgresIndexColumns(fields, parsePostgresTextArray(string(columnDefs)), parsePostgresTextArray(string(columnOptions)), parsePostgresTextArray(string(opclasses)), parsePostgresTextArray(string(collations))),
			Where:    strings.TrimSpace(predicate.String),
			IsUnique: isUnique,
		})
	}
	return rows.Err()
}

// loadPostgresForeignKeys reads foreign keys declared on the introspected
// tables. Targets are resolved by schema and table; constraints PostgreSQL
// derives for partitions (conparentid) are skipped.
func loadPostgresForeignKeys(ctx context.Context, db sqlRunner, schemas []string, catalog *postgresCatalog) error {
	rows, err := db.QueryContext(ctx, `
SELECT ns.nspname,
       src.relname,
       con.conname,
       src_col.attname,
       dst_ns.nspname,
       dst.relname,
       dst_col.attname,
       CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END,
       CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class src ON src.oid = con.conrelid
JOIN pg_catalog.pg_namespace ns ON ns.oid = src.relnamespace
JOIN pg_catalog.pg_class dst ON dst.oid = con.confrelid
JOIN pg_catalog.pg_namespace dst_ns ON dst_ns.oid = dst.relnamespace
JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS keys(src_attnum, dst_attnum, ord) ON true
JOIN pg_catalog.pg_attribute src_col ON src_col.attrelid = src.oid AND src_col.attnum = keys.src_attnum
JOIN pg_catalog.pg_attribute dst_col ON dst_col.attrelid = dst.oid AND dst_col.attnum = keys.dst_attnum
WHERE con.contype = 'f' AND ns.nspname = ANY($1::text[])
  AND NOT src.relispartition AND con.conparentid = 0
ORDER BY ns.nspname, src.relname, con.conname, keys.ord`, postgresTextArray(schemas))
	if err != nil {
		return err
	}
	defer rows.Close()

	type foreignKey struct {
		model *ir.Model
		rel   *ir.Relation
	}
	var keys []foreignKey
	for rows.Next() {
		var schemaName, tableName, constraintName, columnName, foreignSchema, foreignTable, foreignColumn, onDelete, onUpdate string
		if err := rows.Scan(&schemaName, &tableName, &constraintName, &columnName, &foreignSchema, &foreignTable, &foreignColumn, &onDelete, &onUpdate); err != nil {
			return err
		}
		model := catalog.model(schemaName, tableName)
		if model == nil {
			continue
		}
		n := len(keys)
		if n == 0 || keys[n-1].model != model || keys[n-1].rel.ConstraintName != constraintName {
			toModel := foreignTable
			if target := catalog.model(foreignSchema, foreignTable); target != nil {
				toModel = target.Name
			}
			keys = append(keys, foreignKey{model: model, rel: &ir.Relation{
				Name:           constraintName,
				ConstraintName: constraintName,
				FromModel:      model.Name,
				ToModel:        toModel,
				OnDelete:       onDelete,
				OnUpdate:       onUpdate,
			}})
			n++
		}
		keys[n-1].rel.Fields = append(keys[n-1].rel.Fields, columnName)
		keys[n-1].rel.References = append(keys[n-1].rel.References, foreignColumn)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range keys {
		key.model.Relations = append(key.model.Relations, key.rel)
	}
	return nil
}

// loadPostgresTriggers reads GCORM-managed row triggers, whose function lives
// in the table's schema. Triggers PostgreSQL clones onto partitions are
// skipped with their tables.
func loadPostgresTriggers(ctx context.Context, db sqlRunner, schemas []string, catalog *postgresCatalog) error {
	rows, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname, t.tgname, t.tgtype, p.proname, p.prosrc,
       t.tgenabled <> 'O' OR t.tgqual IS NOT NULL OR t.tgnargs <> 0 OR t.tgattr <> ''::int2vector
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid AND p.pronamespace = n.oid
WHERE n.nspname = ANY($1::text[]) AND NOT t.tgisinternal AND NOT c.relispartition
ORDER BY n.nspname, c.relname, t.tgname`, postgresTextArray(schemas))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schemaName, table, function string
		var flags int
		trigger := &ir.Trigger{}
		if err := rows.Scan(&schemaName, &table, &trigger.Name, &flags, &function, &trigger.Body, &trigger.NeedsReplacement); err != nil {
			return err
		}
		model := catalog.model(schemaName, table)
		if model == nil || function != triggerddl.PostgresFunctionName(table, trigger.Name) || flags&1 == 0 {
			continue
		}
		// pg_trigger.tgtype stores row, before, insert, delete, and update
		// as successive bits. Only one operation is supported per definition.
		switch flags &^ 3 {
		case 4:
			trigger.Event = "INSERT"
		case 8:
			trigger.Event = "DELETE"
		case 16:
			trigger.Event = "UPDATE"
		default:
			continue
		}
		trigger.Timing = "AFTER"
		if flags&2 != 0 {
			trigger.Timing = "BEFORE"
		}
		trigger.Body = strings.TrimSpace(trigger.Body)
		model.Triggers = append(model.Triggers, trigger)
	}
	return rows.Err()
}

// matchTruncatedPostgresNames gives live indexes and unique constraints the
// target's name when the live name is the target name truncated by
// PostgreSQL to 63 bytes, as older versions created long names unshortened.
func matchTruncatedPostgresNames(catalog *postgresCatalog) {
	for key, live := range catalog.models {
		target := catalog.targets[key]
		if target == nil {
			continue
		}
		for _, idx := range live.Indexes {
			for _, targetIndex := range target.Indexes {
				name, full := migrate.IndexNames(target, targetIndex)
				if len(full) > migrate.MaxIdentifierBytes && idx.Name != name && migrate.PostgresTruncatedIdentifier(full) == idx.Name {
					idx.Name = name
					break
				}
			}
		}
		for _, uc := range live.UniqueConstraints {
			for _, targetUnique := range target.UniqueConstraints {
				if len(targetUnique.Name) > migrate.MaxIdentifierBytes && migrate.PostgresTruncatedIdentifier(targetUnique.Name) == uc.Name {
					uc.Name = targetUnique.Name
					break
				}
			}
		}
	}
}

// adoptDeclaredUniqueIndexes reports a plain unique index as a unique
// constraint when the target declares a unique constraint on the same
// columns and the table has no such constraint. Such an index enforces the
// declaration; recreating it would fail when a foreign key depends on it.
func adoptDeclaredUniqueIndexes(catalog *postgresCatalog) {
	for key, live := range catalog.models {
		target := catalog.targets[key]
		if target == nil {
			continue
		}
		columnsOf := func(names []string) string {
			columns := make([]string, len(names))
			for i, name := range names {
				columns[i] = name
				for _, field := range target.Fields {
					if field.Name == name || migrate.ColumnName(field) == name {
						columns[i] = migrate.ColumnName(field)
						break
					}
				}
			}
			return strings.Join(columns, "\x00")
		}
		declared := map[string]bool{}
		for _, uc := range target.UniqueConstraints {
			declared[columnsOf(uc.Fields)] = true
		}
		for _, field := range target.ScalarFields() {
			if field.IsUnique && !field.IsID {
				declared[migrate.ColumnName(field)] = true
			}
		}
		for _, uc := range live.UniqueConstraints {
			delete(declared, strings.Join(uc.Fields, "\x00"))
		}
		kept := live.Indexes[:0]
		for _, idx := range live.Indexes {
			columns := strings.Join(idx.Fields, "\x00")
			if !idx.IsUnique || !declared[columns] || !isPlainPostgresIndex(idx) {
				kept = append(kept, idx)
				continue
			}
			delete(declared, columns)
			live.UniqueConstraints = append(live.UniqueConstraints, &ir.UniqueConstraint{Name: idx.Name, Fields: idx.Fields})
			if len(idx.Fields) == 1 {
				if field := findFieldByColumn(live, idx.Fields[0]); field != nil {
					field.IsUnique = true
				}
			}
		}
		live.Indexes = kept
	}
}

// isPlainPostgresIndex reports whether an index has only plain ascending
// columns with default options and no predicate, like a unique constraint's.
func isPlainPostgresIndex(idx *ir.Index) bool {
	if strings.TrimSpace(idx.Where) != "" {
		return false
	}
	for _, col := range idx.Columns {
		if col.Expression != "" || col.OpClass != "" || col.Collation != "" ||
			(col.Sort != "" && col.Sort != "ASC") || (col.Nulls != "" && col.Nulls != "LAST") {
			return false
		}
	}
	return true
}

// mapPostgresEnumDefault replaces the labels of an enum column default with
// the enum value names, as schema defaults name values rather than labels.
func mapPostgresEnumDefault(field *ir.Field, enums []*ir.Enum) {
	if field.Default == nil || field.Default.IsFunction {
		return
	}
	var enum *ir.Enum
	for _, candidate := range enums {
		if candidate.Name == field.EnumType {
			enum = candidate
		}
	}
	if enum == nil {
		return
	}
	valueName := func(label string) string {
		for _, value := range enum.Values {
			if value.Name == label && value.DBName == "" || value.DBName == label {
				return value.Name
			}
		}
		return label
	}
	if field.Default.IsArray {
		for i, label := range field.Default.ArrayValue {
			field.Default.ArrayValue[i] = valueName(label)
		}
		return
	}
	field.Default.Value = valueName(field.Default.Value)
	field.Default.IsString = false
}

// findTargetFieldByColumn finds the target field stored in column.
func findTargetFieldByColumn(model *ir.Model, column string) *ir.Field {
	for _, field := range model.ScalarFields() {
		if migrate.ColumnName(field) == column {
			return field
		}
	}
	return nil
}

// adoptDbGeneratedDefault treats a column default as the declared
// dbgenerated() expression. PostgreSQL stores the expression in a normalized
// form (md5(random()::text) becomes md5((random())::text)), so the text
// cannot be compared; changing the expression of an existing column default
// therefore needs a migration.
func adoptDbGeneratedDefault(field, target *ir.Field) {
	if target == nil || target.Default == nil || !target.Default.IsFunction || target.Default.FuncName != "dbgenerated" ||
		field.IsGenerated || field.Default == nil {
		return
	}
	if field.Default.IsFunction && field.Default.FuncName == "autoincrement" {
		return
	}
	clone := *target.Default
	clone.FuncArgs = append([]string(nil), target.Default.FuncArgs...)
	field.Default = &clone
}
