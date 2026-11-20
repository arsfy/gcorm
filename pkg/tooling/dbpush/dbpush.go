package dbpush

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/buildinfo"
	"github.com/arsfy/gcorm/pkg/tooling/internal/schemautil"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

const (
	schemaPushesTable = "__gco_schema_pushes"
	migrationsTable   = "__gco_migrations"
)

type sqlRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type transactionalRunner interface {
	sqlRunner
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// IntrospectDatabase exposes the live-database reflection used by db push so
// the introspect CLI and schema push always observe the same database shape.
func IntrospectDatabase(ctx context.Context, db *sql.DB, provider string, target *ir.Schema, dsn string) (*ir.Schema, error) {
	return introspectDatabase(ctx, db, provider, target, dsn)
}

// NormalizeConnectionURL converts user-facing URLs to database/sql driver DSNs.
func NormalizeConnectionURL(rawURL, provider string) (string, error) {
	return normalizeConnectionURL(rawURL, provider)
}

// Options configures an importable db push operation.
type Options struct {
	SchemaFS          fs.FS
	SchemaRoot        string
	DatabaseURL       string
	AllowDestructive  bool
	DisableLock       bool
	DryRun            bool
	SkipIntrospection bool
}

// Result summarizes a db push operation.
type Result struct {
	Provider          string
	SchemaHash        string
	ModelCount        int
	ChangeCount       int
	Statements        []string
	AppliedStatements []string
	Noop              bool
}

// Push compiles embedded .gcorm schema files, compares them to the live
// database, and applies the generated DDL. PostgreSQL and SQLite apply
// executable DDL in a transaction. MySQL DDL is not atomic: if one statement in
// a multi-statement push fails, earlier MySQL DDL statements may already be
// committed by the server.
func Push(ctx context.Context, db *sql.DB, opts Options) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.SchemaFS == nil {
		return nil, fmt.Errorf("db push: SchemaFS is required")
	}

	loaded, err := schemautil.LoadFS(opts.SchemaFS, opts.SchemaRoot)
	if err != nil {
		return nil, err
	}
	if opts.SkipIntrospection {
		if !opts.DryRun {
			return nil, fmt.Errorf("db push: SkipIntrospection requires DryRun")
		}
		return planWithoutIntrospection(loaded.Schema, loaded.Hash)
	}
	if db == nil {
		return nil, fmt.Errorf("db push: nil database")
	}
	dsn, _, err := resolveURL(opts.DatabaseURL, loaded.Schema)
	if err != nil {
		return nil, err
	}
	return pushCompiled(ctx, db, loaded.Schema, loaded.Hash, dsn, opts)
}

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

	loaded, err := schemautil.LoadFromConfig(schemaPath, configPath)
	if err != nil {
		return err
	}
	targetSchema := loaded.Schema

	dsn, source, err := resolveURL(explicitURL, targetSchema)
	if err != nil {
		return err
	}

	provider, err := detectProvider(targetSchema)
	if err != nil {
		return err
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

	result, err := pushCompiled(ctx, db, targetSchema, loaded.Hash, dsn, Options{
		DatabaseURL:      explicitURL,
		AllowDestructive: force,
	})
	if err != nil {
		return err
	}
	if result.Noop {
		fmt.Printf("db push: schema compiled with %d model(s). No database changes detected.\n", result.ModelCount)
		return nil
	}
	if len(result.Statements) == 0 {
		fmt.Printf("db push: schema compiled with %d model(s). No executable SQL generated.\n", result.ModelCount)
		return nil
	}
	fmt.Printf("db push: schema compiled with %d model(s). Applied %d change(s) using connection URL from %s.\n", result.ModelCount, result.ChangeCount, source)
	return nil
}

func pushCompiled(ctx context.Context, db *sql.DB, targetSchema *ir.Schema, schemaHash, dsn string, opts Options) (*Result, error) {
	provider, err := detectProvider(targetSchema)
	if err != nil {
		return nil, err
	}
	if err := validateNoInternalTableConflict(targetSchema); err != nil {
		return nil, err
	}
	if provider == "mysql" {
		return pushCompiledMySQL(ctx, db, targetSchema, schemaHash, dsn, opts)
	}
	if provider == "sqlite" && !opts.DryRun {
		return pushCompiledSQLite(ctx, db, targetSchema, schemaHash, dsn, opts)
	}
	return pushTransactional(ctx, db, provider, targetSchema, schemaHash, dsn, opts, false)
}

func pushTransactional(ctx context.Context, db transactionalRunner, provider string, targetSchema *ir.Schema, schemaHash, dsn string, opts Options, checkForeignKeys bool) (*Result, error) {
	return pushTransactionalPhase(ctx, db, provider, targetSchema, schemaHash, dsn, opts, checkForeignKeys, nil)
}

// beginPushTransaction starts a locked push transaction. PostgreSQL resolves
// unqualified names in the default schema and then in public.
func beginPushTransaction(ctx context.Context, db transactionalRunner, provider string, targetSchema *ir.Schema, dsn string, opts Options) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	if provider == "postgresql" {
		defaultSchema := resolveSchemaName(targetSchema, dsn)
		// PostgreSQL skips missing schemas in search_path, so objects for a
		// missing default schema would silently land in public. Create it.
		if err := ensurePostgresSchema(ctx, tx, defaultSchema); err != nil {
			tx.Rollback()
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, postgresSearchPathSQL(defaultSchema)); err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("set postgresql search_path: %w", err)
		}
	}
	if !opts.DisableLock {
		if err := lockPushTransaction(ctx, tx, provider); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
}

