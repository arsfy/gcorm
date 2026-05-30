package dbpush

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/arsfy/gcorm/internal/config"
	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/schema/parser"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

// Run executes `gco db push`.
func Run(args []string) error {
	schemaPath := ""
	configPath := ""
	explicitURL := ""
	force := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--schema":
			if i+1 < len(args) {
				schemaPath = args[i+1]
				i++
			}
		case "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "--url":
			if i+1 < len(args) {
				explicitURL = args[i+1]
				i++
			}
		case "--force":
			force = true
		}
	}

	targetSchema, err := loadTargetSchema(schemaPath, configPath)
	if err != nil {
		return err
	}

	dsn, source, err := resolveURL(explicitURL, targetSchema)
	if err != nil {
		return err
	}

	provider := "postgresql"
	if targetSchema.Datasource != nil && targetSchema.Datasource.Provider != "" {
		provider = targetSchema.Datasource.Provider
	}
	if !isSupportedProvider(provider) {
		return fmt.Errorf("db push supports postgresql, mysql, and sqlite; got %q", provider)
	}

	db, err := sql.Open(driverName(provider), dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	currentSchema, err := introspectDatabase(ctx, db, provider, targetSchema, dsn)
	if err != nil {
		return fmt.Errorf("introspect database: %w", err)
	}

	cs := migrate.Diff(currentSchema, targetSchema)
	if len(cs.Changes) == 0 {
		fmt.Printf("db push: schema compiled with %d model(s). No database changes detected.\n", len(targetSchema.Models))
		return nil
	}

	risky := riskyChanges(cs)
	if len(risky) > 0 && !force {
		return fmt.Errorf("db push refused to apply %d potentially destructive change(s) without --force:\n%s", len(risky), strings.Join(risky, "\n"))
	}

	gen := migrate.DDLGenerator{Dialect: provider, Schema: targetSchema}
	sqlText := gen.GenerateUp(cs)
	stmts, unsupported := splitStatements(sqlText)
	if len(unsupported) > 0 {
		return fmt.Errorf("db push generated unsupported SQL:\n%s", strings.Join(unsupported, "\n"))
	}
	if len(stmts) == 0 {
		fmt.Printf("db push: schema compiled with %d model(s). No executable SQL generated.\n", len(targetSchema.Models))
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if provider == "postgresql" {
		schemaName := resolveSchemaName(targetSchema, dsn)
		if _, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+postgresQuoteIdent(schemaName)); err != nil {
			return fmt.Errorf("set search_path to %q: %w", schemaName, err)
		}
	}

	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("execute SQL %q: %w", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	fmt.Printf("db push: schema compiled with %d model(s). Applied %d change(s) using connection URL from %s.\n", len(targetSchema.Models), len(cs.Changes), source)
	return nil
}

func loadTargetSchema(schemaPath, configPath string) (*ir.Schema, error) {
	cfg, _, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	var roots []string
	if schemaPath != "" {
		roots = []string{schemaPath}
	} else {
		roots, err = config.DiscoverSchemaRoots(cfg, cwd)
		if err != nil {
			return nil, err
		}
	}

	files, err := config.DiscoverSchemaFiles(roots)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .gcorm schema files found in %v", roots)
	}

	fileContents := make(map[string][]byte, len(files))
	for _, f := range files {
		data, readErr := os.ReadFile(f)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", f, readErr)
		}
		fileContents[f] = data
	}

	ds, parseErr := parser.ParseMulti(fileContents)
	if parseErr != nil {
		return nil, fmt.Errorf("parse error: %w", parseErr)
	}

	result := compiler.Compile(ds)
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("schema compilation failed with %d error(s)", len(result.Errors))
	}
	if result.Schema == nil {
		return nil, fmt.Errorf("no schema produced")
	}
	return result.Schema, nil
}

func resolveURL(explicitURL string, schema *ir.Schema) (string, string, error) {
	provider := ""
	if schema != nil && schema.Datasource != nil {
		provider = schema.Datasource.Provider
	}

	if explicitURL != "" {
		normalizedURL, err := normalizeConnectionURL(explicitURL, provider)
		if err != nil {
			return "", "", err
		}
		return normalizedURL, "--url", nil
	}
	if schema != nil && schema.Datasource != nil {
		ds := schema.Datasource
		if ds.URLIsEnv {
			if ds.EnvVar == "" {
				return "", "", fmt.Errorf("datasource url uses env() but no variable name was provided")
			}
			value := os.Getenv(ds.EnvVar)
			if value == "" {
				return "", "", fmt.Errorf("datasource url uses env(%q), but %s is not set", ds.EnvVar, ds.EnvVar)
			}
			normalizedURL, err := normalizeConnectionURL(value, ds.Provider)
			if err != nil {
				return "", "", err
			}
			return normalizedURL, fmt.Sprintf("datasource env(%q)", ds.EnvVar), nil
		}
		if ds.URL != "" {
			normalizedURL, err := normalizeConnectionURL(ds.URL, ds.Provider)
			if err != nil {
				return "", "", err
			}
			return normalizedURL, "schema datasource", nil
		}
	}
	modelCount := 0
	if schema != nil {
		modelCount = len(schema.Models)
	}
	return "", "", fmt.Errorf("db push: schema compiled with %d model(s). Push to database requires a connection URL.\nSet datasource url in your .gcorm schema or provide --url flag.", modelCount)
}

func isSupportedProvider(provider string) bool {
	switch provider {
	case "postgresql", "mysql", "sqlite":
		return true
	default:
		return false
	}
}

func driverName(provider string) string {
	switch provider {
	case "postgresql":
		return "pgx"
	case "mysql":
		return "mysql"
	case "sqlite":
		return "sqlite"
	default:
		return provider
	}
}

func normalizeConnectionURL(rawURL, provider string) (string, error) {
	switch {
	case provider == "postgresql" || isPostgresURL(rawURL):
		return normalizePostgresURL(rawURL)
	case provider == "mysql":
		return normalizeMySQLURL(rawURL)
	case provider == "sqlite":
		return normalizeSQLiteURL(rawURL)
	default:
		return rawURL, nil
	}
}

func normalizePostgresURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse postgresql connection URL: %w", err)
	}
	if parsedURL.Scheme == "" {
		return rawURL, nil
	}

	query := parsedURL.Query()
	if schemaName := strings.TrimSpace(query.Get("schema")); schemaName != "" {
		query.Del("schema")
		if strings.TrimSpace(query.Get("search_path")) == "" {
			query.Set("search_path", schemaName)
		}
	}
	if strings.TrimSpace(query.Get("sslmode")) == "" && isLocalPostgresHost(parsedURL.Hostname()) {
		query.Set("sslmode", "disable")
	}
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func resolveSchemaName(schema *ir.Schema, dsn string) string {
	if schema != nil && schema.Datasource != nil && schema.Datasource.Schema != "" {
		return schema.Datasource.Schema
	}
	if schemaName := postgresSchemaFromURL(dsn); schemaName != "" {
		return schemaName
	}
	return "public"
}

func postgresSchemaFromURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	query := parsedURL.Query()
	if schemaName := strings.TrimSpace(query.Get("schema")); schemaName != "" {
		return schemaName
	}

	searchPath := strings.TrimSpace(query.Get("search_path"))
	if searchPath == "" {
		return ""
	}
	parts := strings.Split(searchPath, ",")
	first := strings.TrimSpace(parts[0])
	return strings.Trim(first, `"`)
}

func isPostgresURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.HasPrefix(lower, "postgresql://") || strings.HasPrefix(lower, "postgres://")
}

func normalizeMySQLURL(rawURL string) (string, error) {
	if strings.Contains(rawURL, "@tcp(") || strings.Contains(rawURL, "@unix(") {
		return rawURL, nil
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse mysql connection URL: %w", err)
	}
	if parsedURL.Scheme == "" || parsedURL.Scheme != "mysql" {
		return rawURL, nil
	}

	user := parsedURL.User.Username()
	password, hasPassword := parsedURL.User.Password()
	auth := user
	if hasPassword {
		auth += ":" + password
	}
	if auth != "" {
		auth += "@"
	}

	host := parsedURL.Host
	if host == "" {
		host = "localhost:3306"
	}
	dbName := strings.TrimPrefix(parsedURL.Path, "/")
	query := parsedURL.RawQuery
	if query != "" {
		query = "?" + query
	}
	return fmt.Sprintf("%stcp(%s)/%s%s", auth, host, dbName, query), nil
}

func normalizeSQLiteURL(rawURL string) (string, error) {
	lower := strings.ToLower(rawURL)
	switch {
	case strings.HasPrefix(lower, "sqlite://"):
		parsedURL, err := url.Parse(rawURL)
		if err != nil {
			return "", fmt.Errorf("parse sqlite connection URL: %w", err)
		}
		if parsedURL.Host != "" {
			return filepath.Join(string(filepath.Separator), parsedURL.Host, parsedURL.Path), nil
		}
		if parsedURL.Path != "" {
			return parsedURL.Path, nil
		}
		return ":memory:", nil
	case strings.HasPrefix(lower, "file:"), rawURL == ":memory:":
		return rawURL, nil
	default:
		return rawURL, nil
	}
}

func isLocalPostgresHost(host string) bool {
	if host == "" {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func riskyChanges(cs *migrate.Changeset) []string {
	var out []string
	for _, c := range cs.Changes {
		if c.Rollback == migrate.SafeRollback {
			continue
		}
		label := fmt.Sprintf("  - %s %s", c.Type, c.Model)
		if c.Field != "" {
			label += "." + c.Field
		}
		label += fmt.Sprintf(" [%s]", c.Rollback)
		out = append(out, label)
	}
	return out
}

func splitStatements(sqlText string) ([]string, []string) {
	parts := strings.Split(sqlText, ";")
	stmts := make([]string, 0, len(parts))
	var unsupported []string

	for _, part := range parts {
		lines := strings.Split(part, "\n")
		var kept []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "--") {
				unsupported = append(unsupported, trimmed)
				continue
			}
			kept = append(kept, line)
		}
		stmt := strings.TrimSpace(strings.Join(kept, "\n"))
		if stmt != "" {
			stmts = append(stmts, stmt)
		}
	}

	return stmts, unsupported
}

func introspectPostgres(ctx context.Context, db *sql.DB, schemaName string) (*ir.Schema, error) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			Schema:   schemaName,
		},
	}

	models, err := loadPostgresTables(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	if err := loadPostgresColumns(ctx, db, schemaName, models); err != nil {
		return nil, err
	}
	if err := loadPostgresPrimaryKeys(ctx, db, schemaName, models); err != nil {
		return nil, err
	}
	if err := loadPostgresUniqueConstraints(ctx, db, schemaName, models); err != nil {
		return nil, err
	}
	if err := loadPostgresIndexes(ctx, db, schemaName, models); err != nil {
		return nil, err
	}
	if err := loadPostgresForeignKeys(ctx, db, schemaName, models); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		schema.Models = append(schema.Models, models[name])
	}
	return schema, nil
}

