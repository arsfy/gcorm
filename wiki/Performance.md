# Performance

GCORM's runtime path is designed to stay close to direct `database/sql` usage.
The generated CRUD code builds SQL strings, appends arguments, and scans rows
without reflection in normal model operations.

## Runtime Query Building

Generated query builders create:

- SQL text with dialect-specific placeholders.
- A `[]any` argument slice.
- Direct row scanning into generated model structs.

Normal CRUD operations do not use reflection for model scanning.

## Raw SQL Performance

`client.Raw[T]` supports custom projection structs and may use reflection to map
columns to fields. It is useful for complex SQL, reporting queries, and special
cases, but generated CRUD paths are better for hot code paths.

## Bulk Inserts

Use `BulkCreate` for large insert workloads:

```go
count, err := c.Event.BulkCreate(events).
	BatchSize(1000).
	Do(ctx)
```

Batch size should be tuned for your database, row width, network latency, and
driver limits. Common starting points are 500 to 1000 rows per batch.

## Pagination

Offset pagination is easy:

```go
users, err := c.User.Query().
	OrderBy(query.User.CreatedAt.Desc()).
	Take(50).
	Skip(1000).
	Do(ctx)
```

For very deep pagination, prefer keyset pagination patterns where possible:

```go
users, err := c.User.Query().
	Where(query.User.CreatedAt.Lt(cursorTime)).
	OrderBy(query.User.CreatedAt.Desc()).
	Take(50).
	Do(ctx)
```

## Indexes

Add indexes in schema for common filters and ordering:

```gcorm
model Post {
  id        String   @id @default(uuid())
  authorId  String
  createdAt DateTime @default(now())

  @@index([authorId])
  @@index([createdAt])
}
```

Database performance still depends on query plans, index choice, and data
distribution. Use your database's `EXPLAIN` tools for slow queries.

## Benchmarks

Run runtime SQL builder benchmarks:

```sh
go test ./pkg/runtime/sqlbuilder -bench=. -benchmem -run=^$
```

Run generated-client runtime tests and benchmarks:

```sh
GCO_RUN_GENERATED_BENCH=1 go test -v ./pkg/codegen/golang \
  -run TestGeneratedClientBulkCreateAndRawRuntime -count=1
```

Run schema and code generation benchmarks:

```sh
go test -bench=. -benchmem ./pkg/schema/compiler/... ./pkg/codegen/golang/...
```

## Interpreting Results

Schema parsing and generation are offline developer operations. Runtime
performance matters most in generated query execution, database round trips,
indexes, and result scanning.

For application tuning:

- Measure with production-like data volume.
- Benchmark through the real driver and database.
- Watch allocation counts in hot loops.
- Prefer bulk APIs for batch writes.
- Keep transaction scopes short.