// pushTransactionalPhase plans and applies a push in one transaction. When
// the plan starts with PostgreSQL steps that must be committed before later
// statements use them (new enum values), those steps are applied and
// committed first, and the remaining changes are planned again in a new
// locked transaction from the updated database. committed carries the result
// of that first phase.
func pushTransactionalPhase(ctx context.Context, db transactionalRunner, provider string, targetSchema *ir.Schema, schemaHash, dsn string, opts Options, checkForeignKeys bool, committed *Result) (*Result, error) {
	var tx *sql.Tx
	var runner sqlRunner = db
	if !opts.DryRun {
		var err error
		tx, err = beginPushTransaction(ctx, db, provider, targetSchema, dsn, opts)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		runner = tx
	}

	currentSchema, err := introspectDatabase(ctx, runner, provider, targetSchema, dsn)
	if err != nil {
		return nil, fmt.Errorf("introspect database: %w", err)
	}

	cs := migrate.Diff(currentSchema, targetSchema)
	if provider == "postgresql" {
		cs, err = dropEquivalentPostgresIndexRebuilds(ctx, db, tx, cs, targetSchema)
		if err != nil {
			return nil, err
		}
	}
	result := &Result{
		Provider:    provider,
		SchemaHash:  schemaHash,
		ModelCount:  len(targetSchema.Models),
		ChangeCount: len(cs.Changes),
	}
	if committed != nil {
		result.ChangeCount = committed.ChangeCount
		result.Statements = append(result.Statements, committed.Statements...)
		result.AppliedStatements = append(result.AppliedStatements, committed.AppliedStatements...)
	}
	if len(cs.Changes) == 0 {
		result.Noop = committed == nil
		if opts.DryRun {
			return result, nil
		}
		if err := ensureSchemaPushesTable(ctx, tx, provider); err != nil {
			return nil, err
		}
		if err := insertSchemaPushRecord(ctx, tx, provider, schemaHash, len(targetSchema.Models), result.ChangeCount); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit transaction: %w", err)
		}
		return result, nil
	}

	risky := riskyChanges(cs)
	if len(risky) > 0 && !opts.AllowDestructive {
		return nil, fmt.Errorf("db push refused to apply %d potentially destructive change(s) without --force:\n%s", len(risky), strings.Join(risky, "\n"))
	}

	gen := migrate.DDLGenerator{Dialect: provider, Schema: targetSchema}
	stmts, commitCount, unsupported := upStatements(gen, cs)
	if len(unsupported) > 0 {
		return nil, fmt.Errorf("db push generated unsupported SQL:\n%s", strings.Join(unsupported, "\n"))
	}
	result.Statements = append(result.Statements, stmts...)
	if len(stmts) == 0 {
		return result, nil
	}
	if opts.DryRun {
		return result, nil
	}

	if commitCount > 0 {
		if committed != nil {
			return nil, fmt.Errorf("db push: enum changes are still pending after they were committed")
		}
		for _, stmt := range stmts[:commitCount] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return nil, fmt.Errorf("execute SQL %s: %w", statementSummary(stmt), err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit enum changes: %w", err)
		}
		phase := &Result{ChangeCount: len(cs.Changes), Statements: stmts[:commitCount], AppliedStatements: stmts[:commitCount]}
		next, err := pushTransactionalPhase(ctx, db, provider, targetSchema, schemaHash, dsn, opts, checkForeignKeys, phase)
		if err != nil {
			return nil, fmt.Errorf("%w; the enum changes were already committed:\n%s", err, strings.Join(stmts[:commitCount], "\n"))
		}
		return next, nil
	}

	var views []string
	sqliteTableChange := provider == "sqlite" && sqliteChangesTableShape(cs)
	if sqliteTableChange {
		views, err = validSQLiteViews(ctx, tx)
		if err != nil {
			return nil, err
		}
	}
	if err := ensureSchemaPushesTable(ctx, tx, provider); err != nil {
		return nil, err
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("execute SQL %s: %w", statementSummary(stmt), err)
		}
	}
	for _, view := range views {
		if err := prepareSQLiteView(ctx, tx, view); err != nil {
			return nil, fmt.Errorf("SQLite schema change would break view %q: %w", view, err)
		}
	}
	if provider == "sqlite" {
		if err := checkSQLiteTriggerActions(ctx, tx, targetSchema); err != nil {
			return nil, err
		}
	}
	if checkForeignKeys && sqliteTableChange {
		if err := checkSQLiteForeignKeys(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := insertSchemaPushRecord(ctx, tx, provider, schemaHash, len(targetSchema.Models), result.ChangeCount); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	result.AppliedStatements = append(result.AppliedStatements, stmts...)

	return result, nil
}

func pushCompiledMySQL(ctx context.Context, db *sql.DB, targetSchema *ir.Schema, schemaHash, dsn string, opts Options) (result *Result, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire mysql connection: %w", err)
	}
	defer conn.Close()

	locked := false
	if !opts.DisableLock {
		if err := acquireMySQLPushLock(ctx, conn); err != nil {
			return nil, err
		}
		locked = true
	}
	defer func() {
		if locked {
			unlockErr := releaseMySQLPushLock(ctx, conn)
			if unlockErr != nil {
				err = errors.Join(err, unlockErr)
			}
		}
	}()

	currentSchema, err := introspectDatabase(ctx, conn, "mysql", targetSchema, dsn)
	if err != nil {
		return nil, fmt.Errorf("introspect database: %w", err)
	}

	cs := migrate.Diff(currentSchema, targetSchema)
	result = &Result{
		Provider:    "mysql",
		SchemaHash:  schemaHash,
		ModelCount:  len(targetSchema.Models),
		ChangeCount: len(cs.Changes),
	}
	if len(cs.Changes) == 0 {
		result.Noop = true
		if opts.DryRun {
			return result, nil
		}
		if err := ensureSchemaPushesTable(ctx, conn, "mysql"); err != nil {
			return nil, err
		}
		if err := insertSchemaPushRecord(ctx, conn, "mysql", schemaHash, len(targetSchema.Models), 0); err != nil {
			return nil, err
		}
		return result, nil
	}

	risky := riskyChanges(cs)
	if len(risky) > 0 && !opts.AllowDestructive {
		return nil, fmt.Errorf("db push refused to apply %d potentially destructive change(s) without --force:\n%s", len(risky), strings.Join(risky, "\n"))
	}

	gen := migrate.DDLGenerator{Dialect: "mysql", Schema: targetSchema}
	stmts, _, unsupported := upStatements(gen, cs)
	if len(unsupported) > 0 {
		return nil, fmt.Errorf("db push generated unsupported SQL:\n%s", strings.Join(unsupported, "\n"))
	}
	result.Statements = append(result.Statements, stmts...)
	if len(stmts) == 0 || opts.DryRun {
		return result, nil
	}

	if err := ensureSchemaPushesTable(ctx, conn, "mysql"); err != nil {
		return nil, err
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return result, fmt.Errorf("execute SQL %s after applying %d MySQL statement(s); earlier DDL may be committed: %w", statementSummary(stmt), len(result.AppliedStatements), err)
		}
		result.AppliedStatements = append(result.AppliedStatements, stmt)
	}
	if err := insertSchemaPushRecord(ctx, conn, "mysql", schemaHash, len(targetSchema.Models), len(cs.Changes)); err != nil {
		return result, fmt.Errorf("%w; MySQL DDL may already be committed", err)
	}
	return result, nil
}

