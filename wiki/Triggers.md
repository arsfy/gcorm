# Database Triggers

Declare a row-level database trigger with `@@trigger` inside the model whose
table fires it. PostgreSQL, MySQL, and SQLite support `Before` and `After`
triggers for `Insert`, `Update`, and `Delete`. Each declaration takes one timing
and one event. Add a separate declaration for each additional event.

## SQLite and MySQL

This trigger records each inserted order in a second table. Use this schema
with a SQLite or MySQL datasource.

```gcorm
model Order {
  id Int @id

  @@map("orders")
  @@trigger(
    name: "audit_order_insert",
    timing: After,
    event: Insert,
    body: "INSERT INTO order_audit (order_id) VALUES (NEW.id);"
  )
}

model OrderAudit {
  orderId Int @id @map("order_id")

  @@map("order_audit")
}
```

GCORM wraps the `body` in a compound SQL block. Supply SQL statements with a
semicolon after each statement. Use `\n` to separate lines, `\"` for a double
quote, and `\\` for a backslash inside the schema string.

## PostgreSQL

Use the same models with a PostgreSQL datasource and provide a complete
PL/pgSQL function body. GCORM creates the associated trigger function in the
active schema and captures the migration session's `search_path` for that
function.

```gcorm
@@trigger(
  name: "audit_order_insert",
  timing: After,
  event: Insert,
  body: "BEGIN\n  INSERT INTO order_audit (order_id) VALUES (NEW.id);\n  RETURN NEW;\nEND;"
)
```

Return `NEW` from a `Before` insert or update to write the row, including any
changes the trigger made to `NEW`. Return `OLD` from a `Before` delete to let the
operation proceed. PostgreSQL ignores the return value of `After` triggers.
[PostgreSQL trigger functions](https://www.postgresql.org/docs/current/plpgsql-trigger.html)

## Naming and SQL

- `name` is the database trigger name. It must be unique within the table on
  PostgreSQL and throughout the database on MySQL and SQLite.
- In `body`, use physical table and column names, including `@@map` and
  `@map` overrides. Use the provider's `NEW` and `OLD` syntax for the selected
  event.
- Treat trigger bodies as trusted SQL. GCORM validates the declaration
  arguments. The database validates the SQL when it compiles or executes the
  trigger.
- For features such as statement-level triggers, `WHEN` clauses, or multi-event
  triggers, use custom SQL migrations.

## Applying Changes

```sh
gco validate --schema schema
gco migrate diff --name add_order_audit --schema schema
gco db push --schema schema
```

Use `gco migrate diff` to generate reviewable `up.sql` and `down.sql` files.
GCORM creates triggers after tables and constraints are ready. A trigger
replacement drops the old definition and creates the new one. Its down
migration restores the previous definition. A down migration cannot undo data
changes the trigger already performed.

MySQL migration scripts use the `mysql` client's `DELIMITER` command for
compound statements. `gco db push` sends each trigger definition to the driver
as one statement. [MySQL trigger syntax](https://dev.mysql.com/doc/refman/8.4/en/trigger-syntax.html)

Repeat `gco db push` to check for changes. If a managed trigger is changed or
removed in the schema, pass `--force` after reviewing the change. MySQL applies
DDL in separate commits, so a failed replacement can leave the old trigger
dropped. Correct the definition and rerun the push to create the missing
trigger.

`gco introspect` writes managed trigger definitions back to `.gcorm`. Existing
triggers created by custom SQL stay outside GCORM's management. A declaration
that reuses an existing unmanaged name produces a database error. Dropping a
table also drops its triggers.

During a SQLite table rebuild, `db push` reads native trigger definitions from
the database and recreates those on surviving tables and views. When applying
generated migration files to SQLite, include any native trigger definitions
needed by your rebuild in the reviewed SQL.
