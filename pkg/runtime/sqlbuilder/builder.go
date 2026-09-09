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
	limit      *int
	offset     *int
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
	b.limit = &n
	return b
}

// Offset adds an OFFSET clause.
func (b *Builder) Offset(n int) *Builder {
	b.offset = &n
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
	parts := make([]string, len(b.parts), len(b.parts)+2)
	copy(parts, b.parts)

	if b.limit != nil {
		parts = append(parts, fmt.Sprintf("LIMIT %d", *b.limit))
	} else if b.offset != nil {
		switch b.dialect {
		case "mysql":
			parts = append(parts, "LIMIT 18446744073709551615")
		case "sqlite":
			parts = append(parts, "LIMIT -1")
		}
	}
	if b.offset != nil {
		parts = append(parts, fmt.Sprintf("OFFSET %d", *b.offset))
	}

	return strings.Join(parts, " "), b.args
}

// rewrite replaces parameter "?" tokens with dialect-specific placeholders.
// PostgreSQL overloads "?" as an operator character, so its path first skips
// quoted/commented text and only preserves a question mark as an operator when
// the surrounding significant tokens form an infix expression.
func (b *Builder) rewrite(s string, nArgs int) string {
	if b.dialect != "postgresql" || nArgs == 0 {
		// For mysql/sqlite "?" is already correct; just bump the counter.
		b.paramCount += nArgs
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	replaced := 0
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			n := quotedTokenLen(s[i:], '\'', isPostgresEscapeString(s, i))
			out.WriteString(s[i : i+n])
			i += n
		case s[i] == '"':
			n := quotedTokenLen(s[i:], '"', false)
			out.WriteString(s[i : i+n])
			i += n
		case strings.HasPrefix(s[i:], "--"):
			n := lineCommentLen(s[i:])
			out.WriteString(s[i : i+n])
			i += n
		case strings.HasPrefix(s[i:], "/*"):
			n := blockCommentLen(s[i:])
			out.WriteString(s[i : i+n])
			i += n
		case s[i] == '$':
			if tag := dollarQuoteTag(s[i:]); tag != "" {
				n := dollarQuotedTokenLen(s[i:], tag)
				out.WriteString(s[i : i+n])
				i += n
				continue
			}
			out.WriteByte(s[i])
			i++
		case s[i] == '?' && replaced < nArgs && !isPostgresQuestionOperator(s, i):
			b.paramCount++
			replaced++
			out.WriteString(fmt.Sprintf("$%d", b.paramCount))
			i++
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func quotedTokenLen(s string, quote byte, backslashEscapes bool) int {
	for i := 1; i < len(s); i++ {
		if backslashEscapes && s[i] == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] != quote {
			continue
		}
		if i+1 < len(s) && s[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(s)
}

func isPostgresEscapeString(s string, quotePos int) bool {
	if quotePos == 0 || s[quotePos-1] != 'E' && s[quotePos-1] != 'e' {
		return false
	}
	return quotePos == 1 || !isIdentByte(s[quotePos-2])
}

func lineCommentLen(s string) int {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return idx + 1
	}
	return len(s)
}

func blockCommentLen(s string) int {
	depth := 0
	for i := 0; i < len(s)-1; i++ {
		switch s[i : i+2] {
		case "/*":
			depth++
			i++
		case "*/":
			depth--
			i++
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

func dollarQuoteTag(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '$' {
			return s[:i+1]
		}
		if !isIdentByte(s[i]) {
			return ""
		}
	}
	return ""
}

func dollarQuotedTokenLen(s, tag string) int {
	if idx := strings.Index(s[len(tag):], tag); idx >= 0 {
		return len(tag) + idx + len(tag)
	}
	return len(s)
}

func isPostgresQuestionOperator(s string, i int) bool {
	// Include safe operator prefixes such as @? and #?, plus every valid
	// PostgreSQL operator suffix such as ?#, ?-, and ?-|. Delimiters and
	// comments are handled by the token lookaround below.
	start := i
	for start > 0 && isQuestionOperatorPrefixByte(s[start-1]) {
		start--
	}
	end := i + 1
	for end < len(s) && isPostgresOperatorByte(s[end]) {
		if strings.HasPrefix(s[end:], "--") || strings.HasPrefix(s[end:], "/*") {
			break
		}
		end++
	}

	prev, ok := previousSignificantToken(s, start)
	if !ok || !tokenCanEndValue(s, prev) {
		return false
	}
	next, ok := nextSignificantToken(s, end)
	return ok && tokenCanStartValue(s, next)
}

type sqlTokenKind uint8

const (
	tokenOther sqlTokenKind = iota
	tokenWord
	tokenValue
	tokenOperator
	tokenLeftParen
	tokenRightParen
	tokenLeftBracket
	tokenRightBracket
)

type sqlToken struct {
	kind       sqlTokenKind
	start, end int
}

func previousSignificantToken(s string, before int) (sqlToken, bool) {
	var last sqlToken
	found := false
	for pos := 0; pos < before; {
		tok, ok := nextSignificantToken(s[:before], pos)
		if !ok {
			break
		}
		last = tok
		found = true
		pos = tok.end
	}
	return last, found
}

func nextSignificantToken(s string, from int) (sqlToken, bool) {
	for from < len(s) {
		switch {
		case isSpaceByte(s[from]):
			from++
		case strings.HasPrefix(s[from:], "--"):
			from += lineCommentLen(s[from:])
		case strings.HasPrefix(s[from:], "/*"):
			from += blockCommentLen(s[from:])
		default:
			goto token
		}
	}
	return sqlToken{}, false

token:
	start := from
	switch s[from] {
	case '\'':
		from += quotedTokenLen(s[from:], '\'', isPostgresEscapeString(s, from))
		return sqlToken{kind: tokenValue, start: start, end: from}, true
	case '"':
		from += quotedTokenLen(s[from:], '"', false)
		return sqlToken{kind: tokenValue, start: start, end: from}, true
	case '$':
		if tag := dollarQuoteTag(s[from:]); tag != "" {
			from += dollarQuotedTokenLen(s[from:], tag)
			return sqlToken{kind: tokenValue, start: start, end: from}, true
		}
	case '(':
		return sqlToken{kind: tokenLeftParen, start: start, end: from + 1}, true
	case ')':
		return sqlToken{kind: tokenRightParen, start: start, end: from + 1}, true
	case '[':
		return sqlToken{kind: tokenLeftBracket, start: start, end: from + 1}, true
	case ']':
		return sqlToken{kind: tokenRightBracket, start: start, end: from + 1}, true
	}

	if isIdentByte(s[from]) {
		for from++; from < len(s) && isIdentByte(s[from]); from++ {
		}
		return sqlToken{kind: tokenWord, start: start, end: from}, true
	}
	if isPostgresOperatorByte(s[from]) {
		for from++; from < len(s) && isPostgresOperatorByte(s[from]); from++ {
			if strings.HasPrefix(s[from:], "--") || strings.HasPrefix(s[from:], "/*") {
				break
			}
		}
		return sqlToken{kind: tokenOperator, start: start, end: from}, true
	}
	return sqlToken{kind: tokenOther, start: start, end: from + 1}, true
}

func isOperandPrefixKeyword(word string) bool {
	switch word {
	case "ALL", "AND", "AS", "BETWEEN", "BY", "CASE", "DEFAULT", "DISTINCT", "ELSE", "EXISTS", "FETCH", "FIRST", "FROM", "GROUP", "HAVING", "ILIKE", "IN", "IS", "LIKE", "LIMIT", "NEXT", "NOT", "OFFSET", "ON", "OR", "ORDER", "PARTITION", "RANGE", "RETURNING", "ROWS", "SELECT", "SET", "SOME", "THEN", "USING", "VALUES", "WHEN", "WHERE", "ZONE":
		return true
	default:
		return false
	}
}

func tokenCanEndValue(s string, tok sqlToken) bool {
	switch tok.kind {
	case tokenValue, tokenRightParen, tokenRightBracket:
		return true
	case tokenWord:
		return !isOperandPrefixKeyword(strings.ToUpper(s[tok.start:tok.end]))
	default:
		return false
	}
}

func isQuantifierKeyword(word string) bool {
	switch word {
	case "ALL", "ANY", "SOME":
		return true
	default:
		return false
	}
}

func tokenCanStartValue(s string, tok sqlToken) bool {
	switch tok.kind {
	case tokenValue, tokenLeftParen, tokenLeftBracket:
		return true
	case tokenWord:
		word := strings.ToUpper(s[tok.start:tok.end])
		if isQuantifierKeyword(word) {
			// ALL/SOME/ANY directly after an operator form a quantified
			// comparison such as `data ? ALL (ARRAY['a','b'])`, which is
			// only valid when the quantifier is followed by a parenthesized
			// subquery or array expression.
			after, ok := nextSignificantToken(s, tok.end)
			return ok && after.kind == tokenLeftParen
		}
		return !isOperandPrefixKeyword(word)
	case tokenOperator:
		return s[tok.start:tok.end] == "?"
	default:
		return false
	}
}

func isQuestionOperatorPrefixByte(ch byte) bool {
	switch ch {
	case '~', '!', '@', '#', '%', '^', '&', '|':
		return true
	default:
		return false
	}
}

func isPostgresOperatorByte(ch byte) bool {
	switch ch {
	case '+', '-', '*', '/', '<', '>', '=', '~', '!', '@', '#', '%', '^', '&', '|', '`', '?':
		return true
	default:
		return false
	}
}

func isIdentByte(ch byte) bool {
	return ch == '_' || ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= 0x80
}

func isSpaceByte(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
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
		escaped := strings.ReplaceAll(p, string(q), strings.Repeat(string(q), 2))
		quoted[i] = string(q) + escaped + string(q)
	}
	return strings.Join(quoted, ".")
}