func planWithoutIntrospection(targetSchema *ir.Schema, schemaHash string) (*Result, error) {
	provider, err := detectProvider(targetSchema)
	if err != nil {
		return nil, err
	}
	if err := validateNoInternalTableConflict(targetSchema); err != nil {
		return nil, err
	}

	cs := migrate.Diff(nil, targetSchema)
	result := &Result{
		Provider:    provider,
		SchemaHash:  schemaHash,
		ModelCount:  len(targetSchema.Models),
		ChangeCount: len(cs.Changes),
		Noop:        len(cs.Changes) == 0,
	}
	if len(cs.Changes) == 0 {
		return result, nil
	}

	gen := migrate.DDLGenerator{Dialect: provider, Schema: targetSchema}
	stmts, _, unsupported := upStatements(gen, cs)
	if len(unsupported) > 0 {
		return nil, fmt.Errorf("db push generated unsupported SQL:\n%s", strings.Join(unsupported, "\n"))
	}
	result.Statements = append(result.Statements, stmts...)
	return result, nil
}

func recordNoopPush(ctx context.Context, db *sql.DB, provider, schemaHash string, modelCount int, opts Options) (*Result, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if !opts.DisableLock {
		if err := lockPushTransaction(ctx, tx, provider); err != nil {
			return nil, err
		}
	}
	if err := ensureSchemaPushesTable(ctx, tx, provider); err != nil {
		return nil, err
	}
	if err := insertSchemaPushRecord(ctx, tx, provider, schemaHash, modelCount, 0); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &Result{
		Provider:    provider,
		SchemaHash:  schemaHash,
		ModelCount:  modelCount,
		ChangeCount: 0,
		Noop:        true,
	}, nil
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
		return applyDatasourceSchema(normalizedURL, schema)
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
			normalizedURL, err = withPostgresSearchPath(normalizedURL, ds)
			return normalizedURL, fmt.Sprintf("datasource env(%q)", ds.EnvVar), err
		}
		if ds.URL != "" {
			normalizedURL, err := normalizeConnectionURL(ds.URL, ds.Provider)
			if err != nil {
				return "", "", err
			}
			normalizedURL, err = withPostgresSearchPath(normalizedURL, ds)
			return normalizedURL, "schema datasource", err
		}
	}
	modelCount := 0
	if schema != nil {
		modelCount = len(schema.Models)
	}
	return "", "", fmt.Errorf("db push: schema compiled with %d model(s). Push to database requires a connection URL.\nSet datasource url in your .gcorm schema or provide --url flag.", modelCount)
}

func applyDatasourceSchema(normalizedURL string, schema *ir.Schema) (string, string, error) {
	if schema == nil || schema.Datasource == nil {
		return normalizedURL, "--url", nil
	}
	value, err := withPostgresSearchPath(normalizedURL, schema.Datasource)
	return value, "--url", err
}

func withPostgresSearchPath(rawURL string, ds *ir.Datasource) (string, error) {
	if ds == nil || ds.Provider != "postgresql" {
		return rawURL, nil
	}
	schemaName := strings.TrimSpace(ds.Schema)
	if schemaName == "" && len(ds.Schemas) == 1 {
		schemaName = strings.TrimSpace(ds.Schemas[0])
	}
	if schemaName == "" {
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		return rawURL, nil
	}
	q := u.Query()
	if existing := strings.TrimSpace(q.Get("search_path")); existing != "" && existing != schemaName {
		return "", fmt.Errorf("postgresql datasource schema %q conflicts with URL search_path %q", schemaName, existing)
	}
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func detectProvider(schema *ir.Schema) (string, error) {
	provider := "postgresql"
	if schema != nil && schema.Datasource != nil && schema.Datasource.Provider != "" {
		provider = schema.Datasource.Provider
	}
	if !isSupportedProvider(provider) {
		return "", fmt.Errorf("db push supports postgresql, mysql, and sqlite; got %q", provider)
	}
	return provider, nil
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
		table := c.Model
		if c.Schema != "" {
			table = c.Schema + "." + c.Model
		}
		label := fmt.Sprintf("  - %s %s", c.Type, table)
		if c.Field != "" {
			label += "." + c.Field
		}
		label += fmt.Sprintf(" [%s]", c.Rollback)
		out = append(out, label)
	}
	return out
}

