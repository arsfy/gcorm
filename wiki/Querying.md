# Querying

GCORM's generated query builders provide type-safe CRUD operations over
`database/sql`.

## Read Many

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

Multiple predicates are combined with `AND` by default.

Where clauses are validated before any SQL runs: an unknown field, an
unsupported operator, or a malformed `AND`/`OR`/`NOT` value makes the call
return an error (`FindMany`, `Count`, `UpdateMany`, `DeleteMany`, aggregates,
...). This matters for destructive operations: `NOT(<invalid clause>)` fails
instead of turning into a match-all filter. Supported operators are `=`, `!=`,
`<>`, `<`, `<=`, `>`, `>=`, `IN`, `NOT IN`, `IS NULL`, `IS NOT NULL`, and the
`CONTAINS`/`STARTS_WITH`/`ENDS_WITH` helpers. An empty `OR()` matches nothing
and an empty `AND()` matches everything.

## Read First

```go
user, err := c.User.Query().
	Where(query.User.Email.Equals("ada@example.com")).
	First(ctx)
if err != nil {
	return err
}
if user == nil {
	// not found
}
```

## Count

```go
count, err := c.User.Query().
	Where(query.User.Email.Contains("@example.com")).
	Count(ctx)
```

## Pagination

```go
pageSize := 20
page := 3

users, err := c.User.Query().
	OrderBy(query.User.CreatedAt.Desc()).
	Take(pageSize).
	Skip((page - 1) * pageSize).
	Do(ctx)
```

Always use deterministic ordering when paginating.

## Create

```go
user, err := c.User.Create().
	Set(
		query.User.Email.Set("ada@example.com"),
		query.User.Name.Set("Ada"),
	).
	Do(ctx)
```

PostgreSQL can return the inserted row through `RETURNING`. MySQL and SQLite
write paths may return `nil` for the model pointer; query the row again when you
need database-generated values.

Omitted columns use their database default. A `@default(cuid())` field has no
database default; when it is omitted the client generates a collision-resistant
cuid (`Create`, `CreateOne`, `BulkCreate`, `UpsertOne`).

## Update One

```go
user, err := c.User.Update().
	Where(query.User.Email.Equals("ada@example.com")).
	Set(query.User.Name.Set("Ada Lovelace")).
	Do(ctx)
```

`Do`/`UpdateOne` changes at most one row even when the predicate matches more:
PostgreSQL targets one row through
`WHERE <pk> IN (SELECT <pk> ... LIMIT 1 FOR UPDATE)` (`ctid` for tables without
a primary key) and returns it with `RETURNING`; MySQL uses `LIMIT 1`; SQLite
uses `rowid IN (SELECT rowid ... LIMIT 1)`. Use a unique predicate when you
care which row is changed.

## Update Many

```go
count, err := c.User.Update().
	Where(query.User.Email.Contains("@example.com")).
	Set(query.User.Name.Set("Member")).
	DoMany(ctx)
```

## Delete One

```go
deleted, err := c.User.Delete().
	Where(query.User.Email.Equals("ada@example.com")).
	Do(ctx)
```

`Do`/`DeleteOne` deletes at most one row, with the same per-dialect strategy as
`UpdateOne`.

## Upsert

```go
user, err := c.User.UpsertOne(ctx,
	query.User.Email.Equals("ada@example.com"),
	[]query.UserSetClause{query.User.Email.Set("ada@example.com"), query.User.Name.Set("Ada")},
	[]query.UserSetClause{query.User.Name.Set("Ada Lovelace")},
)
```

The where clause is the conflict target. It must be an equality or an `AND` of
equalities over a unique key, for example a compound `@@unique([tenant, email])`:

```go
where := query.Member.AND(query.Member.Tenant.Equals("acme"), query.Member.Email.Equals("ada@example.com"))
```

With an empty update list an existing row is left unchanged. On PostgreSQL the
client then loads and returns the existing row; on MySQL the statement is a
no-op `ON DUPLICATE KEY UPDATE`.

