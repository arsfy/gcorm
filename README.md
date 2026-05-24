# GCORM

![Golang](https://img.shields.io/badge/-Golang%201.26-00acd7?style=flat-square&logo=go&logoColor=white)

GCORM is a schema-first ORM toolkit for Go. It provides a Prisma-like schema
language, a Go code generator, type-safe query builders, migration planning, and
database tooling for PostgreSQL, MySQL, and SQLite.

The project is designed around generated Go code: define your data model in
`.gcorm` files, run the CLI, and use the generated `client`, `query`, and
`model` packages in your application.

## Features

- Schema-first data modeling with `.gcorm` files
- Generated Go model structs and query helpers
- Type-safe CRUD builders for create, find, update, delete, bulk insert, upsert,
  aggregate, and group-by operations
- Parameterized SQL generation for normal user-provided values
- PostgreSQL, MySQL, and SQLite dialect support
- Migration diff generation with `up.sql`, `down.sql`, and manifest files
- Development utilities: `init`, `generate`, `fmt`, `validate`, `introspect`,
  `migrate`, and `db push`
- Raw SQL escape hatches when you need full control

## Docs

For full documentation, examples, and guides, visit the [GCORM Wiki](https://github.com/arsfy/gcorm/wiki).

## Status

GCORM is early-stage software. The public APIs and schema language may still
change. Review generated migrations before applying them to production
databases.

## Requirements

- Go 1.26 or newer, matching this repository's `go.mod`
- A SQL driver for your database in your application, for example `pgx`,
  `go-sql-driver/mysql`, or `modernc.org/sqlite`

## Installation

Install the CLI with:

```sh
go install github.com/arsfy/gcorm/cmd/gco@latest
```

Or run it from a checkout:

```sh
go run ./cmd/gco help
```

## Quick Start

Create a schema file, for example `schema/schema.gcorm`:

```gcorm
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator client {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}

model User {
  id        String   @id @default(uuid())
  email     String   @unique
  name      String?
  posts     Post[]
  createdAt DateTime @default(now())

  @@map("users")
}

model Post {
  id        String   @id @default(uuid())
  title     String
  content   String?
  published Boolean  @default(false)
  authorId  String
  author    User     @relation(fields: [authorId], references: [id])

  @@index([authorId])
}
```

Generate the Go client:

```sh
gco generate --schema schema
```

Use the generated packages:

```go
package main

import (
	"context"
	"database/sql"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"your-module/gen/client"
	"your-module/gen/query"
)

func main() {
	ctx := context.Background()

	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer db.Close()

	c := client.New(db, client.WithDialect("postgresql"))
	defer c.Close()

	users, err := c.User.Query().
		Where(query.User.Email.Contains("@example.com")).
		OrderBy(query.User.CreatedAt.Desc()).
		Take(20).
		Do(ctx)
	if err != nil {
		panic(err)
	}

	_ = users
}
```

## CLI

```text
Usage:
  gco <command> [flags]

Commands:
  init         Initialize a new GCORM schema interactively
  generate     Generate Go client code from schema
  fmt          Format schema files
  validate     Validate schema files
  introspect   Generate schema from existing database
  migrate      Manage database migrations
    diff       Generate migration from schema diff
    dev        Apply migrations in development mode
    deploy     Apply migrations in production mode
    resolve    Resolve migration state
  db push      Push schema changes directly to database
  version      Print version information
  upgrade      Upgrade gco when installed with go install
  help         Show this help message

Flags:
  --schema <path>    Path to schema directory or file
  --config <path>    Path to configuration file
```

`gco upgrade` checks GitHub releases and upgrades with the concrete latest
release tag:

```sh
go install github.com/arsfy/gcorm/cmd/gco@va.b.c
```

The upgrade command is limited to binaries installed with `go install`. If GCORM
was installed manually from a release archive, download and replace the binary
from GitHub Releases instead.

## Configuration

GCORM looks for `gco.config.yaml` or `gco.config.yml` in the current directory
and parent directories. You can also pass `--config` or set `GCO_CONFIG`.

Example:

```yaml
schemaRoots:
  - schema
migrationDir: migrations
format:
  indentWidth: 2
```

If no config is present, GCORM discovers schema files from `schema/`, `prisma/`,
or the current directory.

## Migrations

Create a migration from the current schema:

```sh
gco migrate diff --name init --schema schema
```

This creates a timestamped directory under `migrations/` containing:

- `up.sql`
- `down.sql`
- `manifest.json`

Development and deployment helpers are available:

```sh
gco migrate dev --name add_posts --schema schema
gco migrate deploy --dir migrations
gco migrate resolve --applied <migration_id>
```

Review generated SQL before applying it, especially destructive changes and
SQLite table rebuild scenarios.

## Query Examples

Create a row:

```go
user, err := c.User.Create().
	Set(
		query.User.Email.Set("ada@example.com"),
		query.User.Name.Set("Ada"),
	).
	Do(ctx)
```

Find rows:

```go
users, err := c.User.Query().
	Where(
		query.User.Email.Contains("@example.com"),
		query.User.Name.StartsWith("A"),
	).
	OrderBy(query.User.CreatedAt.Desc()).
	Take(50).
	Do(ctx)
```

Update rows:

```go
updated, err := c.User.Update().
	Where(query.User.Email.Equals("ada@example.com")).
	Set(query.User.Name.Set("Ada Lovelace")).
	Do(ctx)
```

Delete rows:

```go
deleted, err := c.User.Delete().
	Where(query.User.Email.Equals("ada@example.com")).
	Do(ctx)
```

Bulk insert:

```go
count, err := c.Post.BulkCreate([]query.PostCreateInput{
	{Id: "p1", Title: "First", Published: true, AuthorId: "u1"},
	{Id: "p2", Title: "Second", Published: false, AuthorId: "u1"},
}).BatchSize(500).Do(ctx)
```

Raw SQL:

```go
rows, err := client.Raw[model.Post](
	ctx,
	c,
	"SELECT id, title, published, author_id FROM posts WHERE author_id = $1",
	"u1",
)
```

## Security Notes

Normal values passed through generated query helpers are sent as SQL parameters.
For example, `Equals`, `In`, `Contains`, `StartsWith`, `EndsWith`, and `Set`
do not concatenate user-provided values into SQL text.

String search helpers escape SQL `LIKE` wildcards so user input such as `%` and
`_` is treated as literal text by default.

Raw SQL APIs and manually constructed clause structs are escape hatches. Treat
them as trusted-code APIs and do not pass untrusted strings as SQL fragments,
column names, operators, or function names.

## Testing

Run the test suite:

```sh
go test ./...
```

Run runtime query-builder benchmarks:

```sh
go test ./pkg/runtime/sqlbuilder -bench=. -benchmem -run=^$
```

Run generated-client runtime benchmarks:

```sh
GCO_RUN_GENERATED_BENCH=1 go test -v ./pkg/codegen/golang \
  -run TestGeneratedClientBulkCreateAndRawRuntime -count=1
```

## Release Builds

Release archive builds can inject the CLI version with Go linker flags:

```sh
go build -ldflags "-X main.Version=v0.1.0" ./cmd/gco
```

When installed with `go install github.com/arsfy/gcorm/cmd/gco@v0.1.0`, GCORM
uses Go build metadata to report the module version and allows `gco upgrade` to
upgrade the CLI through `go install`. Manually downloaded binaries should be
upgraded manually from GitHub Releases.

## Project Layout

```text
cmd/gco                    CLI entry point
pkg/schema                 Parser, formatter, validator, resolver, compiler
pkg/codegen/golang         Go client generator
pkg/runtime                Runtime interfaces, dialects, SQL builder, errors
pkg/tooling                CLI tooling for generate, migrate, db push, fmt
examples                   Example schemas and usage
testdata                   Schema fixtures
```

## License

GCORM is licensed under the MIT License. See [LICENSE](LICENSE).
