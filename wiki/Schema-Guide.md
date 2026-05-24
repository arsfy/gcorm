# Schema Guide

GCORM schema files use the `.gcorm` extension. They are the source of truth for
generated Go models, query builders, and database DDL.

## File Structure

A schema can contain:

```text
datasource  Database provider and URL
generator   Code generation settings
model       Database table model
enum        Enumeration type
```

GCORM supports multiple `.gcorm` files. Files are discovered from the configured
schema roots and merged in deterministic path order.

## Datasource

```gcorm
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
  schema   = "public"
}
```

Fields:

- `provider`: required. One of `postgresql`, `mysql`, or `sqlite`.
- `url`: required for commands that connect to a database. Use a literal string
  or `env("NAME")`.
- `schema`: optional PostgreSQL schema or namespace.

Examples:

```gcorm
url = env("DATABASE_URL")
url = "postgresql://localhost/app?sslmode=disable"
```

Avoid committing production credentials in schema files. Prefer `env()`.

## Generator

```gcorm
generator client {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}
```

Fields:

- `provider`: use `gco-go`.
- `output`: output directory for generated Go code.
- `package`: generated package base name.

## Models

```gcorm
model Post {
  id        String   @id @default(uuid())
  title     String
  content   String?
  published Boolean  @default(false)
  authorId  String
  author    User     @relation(fields: [authorId], references: [id])
  createdAt DateTime @default(now())
  updatedAt DateTime @updatedAt

  @@index([authorId])
  @@map("posts")
}
```

A model maps to a table. A scalar field maps to a column. Relation fields
describe relationships and are not always stored as standalone columns.

## Scalar Types

| Schema type | Go type | Common SQL mapping |
| --- | --- | --- |
| `String` | `string` | `TEXT` |
| `Int` | `int32` | `INTEGER` |
| `SmallInt` | `int16` | `SMALLINT` / `INTEGER` |
| `BigInt` | `int64` | `BIGINT` |
| `Float` | `float64` | `DOUBLE` / `REAL` |
| `Decimal` | `float64` | `DECIMAL` |
| `Boolean` | `bool` | `BOOLEAN` / `TINYINT(1)` |
| `DateTime` | `time.Time` | `TIMESTAMP` / `DATETIME` |
| `Bytes` | `[]byte` | `BYTEA` / `BLOB` |
| `Json` | `json.RawMessage` | `JSONB` / `JSON` / `TEXT` |
| `UUID` | `string` | `UUID` / `VARCHAR(36)` / `TEXT` |

## Type Modifiers

```gcorm
name  String?
posts Post[]
```

- `?` marks a nullable field.
- `[]` marks a list, usually used for relation fields.

## Field Attributes

```gcorm
id        String   @id @default(uuid())
email     String   @unique
createdAt DateTime @default(now())
updatedAt DateTime @updatedAt
name      String   @map("display_name")
author    User     @relation(fields: [authorId], references: [id])
```

Common attributes:

- `@id`: primary key field.
- `@unique`: unique constraint.
- `@default(value)`: database or application default.
- `@updatedAt`: update timestamp managed by generated code.
- `@map("column_name")`: map field to a database column name.
- `@relation(...)`: relationship definition.
- `@db.*`: native type annotation, such as `@db.VarChar(255)` or `@db.Text`.

Supported default examples:

```gcorm
@default(uuid())
@default(cuid())
@default(now())
@default(autoincrement())
@default(0)
@default(0.0)
@default("")
@default(false)
@default(USER)
```

## Model Attributes

```gcorm
@@id([tenantId, id])
@@unique([email])
@@unique([tenantId, slug])
@@index([createdAt])
@@map("users")
@@schema("public")
```

Common model attributes:

- `@@id([...])`: composite primary key.
- `@@unique([...])`: composite unique constraint.
- `@@index([...])`: index.
- `@@map("table_name")`: map model to a database table name.
- `@@schema("name")`: PostgreSQL schema name.

## Advanced Indexes

`@@index` supports partial indexes and per-column index options:

```gcorm
model Announcement {
  id          Int
  status      Int
  publishedAt DateTime?

  @@index(
    [status, publishedAt],
    name: "idx_announcements_user",
    where: "status = 1 AND published_at IS NOT NULL",
    sort: [Desc, Asc],
    nulls: [Last, Last],
    opclass: ["int8_ops", "timestamptz_ops"],
    collate: ["pg_catalog.default", "pg_catalog.default"]
  )
}
```

Index arguments:

- `name`: explicit database index name.
- `where`: raw SQL predicate for a partial or filtered index.
- `sort` or `order`: `Asc` or `Desc`, either one value or one value per field.
- `nulls`: `First` or `Last`, either one value or one value per field.
- `opclass`, `opclasses`, or `ops`: PostgreSQL operator class names.
- `collate` or `collation`: collation names, such as `pg_catalog.default`.

Single-column example:

```gcorm
@@index([clickhouseRecordedAt], name: "idx_ihce_retry", where: "clickhouse_recorded_at IS NULL")
```

`where` is database SQL, not a GCORM expression. Use database column names in
the predicate. Partial indexes are supported by PostgreSQL and SQLite. MySQL
does not support partial indexes, so GCORM will not emit a valid MySQL partial
index for schemas that use `where`.

## Relations

One-to-many:

```gcorm
model User {
  id    String @id @default(uuid())
  posts Post[]
}

model Post {
  id       String @id @default(uuid())
  authorId String
  author   User   @relation(fields: [authorId], references: [id])
}
```

One-to-one:

```gcorm
model User {
  id      String   @id @default(uuid())
  profile Profile?
}

model Profile {
  id     String @id @default(uuid())
  userId String @unique
  user   User   @relation(fields: [userId], references: [id])
}
```

Many-to-many is represented by list relations on both sides:

```gcorm
model Post {
  id   String @id @default(uuid())
  tags Tag[]
}

model Tag {
  id    String @id @default(uuid())
  posts Post[]
}
```

For production systems, an explicit join model is often easier to migrate,
index, and extend:

```gcorm
model PostTag {
  postId String
  tagId  String

  @@id([postId, tagId])
  @@index([tagId])
}
```

## Enums

```gcorm
enum Role {
  USER
  ADMIN
  MODERATOR
}
```

Enums generate Go string types and constants. They can be used as field types:

```gcorm
model User {
  id   String @id @default(uuid())
  role Role   @default(USER)
}
```

## Naming And Mapping

Use clear model and field names for generated Go APIs. Use `@map` and `@@map`
when the database name differs from the Go-facing schema name.

```gcorm
model User {
  id        String @id @default(uuid())
  createdAt DateTime @map("created_at")

  @@map("users")
}
```
