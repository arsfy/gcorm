# Security

This page defines GCORM's SQL injection boundary and the places where
application code must remain careful.

## Normal Query Helpers Parameterize Values

Values passed through generated query helpers are sent as SQL parameters. They
are not concatenated into SQL text.

Examples:

```go
query.User.Email.Equals(userInput)
query.User.Email.In(values)
query.User.Name.Set(userInput)
query.User.Email.Contains(userInput)
```

The generated runtime builds SQL text with placeholders and passes values in a
separate argument slice.

## LIKE Wildcards Are Escaped

String search helpers escape SQL `LIKE` wildcard characters in user-provided
input:

- `%`
- `_`
- `\`

This means a user searching for `%` or `_` searches for those literal
characters, not arbitrary wildcard matches.

Examples:

```go
query.User.Email.Contains(userInput)
query.User.Email.StartsWith(userInput)
query.User.Email.EndsWith(userInput)
```

If your product intentionally supports wildcard search syntax, parse and
validate that syntax at the application layer. Do not pass user-controlled SQL
patterns as trusted SQL fragments.

## Raw SQL Is Trusted Code

Raw SQL helpers are escape hatches:

```go
rows, err := c.RawRows(ctx, "SELECT id FROM users WHERE email = $1", email)
```

This is safe because `email` is still a parameter.

This is unsafe:

```go
rows, err := c.RawRows(ctx, "SELECT id FROM users WHERE email = '"+email+"'")
```

Never concatenate untrusted input into SQL text. This includes:

- Table names.
- Column names.
- Operators.
- Sort direction.
- Function names.
- Raw `WHERE` fragments.
- Raw `ORDER BY` fragments.

## Dynamic Sorting

Do not accept a user-provided column name and place it in SQL. Map public API
values to generated order helpers:

```go
switch sort {
case "created_at":
	q = q.OrderBy(query.User.CreatedAt.Desc())
case "email":
	q = q.OrderBy(query.User.Email.Asc())
default:
	return fmt.Errorf("unsupported sort")
}
```

## Dynamic Filtering

Build filters from a whitelist:

```go
if email != "" {
	q = q.Where(query.User.Email.Contains(email))
}
if role != "" {
	q = q.Where(query.User.Role.Equals(model.Role(role)))
}
```

Validate enum-like user input before converting it to model enum values.

## DB Push And Migrations

`gco db push` and migration generation read trusted schema files. Do not treat
`.gcorm` files from untrusted users as harmless input. Generated SQL can change
or destroy database objects.

Use separate database credentials for schema management. Application runtime
credentials usually should not have permission to drop tables or alter schemas.

## Operational Recommendations

- Use least-privilege database users.
- Keep raw SQL centralized and reviewed.
- Prefer generated query helpers for user-driven filters.
- Put limits on user-controlled pagination sizes.
- Use context timeouts for database calls.
- Review generated SQL before applying it to production.

