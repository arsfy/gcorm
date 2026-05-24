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

## Update One

```go
user, err := c.User.Update().
	Where(query.User.Email.Equals("ada@example.com")).
	Set(query.User.Name.Set("Ada Lovelace")).
	Do(ctx)
```

Use a unique predicate when you expect one row.

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
count, err := c.Post.BulkCreate([]query.PostCreateInput{
	{Id: "p1", Title: "First", Published: true, AuthorId: "u1"},
	{Id: "p2", Title: "Second", Published: false, AuthorId: "u1"},
}).
	BatchSize(500).
	Do(ctx)
```

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

