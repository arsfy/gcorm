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
- Changed models and fields.

This makes generated migrations easier to inspect in code review.

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

## SQLite Notes

SQLite supports fewer direct `ALTER TABLE` operations than PostgreSQL and MySQL.
Some changes require table rebuilds. Review generated SQLite SQL especially
carefully before applying it to a database that contains important data.

## Migration Or DB Push

Use migrations when you need review, repeatability, and audit history.

Use `db push` when you want direct schema synchronization and can accept the
risks of applying generated SQL immediately. For a CLI-free production flow,
embed `.gcorm` files and call `dbpush.Push` from an explicit migrator job.
