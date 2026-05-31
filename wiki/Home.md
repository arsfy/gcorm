# GCORM Wiki

GCORM is a schema-first ORM toolkit for Go. You describe your data model in
`.gcorm` schema files, generate Go code, and use the generated `client`,
`query`, and `model` packages with `database/sql`.

GCORM currently targets PostgreSQL, MySQL, and SQLite. The CLI can initialize
schemas, validate and format schema files, generate Go clients, create migration
files, and push schema changes directly to a database.

## Recommended Reading Order

1. [Installation](Installation.md)
2. [Quick Start](Quick-Start.md)
3. [Schema Guide](Schema-Guide.md)
4. [Generated Client](Generated-Client.md)
5. [Querying](Querying.md)
6. [DB Push](DB-Push.md)
7. [Embedded DB Push](Embedded-DB-Push.md)
8. [Migrations](Migrations.md)
9. [Security](Security.md)
10. [Performance](Performance.md)
11. [Troubleshooting](Troubleshooting.md)

## Reference Pages

- [Configuration](Configuration.md)
- [CLI Reference](CLI-Reference.md)
- [Release and Upgrade](Release-and-Upgrade.md)

## Project Status

GCORM is early-stage software. Public APIs, generated code shape, and schema
syntax may still change. Review generated SQL before using it against important
databases, especially when dropping tables, dropping columns, changing column
types, or using SQLite table rebuild workflows.

## Core Workflow

```sh
gco init
gco validate
gco fmt
gco generate
gco db push
```

Use migration files when you need a reviewed, auditable SQL history:

```sh
gco migrate diff --name init
```

Use `gco db push` for direct schema synchronization in development and controlled
environments.

For production binaries that embed `.gcorm` files and call `dbpush.Push`, see
[Embedded DB Push](Embedded-DB-Push.md).
