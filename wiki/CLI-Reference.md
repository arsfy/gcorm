# CLI Reference

The GCORM CLI is `gco`.

```text
gco <command> [flags]
```

Global-style flags accepted by many commands:

```text
--schema <path>    Path to a schema directory or file
--config <path>    Path to gco.config.yaml or gco.config.yml
```

## gco init

Initialize a schema file.

```sh
gco init
gco init --yes --provider postgresql
```

Flags:

- `--provider <postgresql|mysql|sqlite>`
- `--schema-file <path>`
- `--env <name>`
- `--output <dir>`
- `--package <name>`
- `--yes`
- `--force`

Default values:

- Schema file: `schema/schema.gcorm`
- Environment variable: `DATABASE_URL`
- Output directory: `./gen`
- Package: `db`
- Non-interactive provider: `postgresql`

## gco validate

Parse, compile, and validate schema files.

```sh
gco validate
gco validate --schema schema
gco validate --config gco.config.yaml
```

## gco fmt

Format schema files.

```sh
gco fmt
gco fmt --schema schema
```

## gco generate

Generate Go client code from schema files.

```sh
gco generate
gco generate --schema schema
gco generate --config gco.config.yaml
gco generate --dry-run
```

Flags:

- `--schema <path>`
- `--config <path>`
- `--dry-run`

## gco db push

Push schema changes directly to a database.

```sh
gco db push
gco db push --schema schema
gco db push --url "$DATABASE_URL"
gco db push --force
```

Flags:

- `--schema <path>`
- `--config <path>`
- `--url <connection-url>`
- `--force`

Supported providers:

- `postgresql`
- `mysql`
- `sqlite`

See [DB Push](DB-Push.md).

## gco migrate diff

Create migration files from a schema diff.

```sh
gco migrate diff --name init
gco migrate diff --name add_posts --schema schema --dir migrations
```

Flags:

- `--name <name>`
- `--dir <path>`
- `--schema <path>`
- `--config <path>`

Generated files:

- `up.sql`
- `down.sql`
- `manifest.json`

## gco migrate init-sql

Generate full initialization SQL from the current schema without creating a
migration directory.

```sh
gco migrate init-sql --schema schema --output init.sql
gco migrate init-sql --schema schema > init.sql
```

Flags:

- `--schema <path>`
- `--config <path>`
- `--output <path>`, `-o <path>`

## gco migrate dev

Create a development migration using the same diff path as `migrate diff`.

```sh
gco migrate dev --name add_posts
```

Current behavior: this command generates migration files and prints what would
be applied. It does not currently execute SQL against a live database.

## gco migrate deploy

Inspect migration directories for deployment.

```sh
gco migrate deploy --dir migrations
```

Current behavior: this command reads migration directories and manifests. It
does not currently execute SQL against a live database.

## gco migrate resolve

Validate and resolve migration state.

```sh
gco migrate resolve --applied 20260101_120000_init
gco migrate resolve --rolled-back 20260101_120000_init
```

## gco introspect

Detect provider information for an existing database URL.

```sh
gco introspect --url "$DATABASE_URL"
gco introspect --url "$DATABASE_URL" --provider postgresql --output schema
```

Current behavior: live full-schema introspection is not implemented in the CLI
command. `db push` has its own internal database introspection for comparing the
current database schema with the target `.gcorm` schema.

## gco version

Print the local CLI version.

```sh
gco version
```

## gco upgrade

Upgrade the CLI when it was installed by `go install`.

```sh
gco upgrade
```

Manual release binary installs must be updated manually from GitHub Releases.

## gco help

Print command help.

```sh
gco help
gco --help
gco -h
```
