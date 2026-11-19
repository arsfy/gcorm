# Troubleshooting

## No Schema Files Found

Error:

```text
no .gcorm schema files found
```

Fixes:

- Pass `--schema schema`.
- Create `schema/schema.gcorm`.
- Set `schemaRoots` in `gco.config.yaml`.
- Check that files use the `.gcorm` extension.

## Multiple Schema Directories Found

If both `schema/` and `prisma/` exist, GCORM does not guess.

Fix:

```sh
gco validate --schema schema
```

or:

```yaml
schemaRoots:
  - schema
```

## Environment Variable Is Missing

Error:

```text
datasource url uses env("DATABASE_URL"), but DATABASE_URL is not set
```

Fix:

```sh
export DATABASE_URL='postgresql://postgres:postgres@localhost:5432/app?sslmode=disable'
```

or pass:

```sh
gco db push --url "$DATABASE_URL"
```

## Driver Is Missing

If your application fails to open a database driver, import the driver package.

PostgreSQL:

```go
import _ "github.com/jackc/pgx/v5/stdlib"
```

MySQL:

```go
import _ "github.com/go-sql-driver/mysql"
```

SQLite:

```go
import _ "modernc.org/sqlite"
```

## Wrong Dialect

PostgreSQL is the default generated client dialect. For MySQL and SQLite, pass
the dialect explicitly:

```go
c := client.New(db, client.WithDialect("mysql"))
c := client.New(db, client.WithDialect("sqlite"))
```

## MySQL DateTime Scanning Issues

Use `parseTime=true` in MySQL DSNs:

```text
user:password@tcp(localhost:3306)/app?parseTime=true
```

## PostgreSQL Local SSL Errors

Local PostgreSQL often needs:

```text
sslmode=disable
```

Example:

```text
postgresql://postgres:postgres@localhost:5432/app?sslmode=disable
```

## SQLite File Does Not Exist

Use a file URL that allows creation:

```text
file:./data/app.db?cache=shared&mode=rwc
```

Ensure the parent directory exists.

## Destructive DB Push Refused

`gco db push` refuses destructive changes unless `--force` is used.

Fix:

1. Review the schema change.
2. Back up important data.
3. Run:

```sh
gco db push --force
```

## Migrate Dev Or Deploy Did Not Apply SQL

Current migration commands generate or inspect migration files. They do not
currently execute SQL against a live database.

Use `gco db push` for direct development synchronization, or apply reviewed SQL
through your deployment system.

## Introspect Did Not Generate A Full Schema

`gco introspect` reads the live catalog through the same introspection used by
`gco db push`. Review the generated file for:

- `// ... is a read-only generated column` comments: the column is computed by
  the database. Its `@default(dbgenerated("expr"))` documents the expression;
  never write the field from application code. `db push` leaves generated
  columns alone, but the schema language cannot declare them, so
  `migrate init-sql`/`migrate diff` would create a plain `DEFAULT` instead
  (PostgreSQL rejects defaults that reference other columns). Create such
  columns with custom SQL in migrations.
- `// WARNING: ... nullable array column`: schema lists are always `NOT NULL`,
  so `db push` would add `NOT NULL` to that column. Backfill `NULL` rows first
  or keep the column out of pushes.
- `schema = "..."` in the datasource: the tables were read from that
  non-`public` PostgreSQL schema, so later pushes keep targeting it.
- `// WARNING:` comments for partial unique indexes and unique-index options
  that the schema language cannot express. Recreate those with custom SQL.
- Enum values or names with `@map`/`@@map`: the database label was not a valid
  schema identifier and was renamed in the schema only.

## Native Type Or @@schema Rejected

Validation rejects `@db.*` annotations that the provider's DDL cannot create
(for example `@db.TinyInt` or `@db.Blob` on PostgreSQL) or that do not match
the field type (for example `@db.Uuid` on an `Int`). See the native type table
in the [Schema Guide](Schema-Guide.md#native-types).

`@@schema("name")` is PostgreSQL-only. If the datasource sets `schemas`, add the
name to that list.

## Classifying Database Errors

`dialect.PostgreSQL{}.ClassifyError` uses the driver's SQLSTATE (pgx
`*pgconn.PgError`, lib/pq `*pq.Error`, or a `(SQLSTATE XXXXX)` suffix) and puts
`sqlstate`, `constraint`, `table` and `column` in `DBError.Meta` when known.
Mapped codes: `23505` unique, `23503` foreign key, `23502` not null, `23514`
check, `23P01` exclusion, `22001` truncation, `22003` numeric out of range,
`22P02` invalid text representation, `40001` serialization, `40P01` deadlock,
`57014` query canceled, `08xxx` and `53300` connection. Message text is only
inspected when no code is available, and never inside quoted identifiers.

## Generated Code Is Stale

After changing `.gcorm` files:

```sh
gco validate
gco generate
go test ./...
```

If imports fail, check the generator `output` path and Go module import path.

