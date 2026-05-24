# Quick Start

This guide creates a small project with one `User` model, generates a Go client,
and runs a query.

## 1. Create Or Enter A Go Module

```sh
mkdir gcorm-demo
cd gcorm-demo
go mod init example.com/gcorm-demo
```

Install the CLI if it is not already installed:

```sh
go install github.com/arsfy/gcorm/cmd/gco@latest
```

## 2. Initialize A Schema

Interactive mode:

```sh
gco init
```

Non-interactive PostgreSQL example:

```sh
gco init --yes --provider postgresql --schema-file schema/schema.gcorm --env DATABASE_URL --output ./gen --package db
```

Supported providers are:

- `postgresql`
- `mysql`
- `sqlite`

## 3. Edit The Schema

Example `schema/schema.gcorm`:

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
  createdAt DateTime @default(now())

  @@map("users")
}
```

## 4. Validate, Format, And Generate

```sh
gco validate --schema schema
gco fmt --schema schema
gco generate --schema schema
```

Generated code is written to the configured generator output directory. The
usual package layout is:

```text
gen/client
gen/query
gen/model
```

## 5. Add A Database Driver

PostgreSQL example:

```sh
go get github.com/jackc/pgx/v5/stdlib
```

## 6. Push The Schema In Development

Set the connection string:

```sh
export DATABASE_URL='postgresql://postgres:postgres@localhost:5432/app?sslmode=disable'
```

Apply the schema directly:

```sh
gco db push --schema schema
```

For destructive changes, `db push` refuses to continue unless you pass
`--force`.

## 7. Use The Generated Client

```go
package main

import (
	"context"
	"database/sql"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"example.com/gcorm-demo/gen/client"
	"example.com/gcorm-demo/gen/query"
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

	user, err := c.User.Create().
		Set(
			query.User.Email.Set("ada@example.com"),
			query.User.Name.Set("Ada"),
		).
		Do(ctx)
	if err != nil {
		panic(err)
	}

	users, err := c.User.Query().
		Where(query.User.Email.Contains("@example.com")).
		OrderBy(query.User.CreatedAt.Desc()).
		Take(20).
		Do(ctx)
	if err != nil {
		panic(err)
	}

	_, _ = user, users
}
```

## Next Steps

- Use [Schema Guide](Schema-Guide.md) for model syntax.
- Use [Querying](Querying.md) for generated query helpers.
- Use [DB Push](DB-Push.md) for direct schema sync.
- Use [Migrations](Migrations.md) for migration file generation.