func statementSummary(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return "<empty statement>"
	}
	fields := strings.Fields(stmt)
	if len(fields) == 0 {
		return "<empty statement>"
	}

	limit := 4
	if len(fields) < limit {
		limit = len(fields)
	}
	summary := strings.Join(fields[:limit], " ")
	if len(summary) > 120 {
		summary = summary[:120] + "..."
	}
	return summary
}

func splitStatements(sqlText, dialect string) ([]string, []string) {
	parts := splitSQLStatements(sqlText, dialect)
	stmts := make([]string, 0, len(parts))
	var unsupported []string

	for _, part := range parts {
		cleaned, comments := stripTopLevelSQLComments(part, dialect)
		unsupported = append(unsupported, comments...)
		stmt := strings.TrimSpace(cleaned)
		if stmt != "" {
			stmts = append(stmts, stmt)
		}
	}

	return stmts, unsupported
}

// upStatements returns the driver statements of a changeset. The first
// commitCount statements come from DDL steps that must be committed before
// the rest run (see migrate.DDLStep.RequiresCommit).
func upStatements(gen migrate.DDLGenerator, cs *migrate.Changeset) (statements []string, commitCount int, unsupported []string) {
	for _, step := range gen.UpSteps(cs) {
		var parsed []string
		if step.SingleStatement {
			parsed = []string{strings.TrimSuffix(step.SQL, ";")}
		} else {
			var comments []string
			parsed, comments = splitStatements(step.SQL, gen.Dialect)
			unsupported = append(unsupported, comments...)
		}
		statements = append(statements, parsed...)
		if step.RequiresCommit && commitCount == len(statements)-len(parsed) {
			commitCount = len(statements)
		}
	}
	return statements, commitCount, unsupported
}