func introspectDatabase(ctx context.Context, db *sql.DB, provider string, targetSchema *ir.Schema, dsn string) (*ir.Schema, error) {
	switch provider {
	case "postgresql":
		return introspectPostgres(ctx, db, resolveSchemaName(targetSchema, dsn))
	case "mysql":
		return introspectMySQL(ctx, db)
	case "sqlite":
		return introspectSQLiteWithTarget(ctx, db, targetSchema)
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func introspectMySQL(ctx context.Context, db *sql.DB) (*ir.Schema, error) {
	schema := &ir.Schema{Datasource: &ir.Datasource{Provider: "mysql"}}

	models, err := loadMySQLTables(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := loadMySQLColumns(ctx, db, models); err != nil {
		return nil, err
	}
	if err := loadMySQLPrimaryKeys(ctx, db, models); err != nil {
		return nil, err
	}
	if err := loadMySQLUniqueConstraints(ctx, db, models); err != nil {
		return nil, err
	}
	if err := loadMySQLIndexes(ctx, db, models); err != nil {
		return nil, err
	}
	if err := loadMySQLForeignKeys(ctx, db, models); err != nil {
		return nil, err
	}

	appendSortedModels(schema, models)
	return schema, nil
}

func introspectSQLite(ctx context.Context, db *sql.DB) (*ir.Schema, error) {
	return introspectSQLiteWithTarget(ctx, db, nil)
}

func introspectSQLiteWithTarget(ctx context.Context, db *sql.DB, targetSchema *ir.Schema) (*ir.Schema, error) {
	schema := &ir.Schema{Datasource: &ir.Datasource{Provider: "sqlite"}}

	models, err := loadSQLiteTables(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := loadSQLiteColumns(ctx, db, models, targetSchema); err != nil {
		return nil, err
	}
	if err := loadSQLiteIndexes(ctx, db, models); err != nil {
		return nil, err
	}
	if err := loadSQLiteForeignKeys(ctx, db, models); err != nil {
		return nil, err
	}

	appendSortedModels(schema, models)
	return schema, nil
}

func appendSortedModels(schema *ir.Schema, models map[string]*ir.Model) {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		schema.Models = append(schema.Models, models[name])
	}
}

func loadPostgresTables(ctx context.Context, db *sql.DB, schemaName string) (map[string]*ir.Model, error) {
	rows, err := db.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema = $1 AND table_type = 'BASE TABLE'
ORDER BY table_name`, schemaName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := make(map[string]*ir.Model)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		models[tableName] = &ir.Model{
			Name:   tableName,
			DBName: tableName,
			Schema: schemaName,
		}
	}
	return models, rows.Err()
}

func loadPostgresColumns(ctx context.Context, db *sql.DB, schemaName string, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT table_name, column_name, is_nullable, data_type, udt_name, column_default, is_identity
FROM information_schema.columns
WHERE table_schema = $1
ORDER BY table_name, ordinal_position`, schemaName)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tableName, columnName, isNullable, dataType, udtName, isIdentity string
		var columnDefault sql.NullString
		if err := rows.Scan(&tableName, &columnName, &isNullable, &dataType, &udtName, &columnDefault, &isIdentity); err != nil {
			return err
		}
		model := models[tableName]
		if model == nil {
			continue
		}
		field := &ir.Field{
			Name:       columnName,
			DBName:     columnName,
			Type:       ir.FieldKindScalar,
			IsOptional: isNullable == "YES",
			Default:    postgresDefaultValue(columnDefault.String, isIdentity == "YES"),
		}
		field.ScalarType, field.IsList = postgresColumnType(dataType, udtName)
		model.Fields = append(model.Fields, field)
	}
	return rows.Err()
}

func loadPostgresPrimaryKeys(ctx context.Context, db *sql.DB, schemaName string, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT tc.table_name, kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema = $1
  AND tc.constraint_type = 'PRIMARY KEY'
ORDER BY tc.table_name, kcu.ordinal_position`, schemaName)
	if err != nil {
		return err
	}
	defer rows.Close()

	grouped := map[string][]string{}
	for rows.Next() {
		var tableName, columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			return err
		}
		grouped[tableName] = append(grouped[tableName], columnName)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for tableName, fields := range grouped {
		model := models[tableName]
		if model == nil {
			continue
		}
		model.PrimaryKey = &ir.PrimaryKey{
			Fields:      fields,
			IsComposite: len(fields) > 1,
		}
		if len(fields) == 1 {
			if field := findFieldByColumn(model, fields[0]); field != nil {
				field.IsID = true
			}
		}
	}
	return nil
}

func loadPostgresUniqueConstraints(ctx context.Context, db *sql.DB, schemaName string, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT tc.table_name, tc.constraint_name, kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema = $1
  AND tc.constraint_type = 'UNIQUE'
ORDER BY tc.table_name, tc.constraint_name, kcu.ordinal_position`, schemaName)
	if err != nil {
		return err
	}
	defer rows.Close()

	type uniqueKey struct {
		table string
		name  string
	}
	grouped := map[uniqueKey][]string{}
	for rows.Next() {
		var tableName, constraintName, columnName string
		if err := rows.Scan(&tableName, &constraintName, &columnName); err != nil {
			return err
		}
		grouped[uniqueKey{table: tableName, name: constraintName}] = append(grouped[uniqueKey{table: tableName, name: constraintName}], columnName)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for key, fields := range grouped {
		model := models[key.table]
		if model == nil {
			continue
		}
		if len(fields) == 1 {
			if field := findFieldByColumn(model, fields[0]); field != nil {
				field.IsUnique = true
			}
		}
		model.UniqueConstraints = append(model.UniqueConstraints, &ir.UniqueConstraint{
			Name:   key.name,
			Fields: fields,
		})
	}
	return nil
}

func loadPostgresIndexes(ctx context.Context, db *sql.DB, schemaName string, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  t.relname AS table_name,
  i.relname AS index_name,
  ix.indisunique,
  array_agg(a.attname ORDER BY keys.ord) AS columns,
  array_agg(pg_get_indexdef(ix.indexrelid, keys.ord::int, false) ORDER BY keys.ord) AS column_defs,
  array_agg((ix.indoption::int2[])[keys.ord::int - 1] ORDER BY keys.ord) AS column_options,
  array_agg(opc.opcname ORDER BY keys.ord) AS opclasses,
  array_agg(CASE WHEN coll.oid IS NULL THEN '' ELSE collns.nspname || '.' || coll.collname END ORDER BY keys.ord) AS collations,
  pg_get_expr(ix.indpred, ix.indrelid) AS predicate
FROM pg_class t
JOIN pg_namespace ns ON ns.oid = t.relnamespace
JOIN pg_index ix ON t.oid = ix.indrelid
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS keys(attnum, ord) ON true
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = keys.attnum
JOIN LATERAL unnest(ix.indclass) WITH ORDINALITY AS classes(opcoid, ord) ON classes.ord = keys.ord
JOIN pg_opclass opc ON opc.oid = classes.opcoid
JOIN LATERAL unnest(ix.indcollation) WITH ORDINALITY AS index_collations(colloid, ord) ON index_collations.ord = keys.ord
LEFT JOIN pg_collation coll ON coll.oid = index_collations.colloid
LEFT JOIN pg_namespace collns ON collns.oid = coll.collnamespace
LEFT JOIN pg_constraint c ON c.conindid = ix.indexrelid
WHERE ns.nspname = $1
  AND t.relkind = 'r'
  AND NOT ix.indisprimary
  AND c.oid IS NULL
GROUP BY t.relname, i.relname, ix.indisunique, ix.indpred, ix.indrelid, ix.indexrelid
ORDER BY t.relname, i.relname`, schemaName)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tableName, indexName string
		var isUnique bool
		var columns, columnDefs, columnOptions, opclasses, collations []byte
		var predicate sql.NullString
		if err := rows.Scan(&tableName, &indexName, &isUnique, &columns, &columnDefs, &columnOptions, &opclasses, &collations, &predicate); err != nil {
			return err
		}
		model := models[tableName]
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

func loadPostgresForeignKeys(ctx context.Context, db *sql.DB, schemaName string, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  tc.table_name,
  tc.constraint_name,
  kcu.column_name,
  ccu.table_name AS foreign_table_name,
  ccu.column_name AS foreign_column_name,
  rc.delete_rule,
  rc.update_rule
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
JOIN information_schema.constraint_column_usage ccu
  ON tc.constraint_name = ccu.constraint_name
 AND tc.table_schema = ccu.constraint_schema
JOIN information_schema.referential_constraints rc
  ON tc.constraint_name = rc.constraint_name
 AND tc.table_schema = rc.constraint_schema
WHERE tc.table_schema = $1
  AND tc.constraint_type = 'FOREIGN KEY'
ORDER BY tc.table_name, tc.constraint_name, kcu.ordinal_position`, schemaName)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fkKey struct {
		table string
		name  string
	}
	grouped := map[fkKey]*ir.Relation{}
	for rows.Next() {
		var tableName, constraintName, columnName, foreignTable, foreignColumn, onDelete, onUpdate string
		if err := rows.Scan(&tableName, &constraintName, &columnName, &foreignTable, &foreignColumn, &onDelete, &onUpdate); err != nil {
			return err
		}
		key := fkKey{table: tableName, name: constraintName}
		rel := grouped[key]
		if rel == nil {
			rel = &ir.Relation{
				Name:           constraintName,
				ConstraintName: constraintName,
				FromModel:      tableName,
				ToModel:        foreignTable,
				OnDelete:       onDelete,
				OnUpdate:       onUpdate,
			}
			grouped[key] = rel
		}
		rel.Fields = append(rel.Fields, columnName)
		rel.References = append(rel.References, foreignColumn)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for key, rel := range grouped {
		model := models[key.table]
		if model == nil {
			continue
		}
		model.Relations = append(model.Relations, rel)
	}
	return nil
}

func loadMySQLTables(ctx context.Context, db *sql.DB) (map[string]*ir.Model, error) {
	rows, err := db.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := make(map[string]*ir.Model)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		models[tableName] = &ir.Model{Name: tableName, DBName: tableName}
	}
	return models, rows.Err()
}