## Delete Many

```go
count, err := c.User.Delete().
	Where(query.User.Email.Contains("@example.com")).
	DoMany(ctx)
```

Be careful with broad predicates. Prefer explicit predicates for destructive
operations.

## Bulk Create

```go
published := true
count, err := c.Post.BulkCreate([]query.PostCreateInput{
	{Title: "First", Published: &published, AuthorId: "u1"},
	{Title: "Second", AuthorId: "u1"},
}).
	BatchSize(500).
	Do(ctx)
```

`CreateInput` fields that have a database default (`autoincrement()`,
`uuid()`, `now()`, literals, `dbgenerated(...)`) are pointers: `nil` means "use
the column DEFAULT", including for optional fields with a default. A
`cuid()` field left empty gets a client-generated id. On PostgreSQL and MySQL
omitted values are sent as `DEFAULT`; SQLite has no `DEFAULT` keyword in
multi-row `VALUES`, so consecutive rows with the same provided columns share a
statement that omits the rest.

Batches are split so that one statement never exceeds the bind-parameter limit
(65535 on PostgreSQL/MySQL, 32766 on SQLite). Without `BatchSize` the client
uses the largest batch that fits; an explicit `BatchSize` is clamped to it.

Conflict handling:

```go
count, err := c.Post.BulkCreate(posts).
	OnConflictDoNothing(query.PostIdColumn).
	BatchSize(1000).
	Do(ctx)
```

Returning selected values on supported dialects:

```go
rows, err := c.Post.BulkCreate(posts).
	Returning(query.PostIdColumn, query.PostTitleColumn).
	DoReturningValues(ctx)
```

## Aggregates

```go
stats, err := c.User.AggregateWhere(ctx,
	[]query.UserWhereClause{query.User.Email.Contains("@example.com")},
	query.UserAggregateOption{Fn: "MIN", Field: query.UserCreatedAtColumn},
	query.UserAggregateOption{Fn: "MAX", Field: query.UserCreatedAtColumn},
)
// stats.Count, stats.Min["created_at"].(time.Time) on PostgreSQL

groups, err := c.Post.GroupByWhere(ctx,
	[]string{query.PostAuthorIdColumn},
	[]query.PostWhereClause{query.Post.Published.Equals(true)},
)
// groups[i].Group["author_id"], groups[i].Count
```

`Aggregate` and `GroupBy` are the same without a where clause. `AVG`/`SUM` are
returned as `*float64`; `MIN`/`MAX` keep the column's value type as reported by
the driver (`time.Time`, `string` for text and PostgreSQL numeric, integers,
...), with `[]byte` converted to `string`.

## LIKE Helpers

String helpers include:

```go
query.User.Email.Contains("example")
query.User.Email.StartsWith("ada")
query.User.Email.EndsWith(".org")
```

GCORM escapes SQL `LIKE` wildcards in user-provided values. Input containing
`%`, `_`, or `\` is treated as literal text by default.

If your application intentionally exposes wildcard search syntax, convert and
validate that syntax in application code instead of passing raw SQL fragments.

## Raw SQL

Use raw SQL for queries that are not covered by generated builders:

```go
posts, err := client.Raw[model.Post](
	ctx,
	c,
	"SELECT id, title, published, author_id FROM posts WHERE author_id = $1",
	"u1",
)
```

Rows and exec helpers are also available:

```go
rows, err := c.RawRows(ctx, "SELECT id, email FROM users WHERE email = $1", email)
result, err := c.RawExec(ctx, "UPDATE users SET name = $1 WHERE id = $2", name, id)
```

Raw SQL query text is trusted code. Pass untrusted values as parameters only.

## Performance Notes

The generated CRUD path builds SQL strings and argument slices directly. It does
not use reflection for normal model CRUD. `client.Raw[T]` may use reflection to
map arbitrary projection structs and is best reserved for custom SQL.

