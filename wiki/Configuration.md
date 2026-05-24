# Configuration

GCORM can run with only `.gcorm` files, but a config file is useful for larger
projects.

## Config Files

GCORM looks for:

```text
gco.config.yaml
gco.config.yml
```

Discovery order:

1. Explicit `--config <path>`.
2. `gco.config.yaml` or `gco.config.yml` in the current directory.
3. Parent directory search, up to five levels.
4. `GCO_CONFIG` environment variable.
5. Built-in defaults.

## Example

```yaml
schemaRoots:
  - schema
migrationDir: migrations
shadowDatabaseURL: ""
generators:
  client: gco-go
format:
  indentWidth: 2
```

## Fields

`schemaRoots`:

```yaml
schemaRoots:
  - schema
  - modules/billing/schema
```

Directories or files searched for `.gcorm` files. Relative paths are resolved
from the current working directory.

`migrationDir`:

```yaml
migrationDir: migrations
```

Directory used by migration commands. Defaults to `migrations`.

`shadowDatabaseURL`:

```yaml
shadowDatabaseURL: postgresql://localhost/shadow?sslmode=disable
```

Reserved for workflows that need a separate comparison database.

`generators`:

```yaml
generators:
  client: gco-go
```

Named generator settings. Current Go generation is primarily driven by the
`generator` block in the schema.

`format.indentWidth`:

```yaml
format:
  indentWidth: 2
```

Indent width used by schema formatting.

## Schema Discovery Without Config

When `schemaRoots` is not set, GCORM searches:

1. `schema/`
2. `prisma/`
3. The current directory

If both `schema/` and `prisma/` exist, GCORM asks you to pass `--schema` or set
`schemaRoots` to avoid ambiguity.

## Environment Variables

`GCO_CONFIG` points to a config file:

```sh
export GCO_CONFIG=/path/to/gco.config.yaml
```

Datasource URLs can also come from schema `env()` calls:

```gcorm
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
```