func loadMySQLColumns(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT table_name, column_name, is_nullable, data_type, column_type, column_default, extra
FROM information_schema.columns
WHERE table_schema = DATABASE()
ORDER BY table_name, ordinal_position`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tableName, columnName, isNullable, dataType, columnType, extra string
		var columnDefault sql.NullString
		if err := rows.Scan(&tableName, &columnName, &isNullable, &dataType, &columnType, &columnDefault, &extra); err != nil {
			return err
		}
		model := models[tableName]
		if model == nil {
			continue
		}
		field := &ir.Field{
			Name:       columnName,
			DBName:     columnName,
			Type:       ir.FieldKindScalar,
			ScalarType: mysqlScalarType(dataType, columnType),
			IsOptional: isNullable == "YES",
			Default:    mysqlDefaultValue(columnDefault.String, extra),
		}
		model.Fields = append(model.Fields, field)
	}
	return rows.Err()
}

func loadMySQLPrimaryKeys(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT table_name, column_name
FROM information_schema.key_column_usage
WHERE table_schema = DATABASE()
  AND constraint_name = 'PRIMARY'
ORDER BY table_name, ordinal_position`)
	if err != nil {
		return err
	}
	defer rows.Close()

	grouped := map[string][]string{}
	for rows.Next() {
		var tableName, columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			return err
		}
		grouped[tableName] = append(grouped[tableName], columnName)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for tableName, fields := range grouped {
		model := models[tableName]
		if model == nil {
			continue
		}
		model.PrimaryKey = &ir.PrimaryKey{Fields: fields, IsComposite: len(fields) > 1}
		if len(fields) == 1 {
			if field := findFieldByColumn(model, fields[0]); field != nil {
				field.IsID = true
			}
		}
	}
	return nil
}

