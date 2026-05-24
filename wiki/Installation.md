# Installation

## Requirements

- Go 1.26 or newer.
- A database supported by GCORM: PostgreSQL, MySQL, or SQLite.
- A `database/sql` driver in your application.

The CLI is named `gco`.

## Install With Go

The recommended install method is:

```sh
go install github.com/arsfy/gcorm/cmd/gco@latest
```

Make sure your Go binary directory is on `PATH`:

```sh
go env GOPATH
```

The installed binary is usually under:

```text
$(go env GOPATH)/bin/gco
```

Verify the install:

```sh
gco version
gco help
```

## Install From Source

From a local checkout:

```sh
go run ./cmd/gco help
go build ./cmd/gco
```

Source builds are useful for development, but normal users should prefer
`go install` or release archives.

## Release Archives

If you download a prebuilt binary from GitHub Releases, place it on your `PATH`
and run:

```sh
gco version
```

Manual release binaries must be updated manually. The `gco upgrade` command is
reserved for binaries installed with:

```sh
go install github.com/arsfy/gcorm/cmd/gco@version
```

## Database Drivers For Your Application

GCORM generates code that uses `database/sql`. Your application must import a
driver for the database you connect to.

PostgreSQL:

```sh
go get github.com/jackc/pgx/v5/stdlib
```

```go
import _ "github.com/jackc/pgx/v5/stdlib"
```

MySQL:

```sh
go get github.com/go-sql-driver/mysql
```

```go
import _ "github.com/go-sql-driver/mysql"
```

SQLite:

```sh
go get modernc.org/sqlite
```

```go
import _ "modernc.org/sqlite"
```

## Connection URL Examples

PostgreSQL:

```text
postgresql://user:password@localhost:5432/app?sslmode=disable
postgres://user:password@localhost:5432/app?sslmode=disable
```

MySQL:

```text
user:password@tcp(localhost:3306)/app?parseTime=true
mysql://user:password@localhost:3306/app?parseTime=true
```

SQLite:

```text
file:./data/app.db?cache=shared&mode=rwc
sqlite://./data/app.db
./data/app.db
```

