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
7. Executes supported SQL statements in a transaction.

If no changes are detected, the command exits without executing SQL.

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

Use migrations instead when:

- You need a reviewed SQL history.
- You deploy to production.
- Multiple application versions may run during deploys.
- Data backfills or custom SQL are required.

## Provider Notes

PostgreSQL:

- Supports schema namespaces.
- Uses `$1`, `$2`, ... placeholders in generated runtime queries.
- Connection URLs often need `sslmode=disable` for local development.

MySQL:

- Use DSNs with `parseTime=true` for `DateTime` fields.
- Uses `?` placeholders.

SQLite:

- File URLs and plain file paths are supported.
- Some schema changes require table rebuild patterns.
- Transaction behavior depends on the SQLite driver and connection settings.

## Limitations

`db push` applies generated SQL. It is not a substitute for hand-reviewed
production migration planning. If generated SQL includes an unsupported pattern,
the command reports an error instead of executing that SQL.