func splitSQLStatements(sqlText, dialect string) []string {
	var parts []string
	var b strings.Builder
	state := "normal"
	dollarTag := ""
	singleEscape := false

	for i := 0; i < len(sqlText); i++ {
		c := sqlText[i]

		switch state {
		case "single":
			b.WriteByte(c)
			if singleEscape && c == '\\' && i+1 < len(sqlText) {
				i++
				b.WriteByte(sqlText[i])
				continue
			}
			if c == '\'' {
				if i+1 < len(sqlText) && sqlText[i+1] == '\'' {
					i++
					b.WriteByte(sqlText[i])
					continue
				}
				state = "normal"
			}
		case "double":
			b.WriteByte(c)
			if dialect == "mysql" && c == '\\' && i+1 < len(sqlText) {
				i++
				b.WriteByte(sqlText[i])
				continue
			}
			if c == '"' {
				if i+1 < len(sqlText) && sqlText[i+1] == '"' {
					i++
					b.WriteByte(sqlText[i])
					continue
				}
				state = "normal"
			}
		case "backtick":
			b.WriteByte(c)
			if c == '`' {
				state = "normal"
			}
		case "bracket":
			b.WriteByte(c)
			if c == ']' {
				state = "normal"
			}
		case "lineComment":
			b.WriteByte(c)
			if c == '\n' {
				state = "normal"
			}
		case "blockComment":
			b.WriteByte(c)
			if c == '*' && i+1 < len(sqlText) && sqlText[i+1] == '/' {
				i++
				b.WriteByte(sqlText[i])
				state = "normal"
			}
		case "dollar":
			if dollarTag != "" && strings.HasPrefix(sqlText[i:], dollarTag) {
				b.WriteString(dollarTag)
				i += len(dollarTag) - 1
				state = "normal"
				dollarTag = ""
				continue
			}
			b.WriteByte(c)
		default:
			switch {
			case c == ';':
				parts = append(parts, b.String())
				b.Reset()
			case c == '\'':
				b.WriteByte(c)
				state = "single"
				singleEscape = sqlStringBackslashEscapes(sqlText, i, dialect)
			case c == '"':
				b.WriteByte(c)
				state = "double"
			case c == '`':
				b.WriteByte(c)
				state = "backtick"
			case c == '[':
				b.WriteByte(c)
				state = "bracket"
			case c == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-':
				b.WriteByte(c)
				i++
				b.WriteByte(sqlText[i])
				state = "lineComment"
			case c == '/' && i+1 < len(sqlText) && sqlText[i+1] == '*':
				b.WriteByte(c)
				i++
				b.WriteByte(sqlText[i])
				state = "blockComment"
			case c == '$':
				tag := readDollarQuoteTag(sqlText[i:])
				if tag != "" {
					b.WriteString(tag)
					i += len(tag) - 1
					dollarTag = tag
					state = "dollar"
					continue
				}
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
		}
	}
	parts = append(parts, b.String())
	return parts
}

func stripTopLevelSQLComments(sqlText, dialect string) (string, []string) {
	var b strings.Builder
	var unsupported []string
	state := "normal"
	dollarTag := ""
	singleEscape := false

	for i := 0; i < len(sqlText); i++ {
		c := sqlText[i]

		switch state {
		case "single":
			b.WriteByte(c)
			if singleEscape && c == '\\' && i+1 < len(sqlText) {
				i++
				b.WriteByte(sqlText[i])
				continue
			}
			if c == '\'' {
				if i+1 < len(sqlText) && sqlText[i+1] == '\'' {
					i++
					b.WriteByte(sqlText[i])
					continue
				}
				state = "normal"
			}
		case "double":
			b.WriteByte(c)
			if dialect == "mysql" && c == '\\' && i+1 < len(sqlText) {
				i++
				b.WriteByte(sqlText[i])
				continue
			}
			if c == '"' {
				if i+1 < len(sqlText) && sqlText[i+1] == '"' {
					i++
					b.WriteByte(sqlText[i])
					continue
				}
				state = "normal"
			}
		case "backtick":
			b.WriteByte(c)
			if c == '`' {
				state = "normal"
			}
		case "bracket":
			b.WriteByte(c)
			if c == ']' {
				state = "normal"
			}
		case "blockComment":
			if c == '*' && i+1 < len(sqlText) && sqlText[i+1] == '/' {
				i++
				state = "normal"
			}
		case "dollar":
			if dollarTag != "" && strings.HasPrefix(sqlText[i:], dollarTag) {
				b.WriteString(dollarTag)
				i += len(dollarTag) - 1
				state = "normal"
				dollarTag = ""
				continue
			}
			b.WriteByte(c)
		default:
			switch {
			case c == '\'':
				b.WriteByte(c)
				state = "single"
				singleEscape = sqlStringBackslashEscapes(sqlText, i, dialect)
			case c == '"':
				b.WriteByte(c)
				state = "double"
			case c == '`':
				b.WriteByte(c)
				state = "backtick"
			case c == '[':
				b.WriteByte(c)
				state = "bracket"
			case c == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-':
				start := i
				i += 2
				for i < len(sqlText) && sqlText[i] != '\n' {
					i++
				}
				unsupported = append(unsupported, strings.TrimSpace(sqlText[start:i]))
				if i < len(sqlText) && sqlText[i] == '\n' {
					b.WriteByte('\n')
				}
			case c == '/' && i+1 < len(sqlText) && sqlText[i+1] == '*':
				i++
				state = "blockComment"
			case c == '$':
				tag := readDollarQuoteTag(sqlText[i:])
				if tag != "" {
					b.WriteString(tag)
					i += len(tag) - 1
					dollarTag = tag
					state = "dollar"
					continue
				}
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
		}
	}

	return b.String(), unsupported
}

// sqlStringBackslashEscapes reports whether a string literal opened at
// sqlText[i] (a single quote) treats backslash as an escape character.
// MySQL string literals do by default; in standard SQL only PostgreSQL
// escape strings (E'...') give backslash escape semantics.
func sqlStringBackslashEscapes(sqlText string, quoteIdx int, dialect string) bool {
	if dialect == "mysql" {
		return true
	}
	if quoteIdx < 1 || (sqlText[quoteIdx-1] != 'e' && sqlText[quoteIdx-1] != 'E') {
		return false
	}
	return quoteIdx < 2 || !isSQLIdentChar(sqlText[quoteIdx-2])
}

func isSQLIdentChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func readDollarQuoteTag(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1]
		}
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return ""
}

func lockPushTransaction(ctx context.Context, tx *sql.Tx, provider string) error {
	switch provider {
	case "postgresql":
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2824262875)`); err != nil {
			return fmt.Errorf("acquire db push lock: %w", err)
		}
	case "sqlite":
		return lockSQLitePush(ctx, tx)
	default:
		return fmt.Errorf("db push lock unsupported for provider %q", provider)
	}
	return nil
}

func acquireMySQLPushLock(ctx context.Context, conn *sql.Conn) error {
	var got int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('gco_dbpush', 60)`).Scan(&got); err != nil {
		return fmt.Errorf("acquire db push lock: %w", err)
	}
	if got != 1 {
		return fmt.Errorf("acquire db push lock: timed out")
	}
	return nil
}