func loadMySQLUniqueConstraints(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT tc.table_name, tc.constraint_name, kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_schema = kcu.constraint_schema
 AND tc.constraint_name = kcu.constraint_name
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema = DATABASE()
  AND tc.constraint_type = 'UNIQUE'
ORDER BY tc.table_name, tc.constraint_name, kcu.ordinal_position`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type uniqueKey struct {
		table string
		name  string
	}
	grouped := map[uniqueKey][]string{}
	for rows.Next() {
		var tableName, constraintName, columnName string
		if err := rows.Scan(&tableName, &constraintName, &columnName); err != nil {
			return err
		}
		key := uniqueKey{table: tableName, name: constraintName}
		grouped[key] = append(grouped[key], columnName)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for key, fields := range grouped {
		model := models[key.table]
		if model == nil {
			continue
		}
		if len(fields) == 1 {
			if field := findFieldByColumn(model, fields[0]); field != nil {
				field.IsUnique = true
			}
		}
		model.UniqueConstraints = append(model.UniqueConstraints, &ir.UniqueConstraint{Name: key.name, Fields: fields})
	}
	return nil
}

func loadMySQLIndexes(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT table_name, index_name, column_name, seq_in_index
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND non_unique = 1
ORDER BY table_name, index_name, seq_in_index`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type indexKey struct {
		table string
		name  string
	}
	grouped := map[indexKey][]string{}
	for rows.Next() {
		var tableName, indexName, columnName string
		var seq int
		if err := rows.Scan(&tableName, &indexName, &columnName, &seq); err != nil {
			return err
		}
		key := indexKey{table: tableName, name: indexName}
		grouped[key] = append(grouped[key], columnName)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for key, fields := range grouped {
		model := models[key.table]
		if model == nil {
			continue
		}
		model.Indexes = append(model.Indexes, &ir.Index{Name: key.name, Fields: fields})
	}
	return nil
}

func loadMySQLForeignKeys(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  kcu.table_name,
  kcu.constraint_name,
  kcu.column_name,
  kcu.referenced_table_name,
  kcu.referenced_column_name,
  rc.delete_rule,
  rc.update_rule
FROM information_schema.key_column_usage kcu
JOIN information_schema.referential_constraints rc
  ON kcu.constraint_schema = rc.constraint_schema
 AND kcu.constraint_name = rc.constraint_name
WHERE kcu.table_schema = DATABASE()
  AND kcu.referenced_table_name IS NOT NULL
ORDER BY kcu.table_name, kcu.constraint_name, kcu.ordinal_position`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fkKey struct {
		table string
		name  string
	}
	grouped := map[fkKey]*ir.Relation{}
	for rows.Next() {
		var tableName, constraintName, columnName, foreignTable, foreignColumn, onDelete, onUpdate string
		if err := rows.Scan(&tableName, &constraintName, &columnName, &foreignTable, &foreignColumn, &onDelete, &onUpdate); err != nil {
			return err
		}
		key := fkKey{table: tableName, name: constraintName}
		rel := grouped[key]
		if rel == nil {
			rel = &ir.Relation{Name: constraintName, ConstraintName: constraintName, FromModel: tableName, ToModel: foreignTable, OnDelete: onDelete, OnUpdate: onUpdate}
			grouped[key] = rel
		}
		rel.Fields = append(rel.Fields, columnName)
		rel.References = append(rel.References, foreignColumn)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for key, rel := range grouped {
		model := models[key.table]
		if model == nil {
			continue
		}
		model.Relations = append(model.Relations, rel)
	}
	return nil
}

func loadSQLiteTables(ctx context.Context, db *sql.DB) (map[string]*ir.Model, error) {
	rows, err := db.QueryContext(ctx, `
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := make(map[string]*ir.Model)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		models[tableName] = &ir.Model{Name: tableName, DBName: tableName}
	}
	return models, rows.Err()
}

func loadSQLiteColumns(ctx context.Context, db *sql.DB, models map[string]*ir.Model, targetSchema *ir.Schema) error {
	for tableName, model := range models {
		autoIncrementColumns, err := loadSQLiteAutoIncrementColumns(ctx, db, tableName)
		if err != nil {
			return err
		}
		targetModel := findSQLiteTargetModel(targetSchema, tableName)

		rows, err := db.QueryContext(ctx, `SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, tableName)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var columnName, dataType string
			var notNull, pk int
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &columnName, &dataType, &notNull, &defaultValue, &pk); err != nil {
				_ = rows.Close()
				return err
			}
			targetField := findSQLiteTargetField(targetModel, columnName)
			scalarType := sqliteScalarTypeForTarget(dataType, targetField)
			def := sqliteDefaultValueForScalar(defaultValue.String, scalarType)
			if pk > 0 && autoIncrementColumns[columnName] {
				def = &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}
			}
			field := &ir.Field{
				Name:       columnName,
				DBName:     columnName,
				Type:       ir.FieldKindScalar,
				ScalarType: scalarType,
				IsList:     targetField != nil && targetField.IsList && sqliteTypeCompatibleWithTarget(dataType, targetField),
				IsOptional: notNull == 0 && pk == 0,
				IsID:       pk > 0,
				Default:    def,
			}
			model.Fields = append(model.Fields, field)
			if pk > 0 {
				if model.PrimaryKey == nil {
					model.PrimaryKey = &ir.PrimaryKey{}
				}
				model.PrimaryKey.Fields = append(model.PrimaryKey.Fields, columnName)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if model.PrimaryKey != nil {
			model.PrimaryKey.IsComposite = len(model.PrimaryKey.Fields) > 1
		}
	}
	return nil
}

func loadSQLiteAutoIncrementColumns(ctx context.Context, db *sql.DB, tableName string) (map[string]bool, error) {
	var createSQL sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, tableName).Scan(&createSQL); err != nil {
		return nil, err
	}
	return sqliteAutoIncrementColumns(createSQL.String), nil
}

func sqliteAutoIncrementColumns(createSQL string) map[string]bool {
	columns := map[string]bool{}
	open := strings.Index(createSQL, "(")
	close := strings.LastIndex(createSQL, ")")
	if open < 0 || close <= open {
		return columns
	}

	for _, def := range splitSQLiteColumnDefs(createSQL[open+1 : close]) {
		name, rest := parseSQLiteColumnDef(def)
		if name == "" {
			continue
		}
		upperRest := strings.ToUpper(rest)
		if strings.Contains(upperRest, "PRIMARY KEY") && strings.Contains(upperRest, "AUTOINCREMENT") {
			columns[name] = true
		}
	}
	return columns
}

func splitSQLiteColumnDefs(body string) []string {
	var defs []string
	start := 0
	depth := 0
	var quote rune
	for i, r := range body {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
		case '[':
			quote = ']'
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				defs = append(defs, body[start:i])
				start = i + 1
			}
		}
	}
	defs = append(defs, body[start:])
	return defs
}

func parseSQLiteColumnDef(def string) (string, string) {
	def = strings.TrimSpace(def)
	if def == "" {
		return "", ""
	}
	upper := strings.ToUpper(def)
	for _, prefix := range []string{"CONSTRAINT ", "PRIMARY ", "FOREIGN ", "UNIQUE ", "CHECK "} {
		if strings.HasPrefix(upper, prefix) {
			return "", ""
		}
	}

	switch def[0] {
	case '"', '`':
		name, rest := readDelimitedSQLiteIdentifier(def, def[0], def[0])
		return name, rest
	case '[':
		name, rest := readDelimitedSQLiteIdentifier(def, '[', ']')
		return name, rest
	default:
		for i, r := range def {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return def[:i], def[i:]
			}
		}
		return def, ""
	}
}

func readDelimitedSQLiteIdentifier(def string, open, close byte) (string, string) {
	if len(def) == 0 || def[0] != open {
		return "", def
	}
	var b strings.Builder
	for i := 1; i < len(def); i++ {
		if def[i] == close {
			if close == '"' && i+1 < len(def) && def[i+1] == '"' {
				b.WriteByte('"')
				i++
				continue
			}
			return b.String(), def[i+1:]
		}
		b.WriteByte(def[i])
	}
	return "", def
}

