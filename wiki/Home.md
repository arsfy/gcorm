# GCORM Wiki

GCORM is a schema-first ORM toolkit for Go. You describe your data model in
`.gcorm` schema files, generate Go code, and use the generated `client`,
`query`, and `model` packages with `database/sql`.

GCORM currently targets PostgreSQL, MySQL, and SQLite. The CLI can initialize
schemas, validate and format schema files, generate Go clients, create migration
files, and push schema changes directly to a database.

## Recommended Reading Order

1. [Installation](Installation)
2. [Quick Start](Quick-Start)
3. [Schema Guide](Schema-Guide)
4. [Generated Client](Generated-Client)
5. [Querying](Querying)
6. [DB Push](DB-Push)
7. [Embedded DB Push](Embedded-DB-Push)
8. [Migrations](Migrations)
9. [Security](Security)
10. [Performance](Performance)
11. [Troubleshooting](Troubleshooting)

## Reference Pages

- [Configuration](Configuration)
- [CLI Reference](CLI-Reference)
- [Database Triggers](Triggers)
- [Release and Upgrade](Release-and-Upgrade)

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
[Embedded DB Push](Embedded-DB-Push).
