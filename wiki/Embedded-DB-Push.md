# Embedded DB Push

Embedded DB Push lets a production binary apply a trusted `.gcorm` schema
without installing the `gco` CLI on the target machine. Embed schema files with
Go's `embed` package and call `dbpush.Push` from an explicit migrator command,
job, or administrative startup path.

## When To Use It

Good fit:

- First-time initialization of an empty production database.
- Self-hosted deployments where shipping one binary is simpler than installing
  a separate CLI.
- Internal tools or controlled environments where direct schema synchronization
  is acceptable.
- Tenant/database bootstrap workflows.

Use reviewed SQL migrations instead when:

- Every production change must be reviewed as a stable SQL file.
- You need custom data backfills.
- Multiple application versions run at the same time during deploys.
- Destructive schema changes need a manual rollout plan.

## Embed Schema Files

Keep production credentials out of `.gcorm` files. Prefer `env()` in the
datasource block:

```gcorm
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
```

Embed the schema directory in the binary:

```go
package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/arsfy/gcorm/pkg/tooling/dbpush"
)

//go:embed schema/*.gcorm
var schemaFS embed.FS

func main() {
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	result, err := dbpush.Push(ctx, db, dbpush.Options{
		SchemaFS:         schemaFS,
		SchemaRoot:       "schema",
		DatabaseURL:      dsn,
		Lock:             true,
		AllowDestructive: false,
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("db push: models=%d changes=%d hash=%s noop=%v\n",
		result.ModelCount, result.ChangeCount, result.SchemaHash, result.Noop)
}
```

For MySQL or SQLite, open the database with the matching `database/sql` driver
and import that driver in the migrator binary.

## First Initialization

When the database has no user tables, `dbpush.Push` introspects an empty schema,
diffs it against the embedded `.gcorm` schema, and applies the full set of
`CREATE TABLE`, index, unique constraint, and foreign key statements that the
DDL generator supports.

This is equivalent to synchronizing from an empty database to the target schema.
It does not require a migration directory.

## Subsequent Schema Changes

On every run, `dbpush.Push`:

1. Compiles the embedded `.gcorm` files.
2. Resolves the connection URL from `Options.DatabaseURL` or the schema
   datasource.
3. Introspects the live database.
4. Computes a diff from the live database to the target schema.
5. Refuses destructive or review-required changes unless
   `AllowDestructive` is true.
6. Executes supported SQL in a transaction.
7. Records a row in `gco_schema_pushes` after a successful non-noop push.

If the live database already matches the embedded schema, the result has
`Noop=true` and no SQL is executed.

## Metadata Table

Successful non-noop pushes create and write to `gco_schema_pushes`. The table
stores:

- Schema hash.
- Provider.
- Model count.
- Change count.
- Applied time.
- GCORM tool version.

GCORM ignores `gco_schema_pushes` and `gco_migrations` during introspection, so
metadata tables do not appear as drift from the `.gcorm` schema.

The metadata table is for audit and visibility. Live database introspection is
still the source of truth for deciding what SQL to apply.

## Dry Run

Use `DryRun` to inspect the generated SQL statements without applying them:

```go
result, err := dbpush.Push(ctx, db, dbpush.Options{
	SchemaFS:    schemaFS,
	SchemaRoot:  "schema",
	DatabaseURL: os.Getenv("DATABASE_URL"),
	DryRun:      true,
})
```

`DryRun` does not create user tables and does not write `gco_schema_pushes`.

## Production Guidance

- Run embedded DB Push from one explicit migrator job or admin command, not
  from every web worker automatically.
- Keep `Lock=true` for production use.
- Keep `AllowDestructive=false` by default.
- Use a separate database credential with DDL permissions for the migrator.
- Keep application runtime credentials more restrictive when possible.
- Back up important databases before applying schema changes.
- Use `gco migrate init-sql` or reviewed migrations when you need stable SQL
  artifacts for approval.