func releaseMySQLPushLock(ctx context.Context, conn *sql.Conn) error {
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK('gco_dbpush')`).Scan(&released); err != nil {
		return fmt.Errorf("release db push lock: %w", err)
	}
	return nil
}

func ensureSchemaPushesTable(ctx context.Context, runner sqlRunner, provider string) error {
	_, err := runner.ExecContext(ctx, createSchemaPushesTableSQL(provider))
	if err != nil {
		return fmt.Errorf("create %s table: %w", schemaPushesTable, err)
	}
	return nil
}

func createSchemaPushesTableSQL(provider string) string {
	switch provider {
	case "mysql":
		return `CREATE TABLE IF NOT EXISTS __gco_schema_pushes (
	id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
	schema_hash VARCHAR(64) NOT NULL,
	provider VARCHAR(32) NOT NULL,
	model_count BIGINT NOT NULL,
	change_count BIGINT NOT NULL,
	applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
	tool_version VARCHAR(255) NOT NULL
)`
	case "sqlite":
		return `CREATE TABLE IF NOT EXISTS __gco_schema_pushes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	schema_hash TEXT NOT NULL,
	provider TEXT NOT NULL,
	model_count INTEGER NOT NULL,
	change_count INTEGER NOT NULL,
	applied_at TEXT NOT NULL DEFAULT (datetime('now')),
	tool_version TEXT NOT NULL
)`
	default:
		return `CREATE TABLE IF NOT EXISTS __gco_schema_pushes (
	id BIGSERIAL PRIMARY KEY,
	schema_hash TEXT NOT NULL,
	provider TEXT NOT NULL,
	model_count BIGINT NOT NULL,
	change_count BIGINT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	tool_version TEXT NOT NULL
)`
	}
}

func insertSchemaPushRecord(ctx context.Context, runner sqlRunner, provider, schemaHash string, modelCount, changeCount int) error {
	stmt := `INSERT INTO __gco_schema_pushes (schema_hash, provider, model_count, change_count, tool_version) VALUES (?, ?, ?, ?, ?)`
	args := []any{schemaHash, provider, modelCount, changeCount, buildinfo.Version()}
	if provider == "postgresql" {
		stmt = `INSERT INTO __gco_schema_pushes (schema_hash, provider, model_count, change_count, tool_version) VALUES ($1, $2, $3, $4, $5)`
	}
	if _, err := runner.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("record db push metadata: %w", err)
	}
	return nil
}

func isInternalTable(tableName string) bool {
	switch tableName {
	case schemaPushesTable, migrationsTable:
		return true
	default:
		return false
	}
}

func validateNoInternalTableConflict(schema *ir.Schema) error {
	if schema == nil {
		return nil
	}
	for _, model := range schema.Models {
		if isInternalTable(model.TableName()) {
			return fmt.Errorf("db push: model %s maps to reserved GCORM internal table %q", model.Name, model.TableName())
		}
	}
	return nil
}

func introspectDatabase(ctx context.Context, db sqlRunner, provider string, targetSchema *ir.Schema, dsn string) (*ir.Schema, error) {
	switch provider {
	case "postgresql":
		return introspectPostgresWithTarget(ctx, db, resolveSchemaName(targetSchema, dsn), targetSchema)
	case "mysql":
		return introspectMySQL(ctx, db)
	case "sqlite":
		return introspectSQLiteWithTarget(ctx, db, targetSchema)
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func introspectMySQL(ctx context.Context, db sqlRunner) (*ir.Schema, error) {
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
	if err := loadMySQLTriggers(ctx, db, models); err != nil {
		return nil, err
	}

	appendSortedModels(schema, models)
	return schema, nil
}

func introspectSQLite(ctx context.Context, db sqlRunner) (*ir.Schema, error) {
	return introspectSQLiteWithTarget(ctx, db, nil)
}

func introspectSQLiteWithTarget(ctx context.Context, db sqlRunner, targetSchema *ir.Schema) (*ir.Schema, error) {
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
	if err := loadSQLiteTriggers(ctx, db, schema, models); err != nil {
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

func postgresEnumDatabaseName(enum *ir.Enum) string {
	if enum == nil {
		return ""
	}
	if enum.DBName != "" {
		return enum.DBName
	}
	return enum.Name
}

func postgresIntrospectedEnum(databaseName string, target *ir.Enum) *ir.Enum {
	if target == nil {
		return &ir.Enum{Name: databaseName}
	}
	return &ir.Enum{Name: target.Name, DBName: target.DBName}
}

func postgresIntrospectedEnumValue(label string, target *ir.Enum) *ir.EnumValue {
	if target != nil {
		for _, value := range target.Values {
			databaseName := value.DBName
			if databaseName == "" {
				databaseName = value.Name
			}
			if databaseName == label {
				return &ir.EnumValue{Name: value.Name, DBName: value.DBName}
			}
		}
	}
	return &ir.EnumValue{Name: label}
}

func splitPostgresFormattedTypeArgs(formatted string) (base, args string, isList bool) {
	s := strings.TrimSpace(formatted)
	for strings.HasSuffix(s, "[]") {
		isList = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "[]"))
	}
	if start := strings.Index(s, "("); start >= 0 {
		if relEnd := strings.Index(s[start:], ")"); relEnd > 0 {
			end := start + relEnd
			args = s[start+1 : end]
			s = strings.Join(strings.Fields(s[:start]+" "+s[end+1:]), " ")
		}
	}
	return s, args, isList
}

func loadMySQLTables(ctx context.Context, db sqlRunner) (map[string]*ir.Model, error) {
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
		if isInternalTable(tableName) {
			continue
		}
		models[tableName] = &ir.Model{Name: tableName, DBName: tableName}
	}
	return models, rows.Err()
}

func loadMySQLColumns(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
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
		field.NativeType = mysqlNativeType(columnType, field.ScalarType)
		model.Fields = append(model.Fields, field)
	}
	return rows.Err()
}

func loadMySQLPrimaryKeys(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
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

func loadMySQLUniqueConstraints(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
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

func loadMySQLIndexes(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
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

func loadMySQLForeignKeys(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
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

func loadSQLiteTables(ctx context.Context, db sqlRunner) (map[string]*ir.Model, error) {
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
		if isInternalTable(tableName) {
			continue
		}
		models[tableName] = &ir.Model{Name: tableName, DBName: tableName}
	}
	return models, rows.Err()
}

func loadSQLiteColumns(ctx context.Context, db sqlRunner, models map[string]*ir.Model, targetSchema *ir.Schema) error {
	for tableName, model := range models {
		autoIncrementColumns, err := loadSQLiteAutoIncrementColumns(ctx, db, tableName)
		if err != nil {
			return err
		}
		targetModel := findSQLiteTargetModel(targetSchema, tableName)

		if err := func() error {
			rows, err := db.QueryContext(ctx, `SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, tableName)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var cid int
				var columnName, dataType string
				var notNull, pk int
				var defaultValue sql.NullString
				if err := rows.Scan(&cid, &columnName, &dataType, &notNull, &defaultValue, &pk); err != nil {
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
			return rows.Err()
		}(); err != nil {
			return err
		}
		if model.PrimaryKey != nil {
			model.PrimaryKey.IsComposite = len(model.PrimaryKey.Fields) > 1
		}
	}
	return nil
}

