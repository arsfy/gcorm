# Generated Client

GCORM generates Go code from `.gcorm` files. The generated code is intended to
be imported by your application and used with `database/sql`.

## Package Layout

The default generator output is usually:

```text
gen/client
gen/query
gen/model
```

Package roles:

- `client`: top-level database client, model clients, raw SQL helpers, and
  transaction entry points.
- `query`: type-safe predicates, setters, ordering helpers, and input structs.
- `model`: generated Go structs and enum types.

## Client Setup

PostgreSQL:

```go
import (
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"example.com/app/gen/client"
)

db, err := sql.Open("pgx", dsn)
if err != nil {
	return err
}

c := client.New(db, client.WithDialect("postgresql"))
defer c.Close()
```

MySQL:

```go
import _ "github.com/go-sql-driver/mysql"

db, err := sql.Open("mysql", dsn)
c := client.New(db, client.WithDialect("mysql"))
```

SQLite:

```go
import _ "modernc.org/sqlite"

db, err := sql.Open("sqlite", dsn)
c := client.New(db, client.WithDialect("sqlite"))
```

PostgreSQL is the default dialect, but passing `client.WithDialect(...)` makes
the intent clear and is required for MySQL and SQLite.

## Model Structs

A schema model like:

```gcorm
model User {
  id    String  @id @default(uuid())
  email String  @unique
  name  String?
}
```

generates a Go model struct in the `model` package. Nullable fields are
represented with pointer types.

```go
var u model.User
_ = u.Email
if u.Name != nil {
	_ = *u.Name
}
```

### Enums

Enum constants keep the schema member name in Go and hold the database value:
the member's `@map` name when present, otherwise the member name.

```gcorm
enum Role {
  ADMIN @map("admin")
  USER
}
```

```go
model.RoleADMIN // == "admin"
model.RoleUSER  // == "USER"
```

### Scalar Lists

List fields (`String[]`, `Int[]`, enum lists, ...) are Go slices. The client
encodes and scans them per dialect without extra dependencies: PostgreSQL uses
one-dimensional array literals (quotes, backslash escapes, `NULL`), MySQL and
SQLite store lists as JSON arrays (`JSON`/`TEXT` columns). A `NULL` column scans
as a nil slice, a `NULL` array element as the element's zero value, and a nil
slice is written as an empty list. `client.Raw[T]` scans slice fields the same
way.

### Generated Columns

Database-generated (computed) columns are read-only: they are part of the model
struct and can be filtered and ordered, but they have no `Set`/`SetNull`
helpers, are absent from `CreateInput`, and the client rejects writes to them.

## Table References and `@@schema`

On PostgreSQL a model with `@@schema("audit")` is addressed as
`"audit"."<table>"` in every generated statement. Without `@@schema` the bare
table name is used and resolved through the connection's `search_path`. MySQL
and SQLite always use the bare table name.

## Query Helpers

The `query` package exposes model-specific helpers:

```go
query.User.Email.Equals("ada@example.com")
query.User.Email.Contains("@example.com")
query.User.CreatedAt.Desc()
query.User.Name.Set("Ada")
```

These helpers build structured predicates and updates. Normal values are passed
as SQL parameters.

## Client Lifetime

The generated client wraps a `*sql.DB`. Closing the GCORM client should not be a
substitute for managing the database handle in your application. Prefer to close
both explicitly when your application shuts down:

```go
defer c.Close()
defer db.Close()
```

## Context

All execution methods accept `context.Context`:

```go
users, err := c.User.Query().Take(20).Do(ctx)
```

Use timeouts and request-scoped contexts in services:

```go
ctx, cancel := context.WithTimeout(parent, 2*time.Second)
defer cancel()
```

## Transactions

Generated clients include transaction support through the runtime transaction
APIs. Use transactions when multiple writes must commit or roll back together.

```go
err := c.Tx(ctx, func(tx *client.Client) error {
	_, err := tx.User.Create().
		Set(query.User.Email.Set("ada@example.com")).
		Do(ctx)
	if err != nil {
		return err
	}

	_, err = tx.AuditLog.Create().
		Set(query.AuditLog.Message.Set("created user")).
		Do(ctx)
	return err
})
```

Calling `Tx` on a transaction client opens a `SAVEPOINT` inside the enclosing
transaction instead of a new transaction. If the nested function returns an
error (or panics) the client issues `ROLLBACK TO SAVEPOINT`, so only the nested
work is undone and, on PostgreSQL, the outer transaction stays usable after a
failed statement; on success it issues `RELEASE SAVEPOINT`.

```go
err := c.Tx(ctx, func(tx *client.Client) error {
	if _, err := tx.User.Create().Set(query.User.Email.Set("ada@example.com")).Do(ctx); err != nil {
		return err
	}
	// Only the audit insert is rolled back if it fails.
	_ = tx.Tx(ctx, func(inner *client.Client) error {
		_, err := inner.AuditLog.Create().Set(query.AuditLog.Message.Set("created user")).Do(ctx)
		return err
	})
	return nil
})
```

## Error Handling

Every database error returned by the client (query, exec, scan, and
transaction errors) can be passed through a hook with `client.WithErrorHandler`.
The hook's result is wrapped with `%w`, so `errors.Is`/`errors.As` work on the
error the hook returns. The driver error stays reachable only if the hook's
error wraps it (the runtime `DBError` returned by `ClassifyError` does, via
`Unwrap`). Returning `nil` (or a typed nil pointer) keeps the original error. Validation errors such as an
unknown where field are not database errors and do not reach the hook.

```go
import "github.com/arsfy/gcorm/pkg/runtime/dialect"

c := client.New(db,
	client.WithDialect("postgresql"),
	client.WithErrorHandler(func(err error) error {
		if classified := (dialect.PostgreSQL{}).ClassifyError(err); classified != nil {
			return classified
		}
		return err
	}),
)
```

## Regeneration

Regenerate after changing schema files:

```sh
gco validate
gco generate
```

Commit generated code if your project expects consumers to build without
running the generator. Otherwise, add the output directory to your normal build
workflow.

