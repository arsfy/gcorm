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

The current `gco introspect` command detects provider information and prints
guidance, but full live database introspection is not implemented there.

`gco db push` still performs internal introspection for schema comparison.

## Generated Code Is Stale

After changing `.gcorm` files:

```sh
gco validate
gco generate
go test ./...
```

If imports fail, check the generator `output` path and Go module import path.

