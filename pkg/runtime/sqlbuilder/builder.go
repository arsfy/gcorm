package sqlbuilder

import (
	"fmt"
	"strings"
)

// Builder constructs parameterized SQL queries.
type Builder struct {
	parts      []string
	args       []any
	paramCount int
	dialect    string // "postgresql", "mysql", "sqlite"
}

// New creates a Builder for the given dialect.
func New(dialect string) *Builder {
	return &Builder{dialect: dialect}
}

// Placeholder returns the next placeholder token and advances the counter.
func (b *Builder) Placeholder() string {
	b.paramCount++
	if b.dialect == "postgresql" {
		return fmt.Sprintf("$%d", b.paramCount)
	}
	return "?"
}

// peekPlaceholder returns the placeholder for parameter index n (1-based)
// without advancing the counter.
func peekPlaceholder(dialect string, n int) string {
	if dialect == "postgresql" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// Select starts a SELECT statement.
func (b *Builder) Select(columns ...string) *Builder {
	b.parts = append(b.parts, "SELECT "+strings.Join(columns, ", "))
	return b
}

// From adds a FROM clause.
func (b *Builder) From(table string) *Builder {
	b.parts = append(b.parts, "FROM "+table)
	return b
}

// Where adds a WHERE clause. Occurrences of "?" in condition are rewritten
// to dialect-specific placeholders.
func (b *Builder) Where(condition string, args ...any) *Builder {
	b.parts = append(b.parts, "WHERE "+b.rewrite(condition, len(args)))
	b.args = append(b.args, args...)
	return b
}

// And adds an AND clause.
func (b *Builder) And(condition string, args ...any) *Builder {
	b.parts = append(b.parts, "AND "+b.rewrite(condition, len(args)))
	b.args = append(b.args, args...)
	return b
}

// Or adds an OR clause.
func (b *Builder) Or(condition string, args ...any) *Builder {
	b.parts = append(b.parts, "OR "+b.rewrite(condition, len(args)))
	b.args = append(b.args, args...)
	return b
}

// OrderBy adds an ORDER BY clause.
func (b *Builder) OrderBy(clause string) *Builder {
	b.parts = append(b.parts, "ORDER BY "+clause)
	return b
}

// Limit adds a LIMIT clause.
func (b *Builder) Limit(n int) *Builder {
	b.parts = append(b.parts, fmt.Sprintf("LIMIT %d", n))
	return b
}

// Offset adds an OFFSET clause.
func (b *Builder) Offset(n int) *Builder {
	b.parts = append(b.parts, fmt.Sprintf("OFFSET %d", n))
	return b
}

// Insert starts an INSERT INTO statement.
func (b *Builder) Insert(table string) *Builder {
	b.parts = append(b.parts, "INSERT INTO "+table)
	return b
}

// Columns adds a parenthesized column list.
func (b *Builder) Columns(cols ...string) *Builder {
	b.parts = append(b.parts, "("+strings.Join(cols, ", ")+")")
	return b
}

// Values adds a VALUES clause with placeholders for each value.
func (b *Builder) Values(vals ...any) *Builder {
	placeholders := make([]string, len(vals))
	for i := range vals {
		b.paramCount++
		placeholders[i] = peekPlaceholder(b.dialect, b.paramCount)
	}
	b.parts = append(b.parts, "VALUES ("+strings.Join(placeholders, ", ")+")")
	b.args = append(b.args, vals...)
	return b
}

// Update starts an UPDATE statement.
func (b *Builder) Update(table string) *Builder {
	b.parts = append(b.parts, "UPDATE "+table)
	return b
}

// Set adds a SET assignment.
func (b *Builder) Set(column string, value any) *Builder {
	b.paramCount++
	ph := peekPlaceholder(b.dialect, b.paramCount)

	// If the previous part already starts with "SET ", append to it.
	if len(b.parts) > 0 && strings.HasPrefix(b.parts[len(b.parts)-1], "SET ") {
		b.parts[len(b.parts)-1] += ", " + column + " = " + ph
	} else {
		b.parts = append(b.parts, "SET "+column+" = "+ph)
	}
	b.args = append(b.args, value)
	return b
}

// Delete starts a DELETE FROM statement.
func (b *Builder) Delete(table string) *Builder {
	b.parts = append(b.parts, "DELETE FROM "+table)
	return b
}

// Returning adds a RETURNING clause.
func (b *Builder) Returning(columns ...string) *Builder {
	b.parts = append(b.parts, "RETURNING "+strings.Join(columns, ", "))
	return b
}

// Join adds a JOIN clause. joinType is e.g. "INNER", "LEFT", "RIGHT".
func (b *Builder) Join(joinType, table, condition string) *Builder {
	b.parts = append(b.parts, joinType+" JOIN "+table+" ON "+condition)
	return b
}

// GroupBy adds a GROUP BY clause.
func (b *Builder) GroupBy(columns ...string) *Builder {
	b.parts = append(b.parts, "GROUP BY "+strings.Join(columns, ", "))
	return b
}

// Having adds a HAVING clause.
func (b *Builder) Having(condition string, args ...any) *Builder {
	b.parts = append(b.parts, "HAVING "+b.rewrite(condition, len(args)))
	b.args = append(b.args, args...)
	return b
}

// Build returns the assembled SQL string and collected arguments.
func (b *Builder) Build() (string, []any) {
	return strings.Join(b.parts, " "), b.args
}

// rewrite replaces "?" tokens in s with dialect-specific placeholders.
func (b *Builder) rewrite(s string, nArgs int) string {
	if b.dialect != "postgresql" || nArgs == 0 {
		// For mysql/sqlite "?" is already correct; just bump the counter.
		b.paramCount += nArgs
		return s
	}
	var out strings.Builder
	for _, ch := range s {
		if ch == '?' {
			b.paramCount++
			out.WriteString(fmt.Sprintf("$%d", b.paramCount))
		} else {
			out.WriteRune(ch)
		}
	}
	return out.String()
}

// QuoteIdent quotes identifier parts for the given dialect.
// PostgreSQL/SQLite: "schema"."table"
// MySQL: `schema`.`table`
func QuoteIdent(dialect string, parts ...string) string {
	q := '"'
	if dialect == "mysql" {
		q = '`'
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = string(q) + p + string(q)
	}
	return strings.Join(quoted, ".")
}
