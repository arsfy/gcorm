# Migrations

GCORM can generate migration files from schema changes. A migration directory
contains SQL files plus a manifest describing the change set.

## Generate A Migration

```sh
gco migrate diff --name init --schema schema
```

This creates a timestamped directory under `migrations/`:

```text
migrations/
  20260101_120000_init/
    up.sql
    down.sql
    manifest.json
```

Use a custom directory:

```sh
gco migrate diff --name add_posts --dir db/migrations
```

## Generate Initialization SQL

Generate a complete SQL file for initializing an empty database from the current
schema:

```sh
gco migrate init-sql --schema schema --output init.sql
```

If `--output` is omitted, SQL is written to stdout:

```sh
gco migrate init-sql --schema schema > init.sql
```

This command does not create a migration directory and does not connect to a
database.

For production binaries that initialize or synchronize a live database directly
from embedded `.gcorm` files, see [Embedded DB Push](Embedded-DB-Push.md).

## Manifest

`manifest.json` records metadata such as:

- Migration ID.
- Description.
- Checksum.
- Creation time.
- Tool version.
- Destructive operations.
- Whether review is required.
- Changed models and fields, with the PostgreSQL schema of each table.

This makes generated migrations easier to inspect in code review.

For trigger creation, replacement, and removal, including MySQL's script
delimiter, see [Database Triggers](Triggers.md).

## Development Command

```sh
gco migrate dev --name add_posts --schema schema
```

Current behavior: `migrate dev` reuses the diff flow and reports what would be
applied. It does not currently connect to a database and execute SQL.

For direct development database synchronization, use:

```sh
gco db push --schema schema
```

## Deploy Command

```sh
gco migrate deploy --dir migrations
```

Current behavior: `migrate deploy` inspects migration directories and manifests.
It does not currently connect to a database and execute SQL.

Apply reviewed SQL using your deployment system until live migration execution
is implemented.

## Resolve Command

```sh
gco migrate resolve --applied 20260101_120000_init
gco migrate resolve --rolled-back 20260101_120000_init
```

Use resolve when you need to mark or validate a migration state in workflows
that track applied migrations.

## Review Checklist

Before applying generated SQL:

- Read `up.sql` and `down.sql`.
- Check whether `manifest.json` lists destructive operations.
- Confirm indexes and constraints match production requirements.
- Plan data backfills separately.
- Test rollback SQL on a disposable database.
- Take a backup before production changes.

## PostgreSQL Notes

### Schemas

A model with `@@schema("audit")` lives in that PostgreSQL schema. Generated SQL
qualifies every reference to its table, including foreign key targets, index
drops, and trigger functions, for example `"audit"."Item"`. Models without
`@@schema` use unqualified names that resolve through `search_path`. Enum types
are never schema-qualified, so they are created in the first schema on
`search_path`.

The up migration creates each schema that no model used before with
`CREATE SCHEMA IF NOT EXISTS`. The down migration does not drop schemas.

Changing a model's `@@schema` while keeping its table name moves the table with
`ALTER TABLE ... SET SCHEMA`. Indexes, constraints, identity sequences, and the
functions of its managed triggers move with it, and the data stays in place.
The change is marked as requiring review. Moving a table to a model without
`@@schema` targets the first schema on `search_path` when the migration runs.

### Defaults

- `@default(dbgenerated("expr"))` writes `expr` unchanged as the column
  default on every database.
- `@default(cuid())` creates no database default. The generated client
  produces the value. Changing between `cuid()` and no default produces no
  migration.
- Enum defaults use the database label: `ADMIN @map("admin")` with
  `@default(ADMIN)` produces `DEFAULT 'admin'`. List defaults are cast to the
  column type, for example `ARRAY['admin']::"Role"[]` and `'{}'::TEXT[]`.

### Column Type Changes

`ALTER COLUMN ... TYPE` includes a `USING` cast. Values convert directly
between text and other types, between numeric types, and between integers and
booleans. Other conversions, including every conversion to or from an enum, go
through text. A scalar becomes a one-element array. Rows that cannot convert
make the migration fail. The column default is dropped before the conversion
and the target default is set afterwards, in both the up and down migration.

`@default(autoincrement())` maps to an identity column. Removing it runs
`DROP IDENTITY IF EXISTS` followed by `DROP DEFAULT`, which also covers `serial`
columns (a `nextval()` default). Adding it to an existing column drops the previous default,
adds the identity, and sets its sequence past the largest existing value.

### Enum Changes

- A new value is added with `ALTER TYPE ... ADD VALUE`, positioned with
  `BEFORE` or `AFTER` to keep the declared order.
- Changing a value's `@map` renames the label with `RENAME VALUE`.
- Changing an enum's `@@map` renames the type with `RENAME TO`.
- Removing a value recreates the type: the old type is renamed, the new type
  is created, every column using it is converted through text, and the old type
  is dropped. Rows holding a removed value make the migration fail. The change
  is listed as destructive. Applications must reconnect afterwards, because
  prepared statements can still refer to the dropped type.

PostgreSQL rejects a new enum value until the transaction that added it
commits. `up.sql` therefore lists the `ADD VALUE` statements, and the renames
they depend on, first, between two comments. Do not run the whole file in a
single transaction (for example `psql -1`): commit those statements first, then
apply the rest. Tools that apply migrations through the API can use
`DDLStep.RequiresCommit` from `DDLGenerator.UpSteps`, which marks exactly
those leading steps.

The down migration reverses these changes. Reverting added values recreates
the type and fails when rows still use them.

### Generated Names

Unnamed indexes, unique constraints, and foreign keys are named
`idx_<table>_<columns>`, `uq_<table>_<columns>`, and `fk_<table>_<columns>`,
joining columns with `_`. On PostgreSQL a name longer than 63 bytes keeps a
prefix and ends with `_` and 8 hex digits of its SHA-1 hash, so it fits the
identifier limit and stays unique; shorter names are unchanged. Drops of
unnamed objects also remove the names earlier versions generated.

MySQL and SQLite keep the full names earlier versions created, so existing
databases keep matching: MySQL foreign keys stay `fk_<table>_<a>,<b>` (MySQL
cannot drop a foreign key with `IF EXISTS`), and SQLite has no length limit.

Adding a column with `@unique` creates exactly one unique constraint.

## SQLite Notes

SQLite supports fewer direct `ALTER TABLE` operations than PostgreSQL and MySQL.
Some changes require a table rebuild. Rebuild scripts use their own transaction
and check foreign keys before committing. Run them on a dedicated connection
with no open transaction and stop on the first error. If using the SQLite CLI,
run `sqlite3 -bail database.db < up.sql`. An application runner should roll back
and close the connection on error. A successful script enables foreign keys.

The generated down script restores the previous schema where the snapshot
supports it. Dropped column values and rows from dropped tables cannot be
recovered from the SQL snapshot. Review required columns without defaults before
running the rollback.

When tables contain native SQLite features that the schema cannot express,
include their definitions in the reviewed migration. This applies to native
constraints and triggers as well as views whose referenced columns change.

## Migration Or DB Push

Use migrations when you need review, repeatability, and audit history.

Use `db push` when you want direct schema synchronization and can accept the
risks of applying generated SQL immediately. For a CLI-free production flow,
embed `.gcorm` files and call `dbpush.Push` from an explicit migrator job.
