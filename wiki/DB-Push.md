# DB Push

`gco db push` compares your `.gcorm` schema with a live database and executes
the generated SQL needed to align the database with the schema.

## Supported Providers

`db push` supports:

- PostgreSQL
- MySQL
- SQLite

The provider is read from the schema datasource:

```gcorm
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
```

## Basic Usage

```sh
gco db push --schema schema
```

Use an explicit URL:

```sh
gco db push --schema schema --url "$DATABASE_URL"
```

Use a config file:

```sh
gco db push --config gco.config.yaml
```

For production binaries that embed `.gcorm` files and call `dbpush.Push`
directly, see [Embedded DB Push](Embedded-DB-Push.md).

## Connection URL Resolution

GCORM resolves the database URL in this order:

1. `--url <connection-url>`
2. `datasource url = env("NAME")`
3. `datasource url = "literal-url"`

If the schema uses `env("DATABASE_URL")`, the environment variable must be set
before running `db push`.

## What It Does

`db push`:

1. Discovers and compiles schema files.
2. Resolves the database URL.
3. Connects to the database.
4. Introspects the current database schema.
5. Computes a diff.
6. Refuses destructive changes unless `--force` is provided.
7. Executes supported SQL statements using the provider's DDL transaction behavior.

If no changes are detected, the command exits without executing SQL.

GCORM compares managed triggers on every push. Updating or removing a trigger
requires `--force`. See [Database Triggers](Triggers.md).

## Destructive Changes

Examples of destructive changes include dropping tables, dropping columns, and
some type changes. Without `--force`, GCORM refuses to apply them:

```sh
gco db push --schema schema
```

To allow destructive changes:

```sh
gco db push --schema schema --force
```

Only use `--force` after reviewing the diff impact and backing up important
data.

## When To Use DB Push

Good fit:

- Local development.
- Test databases.
- Disposable preview databases.
- Small internal tools where direct schema sync is acceptable.
- Explicit production migrator jobs that embed trusted `.gcorm` files and use
  [Embedded DB Push](Embedded-DB-Push.md).

Use migrations instead when:

- You need a reviewed SQL history.
- Production policy requires reviewed SQL files.
- Multiple application versions may run during deploys.
- Data backfills or custom SQL are required.

## Provider Notes

PostgreSQL:

- Unqualified names resolve in the default schema: the datasource `schema`,
  a single `schemas` entry, the first `search_path` entry of the URL other
  than `$user`, or `public`. The push transaction creates the default schema
  when it does not exist (otherwise PostgreSQL would silently fall through to
  `public`) and sets `search_path` to the default schema followed by `public`,
  so extensions installed in `public` stay available.
- `db push` reads every datasource `schemas` entry and every model `@@schema`.
  It reads the default schema too, except when a `schemas` list is configured
  that does not include it and every model has `@@schema`: unrelated tables
  there are then left alone instead of being planned as drops. Tables with the same name in different schemas are
  kept apart, foreign keys are matched by schema and table, and schemas a
  model needs are created. Enum types live in the default schema. See
  [PostgreSQL migration notes](Migrations.md#postgresql-notes).
- Adding enum values runs `ALTER TYPE ... ADD VALUE` and commits it in a
  separate locked transaction before the rest of the push, which plans the
  remaining changes again. A new value can therefore be used, for example as a
  default, in the same push. If the rest fails, the added values stay.
  `DryRun` lists all statements in order.
- Partitioned tables are read as tables; their partitions are ignored.
- Generated columns (`GENERATED ALWAYS AS ... STORED`) are read as
  `dbgenerated("<expr>")` and are never altered. Identity columns
  (`ALWAYS` or `BY DEFAULT`) match `@default(autoincrement())`.
- A column default declared with `dbgenerated()` matches any existing default
  of the column, because PostgreSQL normalizes the expression text. Change
  such an expression with a migration.
- A plain unique index matches a `@@unique` or `@unique` declaration on the
  same columns, so an index created outside GCORM, for example one a foreign
  key depends on, is kept.
- Index names that PostgreSQL truncated to 63 bytes match the declared name.
  `Decimal` without `@db` (`DECIMAL(65,30)`) and declared `@db` types such as
  `@db.Int`, `@db.Time`, `@db.Char(10)`, or `String @db.Uuid` push without
  repeated changes.
- Uses `$1`, `$2`, ... placeholders in generated runtime queries.
- Connection URLs often need `sslmode=disable` for local development.

MySQL:

- Use DSNs with `parseTime=true` for `DateTime` fields.
- Uses `?` placeholders.
- DDL commits statement by statement. A failed push can leave earlier changes applied.

SQLite:

- File URLs and plain file paths are supported.
- During a table rebuild, GCORM preserves child rows and the last assigned
  `AUTOINCREMENT` value. It checks foreign keys before committing if enforcement
  was enabled on the connection.
- The push rolls back when it would break a previously valid view or when a
  managed trigger cannot compile against the resulting schema.

## Limitations

`db push` applies generated SQL. It is not a substitute for hand-reviewed
production migration planning. If generated SQL includes an unsupported pattern,
the command reports an error instead of executing that SQL.