func loadSQLiteAutoIncrementColumns(ctx context.Context, db sqlRunner, tableName string) (map[string]bool, error) {
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

func loadSQLiteIndexes(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
	type sqliteIndex struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}

	for tableName, model := range models {
		var indexes []sqliteIndex
		if err := func() error {
			rows, err := db.QueryContext(ctx, `SELECT seq, name, "unique", origin, partial FROM pragma_index_list(?)`, tableName)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var seq int
				var indexName, origin string
				var unique, partial int
				if err := rows.Scan(&seq, &indexName, &unique, &origin, &partial); err != nil {
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
			return rows.Err()
		}(); err != nil {
			return err
		}

		for _, idx := range indexes {
			fields, err := loadSQLiteIndexColumns(ctx, db, idx.name)
			if err != nil {
				return err
			}
			if idx.partial {
				var createSQL string
				if err := db.QueryRowContext(ctx, "SELECT sql FROM main.sqlite_schema WHERE type = 'index' AND name = ?", idx.name).Scan(&createSQL); err != nil {
					return err
				}
				where := sqliteIndexPredicate(createSQL)
				if where == "" {
					return fmt.Errorf("cannot read SQLite partial index %q predicate", idx.name)
				}
				model.Indexes = append(model.Indexes, &ir.Index{Name: idx.name, Fields: fields, IsUnique: idx.unique, Where: where})
				continue
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

func loadSQLiteIndexColumns(ctx context.Context, db sqlRunner, indexName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT seqno, cid, name FROM pragma_index_info(?)`, indexName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fields []string
	for rows.Next() {
		var seqno, cid int
		var columnName sql.NullString
		if err := rows.Scan(&seqno, &cid, &columnName); err != nil {
			return nil, err
		}
		if !columnName.Valid {
			return nil, fmt.Errorf("SQLite expression index %q is not supported; cannot safely plan schema changes", indexName)
		}
		fields = append(fields, columnName.String)
	}
	return fields, rows.Err()
}

func loadSQLiteForeignKeys(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
	for tableName, model := range models {
		grouped := map[int]*ir.Relation{}
		if err := func() error {
			rows, err := db.QueryContext(ctx, `SELECT id, seq, "table", "from", "to", on_update, on_delete, match FROM pragma_foreign_key_list(?)`, tableName)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var id, seq int
				var refTable, fromColumn, toColumn, onUpdate, onDelete, match string
				if err := rows.Scan(&id, &seq, &refTable, &fromColumn, &toColumn, &onUpdate, &onDelete, &match); err != nil {
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
			return rows.Err()
		}(); err != nil {
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
	case strings.Contains(upperType, "TIMESTAMP"), strings.Contains(upperType, "DATE"), strings.Contains(upperType, "TIME"), upperUDT == "TIMESTAMP", upperUDT == "TIMESTAMPTZ", upperUDT == "DATE", upperUDT == "TIME", upperUDT == "TIMETZ":
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
	quotedLiteral := isSQLQuotedLiteralDefault(lower)
	switch {
	case !quotedLiteral && strings.Contains(lower, "nextval("):
		return &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}
	case !quotedLiteral && (containsSQLFunctionCall(lower, "gen_random_uuid") || containsSQLFunctionCall(lower, "uuid_generate")):
		return &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}
	case !quotedLiteral && (strings.Contains(lower, "now()") || strings.Contains(lower, "current_timestamp")):
		return &ir.DefaultValue{IsFunction: true, FuncName: "now"}
	case strings.HasPrefix(lower, "'{}'::") && postgresDefaultCastIsArray(lower):
		return &ir.DefaultValue{IsArray: true}
	case strings.HasPrefix(lower, "'{") && strings.Contains(lower, "}'::") && strings.HasSuffix(lower, "[]"):
		return &ir.DefaultValue{IsArray: true, ArrayValue: parsePostgresTextArray(strings.Trim(def[:indexPostgresCastOutsideQuotes(def)], "'"))}
	case strings.HasPrefix(lower, "array["):
		return &ir.DefaultValue{IsArray: true, ArrayValue: parsePostgresArrayDefault(def)}
	default:
		value := normalizePostgresLiteralDefault(def)
		return &ir.DefaultValue{Value: value, IsString: !isNumericLiteral(value) && value != "true" && value != "false", IsNumber: isNumericLiteral(value), IsBool: value == "true" || value == "false"}
	}
}

func postgresDefaultCastIsArray(def string) bool {
	castIdx := indexPostgresCastOutsideQuotes(def)
	if castIdx < 0 {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(def[castIdx+len("::"):]), "[]")
}

// isSQLQuotedLiteralDefault reports whether def is a quoted string literal,
// optionally followed by a PostgreSQL ::cast, e.g. 'uuid-v1'::text.
func isSQLQuotedLiteralDefault(def string) bool {
	def = strings.TrimSpace(def)
	if !strings.HasPrefix(def, "'") {
		return false
	}
	i := 1
	for i < len(def) {
		if def[i] == '\'' {
			if i+1 < len(def) && def[i+1] == '\'' {
				i += 2
				continue
			}
			i++
			break
		}
		i++
	}
	rest := strings.TrimSpace(def[i:])
	return rest == "" || strings.HasPrefix(rest, "::")
}

// containsSQLFunctionCall reports whether lower contains a call to a function
// whose name is fn or starts with fn (e.g. fn "uuid" matches "uuid(" and
// "uuid_generate_v4(", but not the literal "uuid-v1").
func containsSQLFunctionCall(lower, fn string) bool {
	for start := 0; start < len(lower); {
		idx := strings.Index(lower[start:], fn)
		if idx < 0 {
			return false
		}
		idx += start
		if idx > 0 && isSQLIdentChar(lower[idx-1]) {
			start = idx + len(fn)
			continue
		}
		j := idx + len(fn)
		for j < len(lower) && isSQLIdentChar(lower[j]) {
			j++
		}
		if j < len(lower) && lower[j] == '(' {
			return true
		}
		start = idx + len(fn)
	}
	return false
}

// indexPostgresCastOutsideQuotes returns the index of the first :: cast
// operator that is not inside a single-quoted string literal.
func indexPostgresCastOutsideQuotes(def string) int {
	inString := false
	for i := 0; i < len(def); i++ {
		c := def[i]
		if inString {
			if c == '\'' {
				if i+1 < len(def) && def[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
			continue
		}
		switch {
		case c == '\'':
			inString = true
		case c == ':' && i+1 < len(def) && def[i+1] == ':':
			return i
		}
	}
	return -1
}

func parsePostgresArrayDefault(def string) []string {
	def = strings.TrimSpace(def)
	lower := strings.ToLower(def)
	if !strings.HasPrefix(lower, "array[") {
		return nil
	}
	closeIdx := strings.LastIndex(def, "]")
	if closeIdx < len("array[") {
		return nil
	}
	body := def[len("array["):closeIdx]
	parts := splitPostgresArrayDefaultElements(body)
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := normalizePostgresLiteralDefault(strings.TrimSpace(part))
		values = append(values, value)
	}
	return values
}

func splitPostgresArrayDefaultElements(body string) []string {
	var parts []string
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
		case '\'':
			quote = r
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}

func normalizePostgresLiteralDefault(def string) string {
	def = strings.TrimSpace(def)
	if idx := indexPostgresCastOutsideQuotes(def); idx >= 0 {
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
	case upperColumn == "CHAR(36)":
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

func mysqlNativeType(columnType, scalar string) *ir.NativeType {
	lower := strings.ToLower(strings.TrimSpace(columnType))
	name := ""
	if (scalar == "String" && lower == "varchar(255)") || (scalar == "Decimal" && lower == "decimal(65,30)") {
		return nil
	}
	switch {
	case strings.HasPrefix(lower, "varchar("):
		name = "VarChar"
	case strings.HasPrefix(lower, "char(") && scalar != "UUID":
		name = "Char"
	case strings.HasPrefix(lower, "decimal("):
		name = "Decimal"
	default:
		return nil
	}
	start := strings.Index(lower, "(")
	end := strings.LastIndex(lower, ")")
	var args []string
	if start >= 0 && end > start {
		args = strings.Split(lower[start+1:end], ",")
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
		}
	}
	return &ir.NativeType{Name: name, Args: args}
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
	lower := strings.ToLower(trimDefaultParens(def))
	// COLUMN_DEFAULT omits quotes around string literals; EXTRA identifies
	// expression defaults even when their text is identical to a literal.
	if strings.Contains(extra, "default_generated") && !isSQLQuotedLiteralDefault(lower) {
		switch {
		case containsSQLFunctionCall(lower, "uuid"):
			return &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}
		case containsSQLFunctionCall(lower, "now"), containsSQLFunctionCall(lower, "current_timestamp"), lower == "current_timestamp":
			return &ir.DefaultValue{IsFunction: true, FuncName: "now"}
		}
	}
	return literalDefaultValue(def)
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

func parsePostgresTextArray(input string) []string {
	input = strings.TrimSpace(input)
	if input == "{}" || input == "" {
		return nil
	}
	input = strings.TrimPrefix(strings.TrimSuffix(input, "}"), "{")
	var out []string
	var item strings.Builder
	quoted := false
	escaped := false
	for _, r := range input {
		switch {
		case escaped:
			item.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, item.String())
			item.Reset()
		default:
			item.WriteRune(r)
		}
	}
	out = append(out, item.String())
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
		if cols[i].Expression != "" {
			cols[i].Expression = stripPostgresIndexExpressionOptions(cols[i].Expression, cols[i])
		}
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
	if field == "" {
		col.Expression = rest
		return col
	}
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

func stripPostgresIndexExpressionOptions(expression string, col ir.IndexColumn) string {
	expression = strings.TrimSpace(expression)
	expression = trimPostgresIndexSuffix(expression, "NULLS "+col.Nulls)
	expression = trimPostgresIndexSuffix(expression, col.Sort)
	expression = trimPostgresIndexSuffix(expression, col.OpClass)
	if col.Collation != "" {
		collation := strings.ReplaceAll(col.Collation, ".", `"."`)
		expression = trimPostgresIndexSuffix(expression, `COLLATE "`+collation+`"`)
		expression = trimPostgresIndexSuffix(expression, "COLLATE "+col.Collation)
	}
	return strings.TrimSpace(expression)
}

func trimPostgresIndexSuffix(value, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" || len(value) < len(suffix) {
		return value
	}
	start := len(value) - len(suffix)
	if strings.EqualFold(value[start:], suffix) && (start == 0 || value[start-1] == ' ') {
		return strings.TrimSpace(value[:start])
	}
	return value
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