func findSQLiteTargetModel(schema *ir.Schema, tableName string) *ir.Model {
	if schema == nil {
		return nil
	}
	for _, model := range schema.Models {
		if model.TableName() == tableName {
			return model
		}
	}
	return nil
}

func findSQLiteTargetField(model *ir.Model, columnName string) *ir.Field {
	if model == nil {
		return nil
	}
	for _, field := range model.Fields {
		if field.Type == ir.FieldKindRelation {
			continue
		}
		if sqliteFieldColumnName(field) == columnName {
			return field
		}
	}
	return nil
}

func sqliteFieldColumnName(field *ir.Field) string {
	if field.DBName != "" {
		return field.DBName
	}
	return sqliteToSnakeCase(field.Name)
}

func sqliteToSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			result = append(result, '_')
		}
		result = append(result, unicode.ToLower(r))
	}
	return string(result)
}

func loadSQLiteIndexes(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	type sqliteIndex struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}

	for tableName, model := range models {
		rows, err := db.QueryContext(ctx, `SELECT seq, name, "unique", origin, partial FROM pragma_index_list(?)`, tableName)
		if err != nil {
			return err
		}
		var indexes []sqliteIndex
		for rows.Next() {
			var seq int
			var indexName, origin string
			var unique, partial int
			if err := rows.Scan(&seq, &indexName, &unique, &origin, &partial); err != nil {
				_ = rows.Close()
				return err
			}
			if origin == "pk" {
				continue
			}
			indexes = append(indexes, sqliteIndex{
				name:    indexName,
				unique:  unique == 1,
				origin:  origin,
				partial: partial == 1,
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		for _, idx := range indexes {
			fields, err := loadSQLiteIndexColumns(ctx, db, idx.name)
			if err != nil {
				return err
			}
			if idx.unique {
				if len(fields) == 1 {
					if field := findFieldByColumn(model, fields[0]); field != nil {
						field.IsUnique = true
					}
				}
				model.UniqueConstraints = append(model.UniqueConstraints, &ir.UniqueConstraint{Name: idx.name, Fields: fields})
				continue
			}
			model.Indexes = append(model.Indexes, &ir.Index{Name: idx.name, Fields: fields})
		}
	}
	return nil
}

func loadSQLiteIndexColumns(ctx context.Context, db *sql.DB, indexName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT seqno, cid, name FROM pragma_index_info(?)`, indexName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fields []string
	for rows.Next() {
		var seqno, cid int
		var columnName string
		if err := rows.Scan(&seqno, &cid, &columnName); err != nil {
			return nil, err
		}
		fields = append(fields, columnName)
	}
	return fields, rows.Err()
}

func loadSQLiteForeignKeys(ctx context.Context, db *sql.DB, models map[string]*ir.Model) error {
	for tableName, model := range models {
		rows, err := db.QueryContext(ctx, `SELECT id, seq, "table", "from", "to", on_update, on_delete, match FROM pragma_foreign_key_list(?)`, tableName)
		if err != nil {
			return err
		}
		grouped := map[int]*ir.Relation{}
		for rows.Next() {
			var id, seq int
			var refTable, fromColumn, toColumn, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &refTable, &fromColumn, &toColumn, &onUpdate, &onDelete, &match); err != nil {
				_ = rows.Close()
				return err
			}
			rel := grouped[id]
			if rel == nil {
				rel = &ir.Relation{
					Name:           fmt.Sprintf("fk_%s_%s", tableName, fromColumn),
					ConstraintName: fmt.Sprintf("fk_%s_%s", tableName, fromColumn),
					FromModel:      tableName,
					ToModel:        refTable,
					OnDelete:       onDelete,
					OnUpdate:       onUpdate,
				}
				grouped[id] = rel
			}
			rel.Fields = append(rel.Fields, fromColumn)
			rel.References = append(rel.References, toColumn)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		keys := make([]int, 0, len(grouped))
		for key := range grouped {
			keys = append(keys, key)
		}
		sort.Ints(keys)
		for _, key := range keys {
			model.Relations = append(model.Relations, grouped[key])
		}
	}
	return nil
}

func postgresColumnType(dataType, udtName string) (string, bool) {
	upperType := strings.ToUpper(strings.TrimSpace(dataType))
	upperUDT := strings.ToUpper(strings.TrimSpace(udtName))
	if upperType == "ARRAY" || strings.HasPrefix(upperUDT, "_") {
		return postgresScalarType("", strings.TrimPrefix(udtName, "_")), true
	}
	return postgresScalarType(dataType, udtName), false
}

func postgresScalarType(dataType, udtName string) string {
	upperType := strings.ToUpper(strings.TrimSpace(dataType))
	upperUDT := strings.ToUpper(strings.TrimSpace(udtName))
	switch {
	case upperUDT == "UUID":
		return "UUID"
	case strings.Contains(upperType, "BIGINT"), upperType == "INT8", upperUDT == "INT8":
		return "BigInt"
	case strings.Contains(upperType, "SMALLINT"), upperType == "INT2", upperUDT == "INT2":
		return "SmallInt"
	case strings.Contains(upperType, "INTEGER"), upperType == "INT4", upperUDT == "INT4":
		return "Int"
	case strings.Contains(upperType, "DOUBLE"), strings.Contains(upperType, "REAL"), upperUDT == "FLOAT8", upperUDT == "FLOAT4":
		return "Float"
	case strings.Contains(upperType, "NUMERIC"), strings.Contains(upperType, "DECIMAL"), upperUDT == "NUMERIC":
		return "Decimal"
	case strings.Contains(upperType, "BOOLEAN"), upperUDT == "BOOL":
		return "Boolean"
	case strings.Contains(upperType, "TIMESTAMP"), strings.Contains(upperType, "DATE"), strings.Contains(upperType, "TIME"), upperUDT == "TIMESTAMP", upperUDT == "TIMESTAMPTZ", upperUDT == "DATE", upperUDT == "TIME":
		return "DateTime"
	case strings.Contains(upperType, "BYTEA"), upperUDT == "BYTEA":
		return "Bytes"
	case strings.Contains(upperType, "JSON"), upperUDT == "JSON", upperUDT == "JSONB":
		return "Json"
	default:
		return "String"
	}
}

func postgresDefaultValue(def string, isIdentity bool) *ir.DefaultValue {
	if isIdentity {
		return &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}
	}
	def = strings.TrimSpace(def)
	if def == "" {
		return nil
	}
	lower := strings.ToLower(def)
	switch {
	case strings.Contains(lower, "nextval("):
		return &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}
	case strings.Contains(lower, "uuid"):
		return &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}
	case strings.Contains(lower, "now()"), strings.Contains(lower, "current_timestamp"):
		return &ir.DefaultValue{IsFunction: true, FuncName: "now"}
	case strings.HasPrefix(lower, "'{}'::"):
		return &ir.DefaultValue{IsArray: true}
	case strings.HasPrefix(lower, "array["):
		return &ir.DefaultValue{Value: strings.TrimSpace(def), IsString: true}
	default:
		value := normalizePostgresLiteralDefault(def)
		return &ir.DefaultValue{Value: value, IsString: !isNumericLiteral(value) && value != "true" && value != "false", IsNumber: isNumericLiteral(value), IsBool: value == "true" || value == "false"}
	}
}

func normalizePostgresLiteralDefault(def string) string {
	def = strings.TrimSpace(def)
	if idx := strings.Index(def, "::"); idx >= 0 {
		def = strings.TrimSpace(def[:idx])
	}
	if len(def) >= 2 && def[0] == '\'' && def[len(def)-1] == '\'' {
		def = def[1 : len(def)-1]
		def = strings.ReplaceAll(def, "''", "'")
	}
	return def
}

func mysqlScalarType(dataType, columnType string) string {
	upperType := strings.ToUpper(strings.TrimSpace(dataType))
	upperColumn := strings.ToUpper(strings.TrimSpace(columnType))
	switch {
	case strings.Contains(upperColumn, "CHAR(36)"):
		return "UUID"
	case strings.Contains(upperType, "BIGINT"):
		return "BigInt"
	case strings.Contains(upperType, "TINYINT") && strings.Contains(upperColumn, "TINYINT(1)"):
		return "Boolean"
	case strings.Contains(upperType, "SMALLINT"):
		return "SmallInt"
	case strings.Contains(upperType, "INT"), strings.Contains(upperType, "MEDIUMINT"), strings.Contains(upperType, "TINYINT"):
		return "Int"
	case strings.Contains(upperType, "DOUBLE"), strings.Contains(upperType, "FLOAT"):
		return "Float"
	case strings.Contains(upperType, "DECIMAL"), strings.Contains(upperType, "NUMERIC"):
		return "Decimal"
	case strings.Contains(upperType, "BOOL"):
		return "Boolean"
	case strings.Contains(upperType, "DATETIME"), strings.Contains(upperType, "TIMESTAMP"), strings.Contains(upperType, "DATE"), strings.Contains(upperType, "TIME"):
		return "DateTime"
	case strings.Contains(upperType, "BLOB"), strings.Contains(upperType, "BINARY"):
		return "Bytes"
	case strings.Contains(upperType, "JSON"):
		return "Json"
	default:
		return "String"
	}
}

func mysqlDefaultValue(def, extra string) *ir.DefaultValue {
	def = strings.TrimSpace(def)
	extra = strings.ToLower(strings.TrimSpace(extra))
	if strings.Contains(extra, "auto_increment") {
		return &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}
	}
	if def == "" {
		return nil
	}
	lower := strings.ToLower(def)
	switch {
	case strings.Contains(lower, "uuid"):
		return &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}
	case strings.Contains(lower, "current_timestamp"), strings.Contains(lower, "now()"):
		return &ir.DefaultValue{IsFunction: true, FuncName: "now"}
	default:
		return literalDefaultValue(def)
	}
}

func sqliteScalarType(dataType string) string {
	upperType := strings.ToUpper(strings.TrimSpace(dataType))
	switch {
	case strings.Contains(upperType, "BIGINT"):
		return "BigInt"
	case strings.Contains(upperType, "SMALLINT"):
		return "SmallInt"
	case strings.Contains(upperType, "INT"):
		return "Int"
	case strings.Contains(upperType, "REAL"), strings.Contains(upperType, "DOUBLE"), strings.Contains(upperType, "FLOAT"):
		return "Float"
	case strings.Contains(upperType, "DECIMAL"), strings.Contains(upperType, "NUMERIC"):
		return "Decimal"
	case strings.Contains(upperType, "BOOL"):
		return "Boolean"
	case strings.Contains(upperType, "DATE"), strings.Contains(upperType, "TIME"):
		return "DateTime"
	case strings.Contains(upperType, "BLOB"), strings.Contains(upperType, "BINARY"):
		return "Bytes"
	case strings.Contains(upperType, "JSON"):
		return "Json"
	default:
		return "String"
	}
}

func sqliteScalarTypeForTarget(dataType string, targetField *ir.Field) string {
	if targetField != nil && sqliteTypeCompatibleWithTarget(dataType, targetField) {
		return targetField.ScalarType
	}
	return sqliteScalarType(dataType)
}

func sqliteTypeCompatibleWithTarget(dataType string, targetField *ir.Field) bool {
	if targetField == nil {
		return false
	}
	upperType := strings.ToUpper(strings.TrimSpace(dataType))
	if targetField.IsList {
		return strings.Contains(upperType, "TEXT")
	}

	switch targetField.ScalarType {
	case "String", "Decimal", "DateTime", "Json", "UUID":
		return strings.Contains(upperType, "TEXT") ||
			strings.Contains(upperType, "CHAR") ||
			strings.Contains(upperType, "CLOB") ||
			strings.Contains(upperType, "DECIMAL") ||
			strings.Contains(upperType, "NUMERIC") ||
			strings.Contains(upperType, "DATE") ||
			strings.Contains(upperType, "TIME") ||
			strings.Contains(upperType, "JSON")
	case "Int", "SmallInt", "BigInt", "Boolean":
		return strings.Contains(upperType, "INT") || strings.Contains(upperType, "BOOL")
	case "Float":
		return strings.Contains(upperType, "REAL") ||
			strings.Contains(upperType, "DOUBLE") ||
			strings.Contains(upperType, "FLOAT")
	case "Bytes":
		return strings.Contains(upperType, "BLOB") || strings.Contains(upperType, "BINARY")
	default:
		return false
	}
}

func sqliteDefaultValue(def string) *ir.DefaultValue {
	return sqliteDefaultValueForScalar(def, "")
}

func sqliteDefaultValueForScalar(def, scalarType string) *ir.DefaultValue {
	def = strings.TrimSpace(def)
	if def == "" {
		return nil
	}
	def = trimDefaultParens(def)
	lower := strings.ToLower(def)
	switch {
	case strings.Contains(lower, "randomblob"):
		return &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}
	case strings.Contains(lower, "datetime('now')"), strings.Contains(lower, "current_timestamp"):
		return &ir.DefaultValue{IsFunction: true, FuncName: "now"}
	default:
		if scalarType != "Boolean" {
			if value := numericLiteralDefaultValue(def); value != nil {
				return value
			}
		}
		return literalDefaultValue(def)
	}
}

func trimDefaultParens(def string) string {
	for hasWrappingDefaultParens(def) {
		def = strings.TrimSpace(def[1 : len(def)-1])
	}
	return def
}

func hasWrappingDefaultParens(s string) bool {
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return false
	}
	depth := 0
	var quote rune
	for i, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(s)-1 {
				return false
			}
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0
}

func numericLiteralDefaultValue(def string) *ir.DefaultValue {
	unquoted := strings.Trim(def, "'\"")
	if isNumericLiteral(unquoted) {
		return &ir.DefaultValue{Value: unquoted, IsNumber: true}
	}
	return nil
}

func literalDefaultValue(def string) *ir.DefaultValue {
	unquoted := strings.Trim(def, "'\"")
	lower := strings.ToLower(unquoted)
	switch lower {
	case "true":
		return &ir.DefaultValue{Value: "true", IsBool: true}
	case "false":
		return &ir.DefaultValue{Value: "false", IsBool: true}
	}
	if lower == "1" {
		return &ir.DefaultValue{Value: "true", IsBool: true}
	}
	if lower == "0" {
		return &ir.DefaultValue{Value: "false", IsBool: true}
	}
	if isNumericLiteral(unquoted) {
		return &ir.DefaultValue{Value: unquoted, IsNumber: true}
	}
	return &ir.DefaultValue{Value: unquoted, IsString: true}
}

func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	for i, ch := range s {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '.' || (ch == '-' && i == 0) {
			continue
		}
		return false
	}
	return true
}

func sqliteQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func postgresQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func parsePostgresTextArray(input string) []string {
	input = strings.TrimSpace(strings.Trim(input, "{}"))
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.Trim(part, `"`))
	}
	return out
}

func parsePostgresIndexColumns(fields, defs, options, opclasses, collations []string) []ir.IndexColumn {
	cols := make([]ir.IndexColumn, len(fields))
	for i, field := range fields {
		cols[i] = ir.IndexColumn{Field: field}
		if i < len(defs) {
			cols[i] = parsePostgresIndexColumnDef(field, defs[i])
		}
		applyPostgresIndexCatalogOptions(&cols[i], arrayValueAt(options, i), arrayValueAt(opclasses, i), arrayValueAt(collations, i))
	}
	return cols
}

func arrayValueAt(values []string, idx int) string {
	if idx < len(values) {
		return values[idx]
	}
	return ""
}

func applyPostgresIndexCatalogOptions(col *ir.IndexColumn, optionText, opclass, collation string) {
	if col == nil {
		return
	}
	option, err := strconv.Atoi(strings.TrimSpace(optionText))
	if err == nil {
		desc := option&1 == 1
		nullsFirst := option&2 == 2
		if desc {
			col.Sort = "DESC"
		} else if col.Sort == "" {
			col.Sort = "ASC"
		}
		switch {
		case nullsFirst:
			col.Nulls = "FIRST"
		case desc:
			col.Nulls = "LAST"
		}
	}

	opclass = normalizePostgresArrayItem(opclass)
	if opclass != "" {
		col.OpClass = opclass
	}
	collation = normalizePostgresArrayItem(collation)
	if collation != "" {
		col.Collation = normalizePostgresIdentifierPath(collation)
	}
}

func normalizePostgresArrayItem(value string) string {
	value = strings.TrimSpace(strings.Trim(value, `"`))
	if strings.EqualFold(value, "NULL") {
		return ""
	}
	return value
}

func parsePostgresIndexColumnDef(field, def string) ir.IndexColumn {
	col := ir.IndexColumn{Field: field}
	rest := strings.TrimSpace(def)
	quotedField := `"` + strings.ReplaceAll(field, `"`, `""`) + `"`
	switch {
	case strings.HasPrefix(rest, quotedField):
		rest = strings.TrimSpace(strings.TrimPrefix(rest, quotedField))
	case strings.HasPrefix(rest, field):
		rest = strings.TrimSpace(strings.TrimPrefix(rest, field))
	}
	tokens := strings.Fields(rest)
	for i := 0; i < len(tokens); i++ {
		token := strings.Trim(tokens[i], ",")
		upper := strings.ToUpper(token)
		switch upper {
		case "COLLATE":
			if i+1 < len(tokens) {
				col.Collation = normalizePostgresIdentifierPath(tokens[i+1])
				i++
			}
		case "ASC", "DESC":
			col.Sort = upper
		case "NULLS":
			if i+1 < len(tokens) {
				next := strings.ToUpper(strings.Trim(tokens[i+1], ","))
				if next == "FIRST" || next == "LAST" {
					col.Nulls = next
					i++
				}
			}
		default:
			if col.OpClass == "" {
				col.OpClass = token
			}
		}
	}
	return col
}

func normalizePostgresIdentifierPath(s string) string {
	s = strings.Trim(s, ",")
	parts := strings.Split(s, ".")
	for i, part := range parts {
		parts[i] = strings.Trim(part, `"`)
	}
	return strings.Join(parts, ".")
}

func findFieldByColumn(model *ir.Model, column string) *ir.Field {
	for _, field := range model.Fields {
		if field.DBName == column || field.Name == column {
			return field
		}
	}
	return nil
}
