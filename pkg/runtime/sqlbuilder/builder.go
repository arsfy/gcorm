package sqlbuilder

import (
	"errors"
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
	err        error
}

// Err reports the first placeholder/argument mismatch recorded while
// building, e.g. Where("a = ? AND b = ?", 1). Placeholder numbering always
// stays aligned with the collected args, so later clauses are numbered
// correctly even after a mismatch; callers must still check Err before
// executing the statement.
func (b *Builder) Err() error {
	return b.err
}

func (b *Builder) recordPlaceholderMismatch(condition string, placeholders, nArgs int) {
	// A negative count means the condition uses numbered or named parameters
	// (?1, :name, @name, $name) whose arguments cannot be counted reliably.
	if b.err != nil || placeholders < 0 || placeholders == nArgs {
		return
	}
	b.err = fmt.Errorf("%w: %q has %d placeholder(s) but %d argument(s)", ErrPlaceholderMismatch, condition, placeholders, nArgs)
}

// ErrPlaceholderMismatch is wrapped by Builder.Err when a condition's "?"
// placeholders do not match the number of supplied arguments.
var ErrPlaceholderMismatch = errors.New("sqlbuilder: placeholder/argument count mismatch")

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
// the surrounding significant tokens form an infix expression. A mismatch
// between detected placeholders and nArgs is recorded for Err; the parameter
// counter always advances by nArgs so numbering stays aligned with args.
func (b *Builder) rewrite(s string, nArgs int) string {
	if b.dialect != "postgresql" {
		// For mysql/sqlite "?" is already correct; just bump the counter.
		b.recordPlaceholderMismatch(s, countQuestionPlaceholders(s, b.dialect), nArgs)
		b.paramCount += nArgs
		return s
	}
	// Even without arguments the condition is scanned: a "?" that is not a
	// PostgreSQL operator is a placeholder with no argument (a mismatch); the
	// text itself is returned unchanged in that case.
	var out strings.Builder
	out.Grow(len(s))
	base := b.paramCount
	found := 0
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
		case isIdentByte(s[i]):
			// Identifiers may contain "$" after their first character
			// (a$b$), which must not open a dollar-quoted string.
			n := identifierLen(s[i:])
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
		case s[i] == '?' && !isPostgresQuestionOperator(s, i):
			found++
			if found <= nArgs {
				out.WriteString(fmt.Sprintf("$%d", base+found))
			} else {
				out.WriteByte('?')
			}
			i++
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	b.recordPlaceholderMismatch(s, found, nArgs)
	b.paramCount = base + nArgs
	return out.String()
}

// countQuestionPlaceholders counts "?" outside string literals, quoted
// identifiers and comments for MySQL/SQLite.
func countQuestionPlaceholders(s, dialect string) int {
	count := 0
	mysql := dialect == "mysql"
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'' || s[i] == '"':
			i += quotedTokenLen(s[i:], s[i], mysql)
		case s[i] == '`':
			i += quotedTokenLen(s[i:], '`', false)
		case !mysql && s[i] == '[':
			if end := strings.IndexByte(s[i:], ']'); end >= 0 {
				i += end + 1
			} else {
				i = len(s)
			}
		case strings.HasPrefix(s[i:], "--"), mysql && s[i] == '#':
			i += lineCommentLen(s[i:])
		case strings.HasPrefix(s[i:], "/*"):
			if end := strings.Index(s[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(s)
			}
		case s[i] == '?':
			if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
				return -1 // numbered parameter (?NNN) may be reused
			}
			count++
			i++
		case !mysql && (s[i] == ':' || s[i] == '@' || s[i] == '$') && i+1 < len(s) && isNamedParamStart(s[i+1]):
			return -1 // SQLite named parameter
		default:
			i++
		}
	}
	return count
}

// isNamedParamStart reports whether ch can start a SQLite named parameter
// name (after ':', '@' or '$').
func isNamedParamStart(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// identifierLen returns the length of the identifier or keyword at the start
// of s, including "$" characters after the first byte.
func identifierLen(s string) int {
	n := 1
	for n < len(s) && (isIdentByte(s[n]) || s[n] == '$') {
		n++
	}
	return n
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

// dollarQuoteTag returns the opening $tag$ (or $$) at the start of s. A tag
// follows identifier rules without "$" and cannot start with a digit, so
// positional parameters such as $1 never open a dollar quote.
func dollarQuoteTag(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	if s[1] >= '0' && s[1] <= '9' {
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
	if !ok || !tokenCanEndValue(s, prev, 0) {
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
		if from+1 < len(s) && s[from+1] >= '0' && s[from+1] <= '9' {
			// Positional parameter such as $1.
			for from++; from < len(s) && s[from] >= '0' && s[from] <= '9'; from++ {
			}
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
		from += identifierLen(s[from:])
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

// isOperandPrefixKeyword reports reserved (or column-name) keywords that are
// always followed by an operand. Non-reserved keywords that are valid column
// names (ROWS, RANGE, FIRST, NEXT, ZONE, PARTITION...) are not listed here;
// see isContextualOperandPrefixKeyword.
func isOperandPrefixKeyword(word string) bool {
	switch word {
	case "ALL", "AND", "AS", "BETWEEN", "BY", "CASE", "DEFAULT", "DISTINCT", "ELSE", "EXISTS", "FETCH", "FROM", "GROUP", "HAVING", "ILIKE", "IN", "IS", "LIKE", "LIMIT", "NOT", "OFFSET", "ON", "OR", "ORDER", "RETURNING", "SELECT", "SET", "SOME", "THEN", "USING", "VALUES", "WHEN", "WHERE":
		return true
	default:
		return false
	}
}

// isContextualOperandPrefixKeyword reports whether a non-reserved keyword at
// tok acts as a keyword that precedes an operand, judged by the token before
// it: FETCH FIRST|NEXT ?, AT TIME ZONE ?, and window frames such as
// "ORDER BY x ROWS ? PRECEDING" or "OVER (RANGE ? PRECEDING)". Otherwise the
// word is an ordinary column name, e.g. `rows ? 'k'`.
func isContextualOperandPrefixKeyword(s string, tok sqlToken, word string, depth int) bool {
	switch word {
	case "FIRST", "NEXT", "ZONE", "ROWS", "RANGE", "GROUPS":
	default:
		return false
	}
	prev, ok := previousSignificantToken(s, tok.start)
	if !ok {
		return false
	}
	prevWord := ""
	if prev.kind == tokenWord {
		prevWord = strings.ToUpper(s[prev.start:prev.end])
	}
	switch word {
	case "FIRST", "NEXT":
		return prevWord == "FETCH"
	case "ZONE":
		return prevWord == "TIME"
	default: // ROWS, RANGE, GROUPS
		if prev.kind == tokenLeftParen {
			before, ok := previousSignificantToken(s, prev.start)
			return ok && before.kind == tokenWord && strings.EqualFold(s[before.start:before.end], "OVER")
		}
		// A frame clause directly follows a value (ORDER BY expression or
		// window name); a column name never does.
		return depth < 4 && tokenCanEndValue(s, prev, depth+1)
	}
}

func tokenCanEndValue(s string, tok sqlToken, depth int) bool {
	switch tok.kind {
	case tokenValue, tokenRightParen, tokenRightBracket:
		return true
	case tokenWord:
		word := strings.ToUpper(s[tok.start:tok.end])
		return !isOperandPrefixKeyword(word) && !isContextualOperandPrefixKeyword(s, tok, word, depth)
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
