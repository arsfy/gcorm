package migrate

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// PostgreSQL deparses index expressions and predicates into a canonical form
// that differs textually from what users write in a schema. The normalizer
// rewrites both sides into a shared shape so that db push does not drop and
// recreate indexes whose definitions are unchanged.
//
// Every rewrite must be sound: two definitions PostgreSQL stores differently
// must never normalize to the same text, or a real change would be skipped
// (db push only probes the database when the texts differ). A rewrite that
// cannot be proven safe from the text alone is left out; such definitions
// fall back to the db push probe. postgres_index_normalize_oracle_test.go
// checks both properties against a live PostgreSQL.

// indexNormalizer holds the table context of an index.
type indexNormalizer struct {
	enumTypes map[string]struct{}
	// columnTypes maps column names to canonical PostgreSQL type names. Casts
	// on a column are only removed when the type proves them redundant.
	columnTypes map[string]string
}

func newIndexNormalizer(schema *ir.Schema, model *ir.Model) indexNormalizer {
	n := indexNormalizer{enumTypes: postgresEnumTypeNames(schema)}
	if model == nil {
		return n
	}
	gen := DDLGenerator{Dialect: "postgresql", Schema: schema}
	n.columnTypes = make(map[string]string)
	for _, f := range model.ScalarFields() {
		n.columnTypes[columnName(f)] = canonicalPostgresTypeName(gen.columnSQLType(f))
	}
	return n
}

// normalizeIndexPredicate normalizes a WHERE clause without table context.
func normalizeIndexPredicate(where string) string {
	return indexNormalizer{}.predicate(where)
}

// normalizeIndexExpression normalizes an index expression without table
// context.
func normalizeIndexExpression(expression string) string {
	return indexNormalizer{}.expression(expression)
}

func normalizeIndexExpressionWithEnums(expression string, enumTypes map[string]struct{}) string {
	return indexNormalizer{enumTypes: enumTypes}.expression(expression)
}

func (n indexNormalizer) predicate(where string) string {
	// A string literal in a predicate always compares against a typed operand,
	// so its cast follows from the operand and carries no information.
	return n.normalize(where, true)
}

func (n indexNormalizer) expression(expression string) string {
	return n.normalize(expression, false)
}

// normalize runs the rewrite pipeline until its output is stable, since one
// rewrite can expose work for an earlier one.
func (n indexNormalizer) normalize(s string, allLiteralCasts bool) string {
	for range 8 {
		next := n.normalizeOnce(s, allLiteralCasts)
		if next == s {
			break
		}
		s = next
	}
	return s
}

func (n indexNormalizer) normalizeOnce(s string, allLiteralCasts bool) string {
	s = collapseSQLWhitespace(s)
	s = unwrapParens(s)
	s = uppercaseSQLKeywords(s)
	s = unquoteSimpleIdentifiers(s)
	s = rewriteCastSyntax(s)
	s = rewritePostgresOperators(s)
	s = spaceOperators(s)
	s = canonicalizePostgresCastTypes(s)
	s = stripPostgresImplicitCaseElseNull(s)
	s = n.stripCasts(s, allLiteralCasts)
	s = rewriteSingleElementIn(s)
	s = rewriteIsNotDistinctFrom(s)
	s = rewritePostgresBetween(s)
	s = rewritePostgresArrayMembership(s)
	for range 32 {
		next := unwrapParens(s)
		next = stripPostgresCaseWhenParens(next)
		next = stripSimplePredicateParens(next)
		next = stripRedundantIdentifierParens(next)
		next = stripRedundantOperatorParens(next)
		if next == s {
			break
		}
		s = next
	}
	return collapseSQLWhitespace(normalizeSQLCommas(s))
}

func unwrapParens(s string) string {
	s = strings.TrimSpace(s)
	for hasWrappingParens(s) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// collapseSQLWhitespace collapses whitespace runs to one space outside string
// literals and quoted identifiers, whose contents are significant.
func collapseSQLWhitespace(s string) string {
	var b strings.Builder
	space := false
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			space = true
			i++
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// stripCasts removes casts that cannot change the meaning of the definition:
//
//   - On a constant without a type modifier: PostgreSQL resolves the
//     constant's type from the operand it is compared or combined with and
//     prints the result as 'x'::uuid or (0)::bigint. String constants are
//     only stripped in predicates (allLiteralCasts) or when the cast is to a
//     text-like or enum type; numeric strings with a numeric cast ('-1'::integer)
//     become plain numbers. Casts with a modifier (varchar(10), numeric(10,2))
//     can truncate or round and are kept.
//   - On a column: only when the cast is a no-op (the column already has that
//     type) or the varchar-to-text coercion PostgreSQL inserts by itself.
//     Other column casts select different operators (bpchar vs text compare)
//     and are kept.
func (n indexNormalizer) stripCasts(s string, allLiteralCasts bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			end := postgresStringLiteralEnd(s, i)
			castEnd := skipPostgresCast(s, end)
			if castEnd > end && end-1 > i && s[end-1] == '\'' {
				castType := s[end+2 : castEnd]
				if !hasTypeModifier(castType) || isNoopVarcharLiteralCast(s[i+1:end-1], castType) {
					if inner := s[i+1 : end-1]; isPostgresNumericType(castType) && isNumericLiteral(inner) {
						b.WriteString(inner)
						i = castEnd
						continue
					}
					if allLiteralCasts || isPostgresTextLikeCast(castType) || isPostgresEnumCast(castType, n.enumTypes) {
						b.WriteString(s[i:end])
						i = castEnd
						continue
					}
				}
			}
			b.WriteString(s[i:end])
			i = end
			continue
		case s[i] == '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = n.skipRedundantColumnCast(s, s[i:end], end)
			continue
		case s[i] == '(':
			closeIdx := matchingParenIndex(s, i)
			if castEnd := skipPostgresCast(s, max(closeIdx+1, i+1)); closeIdx > i && castEnd > closeIdx+1 {
				castType := s[closeIdx+3 : castEnd]
				inner := strings.TrimSpace(s[i+1 : closeIdx])
				if isNumericLiteral(inner) && !hasTypeModifier(castType) {
					b.WriteString(inner)
					i = castEnd
					continue
				}
				if isSimplePostgresIdentifier(inner) && n.isRedundantColumnCast(inner, castType) {
					b.WriteString(inner)
					i = castEnd
					continue
				}
				// (x::varchar(3))::text: PostgreSQL adds the text coercion itself.
				if canonicalPostgresTypeName(castType) == "text" && strings.HasPrefix(canonicalPostgresTypeName(trailingCastType(inner)), "character varying") {
					b.WriteString(n.stripCasts(inner, allLiteralCasts))
					i = castEnd
					continue
				}
			}
		case isDigit(s[i]) && (i == 0 || !isSQLIdentifierByte(s[i-1]) && s[i-1] != '.'):
			if numEnd, ok := numericLiteralEnd(s, i); ok {
				b.WriteString(s[i:numEnd])
				i = numEnd
				if castEnd := skipPostgresCast(s, numEnd); castEnd > numEnd && !hasTypeModifier(s[numEnd+2:castEnd]) {
					i = castEnd
				}
				continue
			}
		case isSQLIdentifierStart(s[i]) && (i == 0 || !isSQLIdentifierByte(s[i-1]) && s[i-1] != '.' && s[i-1] != ':'):
			end := postgresQualifiedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = n.skipRedundantColumnCast(s, s[i:end], end)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// skipRedundantColumnCast returns the index after a redundant cast of column
// at s[end:], or end when there is none.
func (n indexNormalizer) skipRedundantColumnCast(s, column string, end int) int {
	castEnd := skipPostgresCast(s, end)
	if castEnd > end && n.isRedundantColumnCast(column, s[end+2:castEnd]) {
		return castEnd
	}
	return end
}

func (n indexNormalizer) isRedundantColumnCast(column, castType string) bool {
	if dot := strings.LastIndexByte(column, '.'); dot >= 0 && !strings.HasSuffix(column, `"`) {
		column = column[dot+1:]
	}
	column = unquotePostgresIdentifier(column)
	columnType, ok := n.columnTypes[column]
	if !ok {
		return false
	}
	castType = canonicalPostgresTypeName(castType)
	if castType == columnType {
		return true
	}
	// varchar has no operators of its own; PostgreSQL coerces it to text.
	return castType == "text" && strings.HasPrefix(columnType, "character varying") && !strings.HasSuffix(columnType, "]")
}

// isNoopVarcharLiteralCast reports whether casting the literal to
// varchar(n) leaves it unchanged, i.e. it is at most n characters long.
func isNoopVarcharLiteralCast(literal, castType string) bool {
	canonical := canonicalPostgresTypeName(castType)
	rest, ok := strings.CutPrefix(canonical, "character varying(")
	if !ok || !strings.HasSuffix(rest, ")") {
		return false
	}
	limit, err := strconv.Atoi(strings.TrimSuffix(rest, ")"))
	return err == nil && utf8.RuneCountInString(strings.ReplaceAll(literal, "''", "'")) <= limit
}

// trailingCastType returns the type of a top-level cast that ends s, as in
// x::varchar(3), or "".
func trailingCastType(s string) string {
	depth, last := 0, -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ':':
			if depth == 0 && strings.HasPrefix(s[i:], "::") {
				last = i
				i++
			}
		}
	}
	if last < 0 || skipPostgresCast(s, last) != len(s) {
		return ""
	}
	return s[last+2:]
}

// spaceOperators puts single spaces around comparison operators and after a
// unary minus applied to a column or group, matching PostgreSQL's output:
// n>0 becomes n > 0 and -n becomes - n. Other operator runs are left alone;
// a negative number such as -1 keeps its sign attached.
func spaceOperators(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if !isOperatorChar(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := i
		for end < len(s) && isOperatorChar(s[end]) {
			end++
		}
		run := s[i:end]
		switch {
		case run == "<" || run == ">" || run == "=" || run == "<=" || run == ">=" || run == "<>":
			b.WriteString(" " + run + " ")
		case run == "-" && isUnaryPosition(s, i) && end < len(s) && (isSQLIdentifierStart(s[end]) || s[end] == '"' || s[end] == '('):
			b.WriteString("- ")
		default:
			b.WriteString(run)
		}
		i = end
	}
	return collapseSQLWhitespace(b.String())
}

func isOperatorChar(ch byte) bool {
	return strings.IndexByte("+-*/<>=~!@#%^&|`?", ch) >= 0
}

// isUnaryPosition reports whether an operator at i has no left operand.
func isUnaryPosition(s string, i int) bool {
	j := i - 1
	for j >= 0 && s[j] == ' ' {
		j--
	}
	if j < 0 || s[j] == '(' || s[j] == ',' || isOperatorChar(s[j]) {
		return true
	}
	if !isSQLIdentifierByte(s[j]) {
		return false
	}
	end := j + 1
	for j >= 0 && isSQLIdentifierByte(s[j]) {
		j--
	}
	switch strings.ToUpper(s[j+1 : end]) {
	case "AND", "OR", "NOT", "WHEN", "THEN", "ELSE", "CASE", "IS", "IN", "BETWEEN":
		return true
	}
	return false
}

func hasTypeModifier(castType string) bool {
	return strings.ContainsRune(castType, '(')
}

func isSQLIdentifierStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

// postgresQualifiedIdentifierEnd returns the end of a possibly qualified
// identifier (a, t.a, s."T".a) starting at i.
func postgresQualifiedIdentifierEnd(s string, i int) int {
	for {
		next := postgresIdentifierEnd(s, i)
		if next == i {
			return i
		}
		i = next
		if i >= len(s) || s[i] != '.' || i+1 >= len(s) || !(isSQLIdentifierStart(s[i+1]) || s[i+1] == '"') {
			return i
		}
		i++
	}
}

func unquotePostgresIdentifier(name string) string {
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return name
}

// unquoteSimpleIdentifiers removes quotes PostgreSQL adds around identifiers
// that are keywords, such as "left"(name, 3). Only names that read the same
// unquoted are changed, and never words that would turn into a value or
// operator (null, true, and, ...).
func unquoteSimpleIdentifiers(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			name := s[i+1 : max(end-1, i+1)]
			if isLowerSimpleIdentifier(name) && !isDeparsedKeyword(name) && !isSQLValueKeyword(name) {
				b.WriteString(name)
			} else {
				b.WriteString(s[i:end])
			}
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isLowerSimpleIdentifier(name string) bool {
	if name == "" || !(name[0] == '_' || name[0] >= 'a' && name[0] <= 'z') {
		return false
	}
	for i := 1; i < len(name); i++ {
		ch := name[i]
		if !(ch == '_' || ch == '$' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func isSQLValueKeyword(word string) bool {
	switch strings.ToLower(word) {
	case "true", "false", "null", "default", "user", "current_user", "session_user", "current_role",
		"current_date", "current_time", "current_timestamp", "localtime", "localtimestamp",
		"current_catalog", "current_schema", "not", "and", "or", "is", "in", "like", "ilike",
		"similar", "between", "case", "when", "then", "else", "end", "cast", "as", "array", "any", "all",
		"some", "exists", "distinct", "from", "collate", "at", "isnull", "notnull", "overlaps":
		return true
	}
	return false
}

// rewriteCastSyntax rewrites CAST(x AS t) as (x)::t, the form PostgreSQL
// stores.
func rewriteCastSyntax(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if sqlKeywordAt(s, i, "CAST") {
			open := skipSQLSpace(s, i+len("CAST"))
			if open < len(s) && s[open] == '(' {
				if end := matchingParenIndex(s, open); end > open {
					inner := s[open+1 : end]
					if as := lastTopLevelKeyword(inner, "AS"); as >= 0 {
						b.WriteString("(" + strings.TrimSpace(rewriteCastSyntax(inner[:as])) + ")::" + strings.TrimSpace(inner[as+len("AS"):]))
						i = end + 1
						continue
					}
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func lastTopLevelKeyword(s, keyword string) int {
	found, depth := -1, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
			continue
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && sqlKeywordAt(s, i, keyword) {
			found = i
		}
	}
	return found
}

// mapCommaList applies fn to each top-level comma-separated item of s.
func mapCommaList(s string, fn func(string) string) string {
	var items []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, fn(strings.TrimSpace(s[start:i])))
				start = i + 1
			}
		}
	}
	items = append(items, fn(strings.TrimSpace(s[start:])))
	return strings.Join(items, ", ")
}

// rewriteTokens applies fn to the whitespace-separated tokens of s and, first,
// recursively to the contents of every parenthesized group and argument list.
func rewriteTokens(s string, fn func([]string) []string) string {
	var self func(string) string
	self = func(s string) string {
		s = mapParenGroups(s, func(inner string) string { return mapCommaList(inner, self) })
		return strings.Join(fn(splitTopLevelTokens(s)), " ")
	}
	return self(s)
}

// startsOperand reports whether the token before position k (if any) ends
// the previous operand, so tokens[k] begins a boolean operand.
func startsOperand(tokens []string, k int) bool {
	if k == 0 {
		return true
	}
	switch tokens[k-1] {
	case "AND", "OR", "WHEN", "THEN", "ELSE":
		return true
	}
	return false
}

// rewriteSingleElementIn rewrites x IN (a) as x = a and x NOT IN (a) as
// x <> a, as PostgreSQL stores one-element lists.
func rewriteSingleElementIn(s string) string {
	if !strings.Contains(s, "IN") {
		return s
	}
	return rewriteTokens(s, func(tokens []string) []string {
		out := make([]string, 0, len(tokens))
		for i := 0; i < len(tokens); i++ {
			if i > 0 && tokens[i] == "IN" && i+1 < len(tokens) && isSingleItemGroup(tokens[i+1]) {
				op := "="
				if out[len(out)-1] == "NOT" {
					out = out[:len(out)-1]
					op = "<>"
				}
				out = append(out, op, strings.TrimSpace(tokens[i+1][1:len(tokens[i+1])-1]))
				i++
				continue
			}
			out = append(out, tokens[i])
		}
		return out
	})
}

func isSingleItemGroup(token string) bool {
	if len(token) < 2 || token[0] != '(' || matchingParenIndex(token, 0) != len(token)-1 {
		return false
	}
	inner := strings.TrimSpace(token[1 : len(token)-1])
	return inner != "" && !hasTopLevelComma(inner) && !sqlKeywordAt(inner, 0, "SELECT")
}

// rewriteIsNotDistinctFrom rewrites x IS NOT DISTINCT FROM y as
// NOT x IS DISTINCT FROM y, as PostgreSQL stores it. x must be one token
// that starts a boolean operand, so the NOT covers exactly the comparison.
func rewriteIsNotDistinctFrom(s string) string {
	if !strings.Contains(s, "DISTINCT") {
		return s
	}
	return rewriteTokens(s, func(tokens []string) []string {
		out := make([]string, 0, len(tokens)+1)
		for i := 0; i < len(tokens); i++ {
			if i+4 < len(tokens) && tokens[i+1] == "IS" && tokens[i+2] == "NOT" && tokens[i+3] == "DISTINCT" && tokens[i+4] == "FROM" &&
				(startsOperand(tokens, i) || tokens[i-1] == "NOT") && isBetweenOperand(tokens[i]) {
				out = append(out, "NOT", tokens[i], "IS", "DISTINCT", "FROM")
				i += 4
				continue
			}
			out = append(out, tokens[i])
		}
		return out
	})
}

// rewritePostgresBetween expands x BETWEEN a AND b into x >= a AND x <= b and
// x NOT BETWEEN a AND b into (x < a OR x > b), as PostgreSQL stores them. x is
// the run of tokens since the start of the boolean operand and may only use
// operators that bind tighter than BETWEEN; a and b are single tokens. When
// a NOT precedes x it negates the whole range, so the expansion is grouped.
func rewritePostgresBetween(s string) string {
	if !strings.Contains(s, "BETWEEN") {
		return s
	}
	return rewriteTokens(s, func(tokens []string) []string {
		for {
			next, ok := expandOneBetween(tokens)
			if !ok {
				return tokens
			}
			tokens = next
		}
	})
}

func expandOneBetween(tokens []string) ([]string, bool) {
	for i := 1; i < len(tokens); i++ {
		negated := tokens[i] == "NOT" && i+1 < len(tokens) && tokens[i+1] == "BETWEEN"
		if tokens[i] != "BETWEEN" && !negated {
			continue
		}
		kw := i
		if negated {
			kw = i + 1
		}
		if kw+3 >= len(tokens) || tokens[kw+2] != "AND" || !isBetweenOperand(tokens[kw+1]) || !isBetweenOperand(tokens[kw+3]) || !endsOperand(tokens, kw+3) {
			continue
		}
		start := i
		for start > 0 && !isOperandBoundary(tokens[start-1]) {
			start--
		}
		x := tokens[start:i]
		if !isTightOperand(x) {
			continue
		}
		xs := strings.Join(x, " ")
		lo, hi := tokens[kw+1], tokens[kw+3]
		var expansion string
		if negated {
			expansion = "(" + xs + " < " + lo + " OR " + xs + " > " + hi + ")"
		} else {
			expansion = xs + " >= " + lo + " AND " + xs + " <= " + hi
			if start > 0 && tokens[start-1] == "NOT" {
				expansion = "(" + expansion + ")"
			}
		}
		out := append(append(append([]string{}, tokens[:start]...), expansion), tokens[kw+4:]...)
		return splitTopLevelTokens(strings.Join(out, " ")), true
	}
	return tokens, false
}

func isOperandBoundary(token string) bool {
	switch token {
	case "AND", "OR", "NOT", "WHEN", "THEN", "ELSE":
		return true
	}
	return false
}

// isTightOperand reports whether tokens form an operand whose operators all
// bind tighter than BETWEEN.
func isTightOperand(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	level, ok := operatorParenGroupPrecedence(strings.Join(tokens, " "))
	return ok && level > precLike
}

// endsOperand reports whether tokens[k] ends a boolean operand.
func endsOperand(tokens []string, k int) bool {
	if k+1 >= len(tokens) {
		return true
	}
	switch tokens[k+1] {
	case "AND", "OR", "THEN", "ELSE", "END":
		return true
	}
	return false
}

func isBetweenOperand(token string) bool {
	return token != "" && !isDeparsedKeyword(token) && !isSQLOperatorToken(token)
}

func isSQLOperatorToken(token string) bool {
	return strings.Trim(token, "+-*/<>=~!@#%^&|`?") == ""
}

// Operator precedence, following the PostgreSQL documentation. Higher binds
// tighter; 0 marks a boundary that ends an operand (start, end, a comma,
// CASE keywords).
const (
	precBoundary = iota
	precOr
	precAnd
	precNot
	precIs
	precComparison
	precLike // BETWEEN, IN, LIKE, ILIKE, SIMILAR
	precOther
	precAdd
	precMul
	precExp
	precUnary
)

func binaryOperatorPrecedence(token string) int {
	switch token {
	case "OR":
		return precOr
	case "AND":
		return precAnd
	case "IS", "ISNULL", "NOTNULL":
		return precIs
	case "<", ">", "=", "<=", ">=", "<>", "!=":
		return precComparison
	case "+", "-":
		return precAdd
	case "*", "/", "%":
		return precMul
	case "^":
		return precExp
	}
	if isSQLOperatorToken(token) {
		return precOther
	}
	return -1
}

// operatorParenGroupPrecedence returns the loosest binding operator at the
// top level of a parenthesized group's contents. It fails for contents it
// cannot classify, such as CASE or IN lists.
func operatorParenGroupPrecedence(inner string) (int, bool) {
	tokens := splitTopLevelTokens(inner)
	if len(tokens) == 0 {
		return 0, false
	}
	lowest := precUnary + 1
	expectOperand := true
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if expectOperand {
			switch {
			case token == "NOT":
				lowest = min(lowest, precNot)
				continue
			case token == "-" || token == "+":
				lowest = min(lowest, precUnary)
				continue
			case isSQLOperatorToken(token) || (isDeparsedKeyword(token) && token != "NULL"):
				return 0, false
			}
			expectOperand = false
			continue
		}
		if token == "IS" {
			// IS [NOT] NULL | TRUE | FALSE | UNKNOWN | DISTINCT FROM x
			lowest = min(lowest, precIs)
			j := i + 1
			if j < len(tokens) && tokens[j] == "NOT" {
				j++
			}
			if j < len(tokens) && (tokens[j] == "NULL" || tokens[j] == "true" || tokens[j] == "false" || strings.EqualFold(tokens[j], "UNKNOWN")) {
				i = j
				continue
			}
			if j+1 < len(tokens) && tokens[j] == "DISTINCT" && tokens[j+1] == "FROM" {
				i = j + 1
				expectOperand = true
				continue
			}
			return 0, false
		}
		prec := binaryOperatorPrecedence(token)
		if prec < 0 {
			return 0, false
		}
		lowest = min(lowest, prec)
		expectOperand = true
	}
	if expectOperand {
		return 0, false
	}
	return lowest, true
}

// neighborPrecedence classifies the token next to a group: a boundary, a
// binary operator, or something that forbids unwrapping (a list keyword, a
// unary operator, an operand).
func neighborPrecedence(tokens []string, k int, before bool) (int, bool) {
	if k < 0 || k >= len(tokens) {
		return precBoundary, true
	}
	token := tokens[k]
	switch token {
	case "WHEN", "THEN", "ELSE", "END", "CASE":
		return precBoundary, true
	case "NOT":
		if before {
			return precNot, true
		}
		return 0, false
	}
	prec := binaryOperatorPrecedence(token)
	if prec < 0 {
		return 0, false
	}
	if before && prec != precOr && prec != precAnd {
		// A symbolic operator with nothing before it is unary: -(a + b).
		if k == 0 || binaryOperatorPrecedence(tokens[k-1]) >= 0 || tokens[k-1] == "NOT" {
			return 0, false
		}
	}
	return prec, true
}

// stripRedundantOperatorParens removes parentheses PostgreSQL adds around
// operator expressions when precedence already groups them the same way:
// ((n + 1) > 0), ((a || b) || c), ((x > 0) OR (y AND z)). A group is
// unwrapped when its loosest operator binds tighter than both neighbors, or
// equally tight as the operator after it (left associativity). AND and OR are
// associative, so equal precedence on either side is fine. Comparisons and
// IS are not associative and need strictly looser neighbors.
func stripRedundantOperatorParens(s string) string {
	return rewriteTokens(s, func(tokens []string) []string {
		for k, token := range tokens {
			if len(token) < 2 || token[0] != '(' || matchingParenIndex(token, 0) != len(token)-1 {
				continue
			}
			inner := strings.TrimSpace(token[1 : len(token)-1])
			level, ok := operatorParenGroupPrecedence(inner)
			if !ok {
				continue
			}
			prev, okPrev := neighborPrecedence(tokens, k-1, true)
			next, okNext := neighborPrecedence(tokens, k+1, false)
			if !okPrev || !okNext {
				continue
			}
			associative := level == precAnd || level == precOr
			leftAssociative := level >= precOther
			if (prev < level || associative && prev == level) &&
				(next < level || (associative || leftAssociative) && next == level) {
				tokens[k] = inner
			}
		}
		return tokens
	})
}

// precededByListKeyword reports whether the parenthesis at i follows IN, ANY,
// ALL, or ARRAY, where it delimits a list rather than grouping.
func precededByListKeyword(s string, i int) bool {
	j := i - 1
	for j >= 0 && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
		j--
	}
	end := j + 1
	for j >= 0 && isSQLIdentifierByte(s[j]) {
		j--
	}
	switch strings.ToUpper(s[j+1 : end]) {
	case "IN", "ANY", "ALL", "SOME", "ARRAY", "EXISTS", "VALUES":
		return true
	}
	return false
}

// skipPostgresCast returns the index after a "::type" suffix starting at i, or
// i itself when there is no cast.
func skipPostgresCast(s string, i int) int {
	if !strings.HasPrefix(s[i:], "::") {
		return i
	}
	end := postgresCastTypeEnd(s, i+2)
	if end == i+2 {
		return i
	}
	return end
}

func numericLiteralEnd(s string, start int) (int, bool) {
	i := start
	if i < len(s) && s[i] == '-' {
		i++
	}
	digits := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if i == digits || (i < len(s) && isSQLIdentifierByte(s[i])) {
		return 0, false
	}
	return i, true
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }

func isNumericLiteral(s string) bool {
	end, ok := numericLiteralEnd(s, 0)
	return ok && end == len(s)
}

func isPostgresNumericType(castType string) bool {
	name := canonicalPostgresTypeName(castType)
	if open := strings.IndexByte(name, '('); open >= 0 {
		name = name[:open]
	}
	switch name {
	case "smallint", "integer", "bigint", "numeric", "real", "double precision":
		return true
	}
	return false
}

// postgresTypeAliases maps type spellings to the name format_type prints.
var postgresTypeAliases = map[string]string{
	"int": "integer", "int4": "integer", "integer": "integer",
	"int8": "bigint", "bigint": "bigint",
	"int2": "smallint", "smallint": "smallint",
	"float8": "double precision", "double precision": "double precision", "float": "double precision",
	"float4": "real", "real": "real",
	"bool": "boolean", "boolean": "boolean",
	"decimal": "numeric", "numeric": "numeric",
	"varchar": "character varying", "character varying": "character varying",
	"timestamptz": "timestamp with time zone", "timestamp with time zone": "timestamp with time zone",
	"timestamp": "timestamp without time zone", "timestamp without time zone": "timestamp without time zone",
	"timetz": "time with time zone", "time with time zone": "time with time zone",
	"time": "time without time zone", "time without time zone": "time without time zone",
}

// canonicalPostgresTypeName rewrites a type such as int4, timestamptz(3),
// VARCHAR(20) or pg_catalog.float8[] into the name format_type prints.
// Unquoted unknown names are lower-cased, as PostgreSQL folds them; quoted
// names, such as enums, are returned unchanged.
func canonicalPostgresTypeName(castType string) string {
	t := strings.Join(strings.Fields(strings.TrimSpace(castType)), " ")
	if strings.ContainsRune(t, '"') {
		return t
	}
	arrays := ""
	for strings.HasSuffix(t, "[]") {
		arrays += "[]"
		t = strings.TrimSpace(t[:len(t)-2])
	}
	lower := strings.TrimPrefix(strings.ToLower(t), "pg_catalog.")
	args, suffix := "", ""
	if open := strings.IndexByte(lower, '('); open >= 0 {
		close := matchingParenIndex(lower, open)
		if close < 0 {
			return t + arrays
		}
		args = strings.ReplaceAll(lower[open:close+1], " ", "")
		suffix = lower[close+1:]
		lower = lower[:open]
	}
	key := strings.TrimSpace(lower) + suffix
	if args != "" && (key == "float" || key == "char" || key == "character" || key == "bpchar") {
		if key == "float" {
			return t + arrays // float(p) is real or double precision depending on p.
		}
		return "character" + args + arrays
	}
	canonical, ok := postgresTypeAliases[key]
	if !ok {
		return lower + args + suffix + arrays
	}
	if args == "" {
		return canonical + arrays
	}
	// The modifier goes after the base word: timestamp(3) with time zone.
	if base, zone, found := strings.Cut(canonical, " with"); found {
		return base + args + " with" + zone + arrays
	}
	return canonical + args + arrays
}

// canonicalizePostgresCastTypes rewrites every ::type into its display name.
func canonicalizePostgresCastTypes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case s[i] == '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case strings.HasPrefix(s[i:], "::"):
			end := postgresCastTypeEnd(s, i+2)
			b.WriteString("::")
			b.WriteString(canonicalPostgresTypeName(s[i+2 : end]))
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// rewritePostgresOperators spells operators the way PostgreSQL deparses them:
// LIKE as ~~, ILIKE as ~~*, their NOT forms as !~~ and !~~*, and != as <>.
// It expects upper-case keywords.
func rewritePostgresOperators(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if strings.HasPrefix(s[i:], "!=") {
			b.WriteString("<>")
			i += 2
			continue
		}
		negated := false
		j := i
		if sqlKeywordAt(s, i, "NOT") {
			negated = true
			j = skipSQLSpace(s, i+len("NOT"))
		}
		op := ""
		switch {
		case sqlKeywordAt(s, j, "ILIKE"):
			op, j = "~~*", j+len("ILIKE")
		case sqlKeywordAt(s, j, "LIKE"):
			op, j = "~~", j+len("LIKE")
		}
		if op != "" {
			if negated {
				op = "!" + op
			}
			b.WriteString(op)
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// splitTopLevelTokens splits s on whitespace outside parentheses, brackets,
// and quotes, so each token is an operand, operator, or keyword.
func splitTopLevelTokens(s string) []string {
	var tokens []string
	depth, start := 0, -1
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if depth == 0 && (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') {
			if start >= 0 {
				tokens = append(tokens, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
		switch ch {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		}
	}
	if start >= 0 {
		tokens = append(tokens, s[start:])
	}
	return tokens
}

// mapParenGroups applies fn to the contents of every top-level parenthesized
// group in s, including function arguments.
func mapParenGroups(s string, fn func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '(':
			if end := matchingParenIndex(s, i); end > i {
				b.WriteByte('(')
				b.WriteString(fn(s[i+1 : end]))
				b.WriteByte(')')
				i = end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// rewritePostgresArrayMembership turns PostgreSQL's deparsed form of IN / NOT
// IN lists back into the user-facing syntax:
//
//	x = ANY (ARRAY[a, b])            -> x IN (a, b)
//	x <> ALL (ARRAY[a, b])           -> x NOT IN (a, b)
//	x = ANY ((ARRAY[a, b])::text[])  -> x IN (a, b)
func rewritePostgresArrayMembership(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		op, quantifier := "", ""
		switch {
		case strings.HasPrefix(s[i:], "<>"):
			op, quantifier = "<>", "ALL"
		case strings.HasPrefix(s[i:], "!="):
			op, quantifier = "!=", "ALL"
		case s[i] == '=' && (i == 0 || !strings.ContainsRune("<>!=", rune(s[i-1]))):
			op, quantifier = "=", "ANY"
		}
		if op != "" {
			if items, end, ok := parseArrayMembership(s, i+len(op), quantifier); ok {
				out := strings.TrimRight(b.String(), " \t\r\n")
				b.Reset()
				b.WriteString(out)
				if quantifier == "ALL" {
					b.WriteString(" NOT IN (")
				} else {
					b.WriteString(" IN (")
				}
				b.WriteString(items)
				b.WriteString(")")
				i = end
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// parseArrayMembership parses " ANY (ARRAY[...])" starting at i. It returns
// the raw array items and the index just past the closing parenthesis.
func parseArrayMembership(s string, i int, quantifier string) (string, int, bool) {
	i = skipSQLSpace(s, i)
	if !sqlKeywordAt(s, i, quantifier) {
		return "", 0, false
	}
	i = skipSQLSpace(s, i+len(quantifier))
	if i >= len(s) || s[i] != '(' {
		return "", 0, false
	}
	outerEnd := matchingParenIndex(s, i)
	if outerEnd < 0 {
		return "", 0, false
	}
	inner := strings.TrimSpace(s[i+1 : outerEnd])
	// Optional cast wrapper: (ARRAY[...])::type[]
	if strings.HasPrefix(inner, "(") {
		end := matchingParenIndex(inner, 0)
		if end < 0 {
			return "", 0, false
		}
		rest := inner[end+1:]
		if rest != "" {
			if skipPostgresCast(rest, 0) != len(rest) {
				return "", 0, false
			}
		}
		inner = strings.TrimSpace(inner[1:end])
	}
	if !sqlKeywordAt(inner, 0, "ARRAY") {
		return "", 0, false
	}
	j := skipSQLSpace(inner, len("ARRAY"))
	if j >= len(inner) || inner[j] != '[' {
		return "", 0, false
	}
	end := matchingBracketIndex(inner, j)
	if end != len(inner)-1 {
		return "", 0, false
	}
	return strings.TrimSpace(inner[j+1 : end]), outerEnd + 1, true
}

func matchingBracketIndex(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// normalizeSQLCommas makes comma spacing uniform ("a, b") outside literals.
func normalizeSQLCommas(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case ',':
			out := strings.TrimRight(b.String(), " \t\r\n")
			b.Reset()
			b.WriteString(out)
			b.WriteString(", ")
			i = skipSQLSpace(s, i+1)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

var postgresDeparsedKeywords = []string{
	"AND", "OR", "NOT", "IN", "IS", "NULL", "ANY", "ALL", "ARRAY",
	"CASE", "WHEN", "THEN", "ELSE", "END", "LIKE", "ILIKE", "BETWEEN", "DISTINCT", "FROM",
	"SYMMETRIC", "COALESCE", "NULLIF", "GREATEST", "LEAST",
}

// uppercaseSQLKeywords upper-cases the keywords PostgreSQL emits in upper case
// and lower-cases true/false, so hand-written predicates compare equal.
func uppercaseSQLKeywords(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			end := postgresStringLiteralEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if isSQLIdentifierByte(s[i]) && (i == 0 || !isSQLIdentifierByte(s[i-1])) {
			end := i
			for end < len(s) && isSQLIdentifierByte(s[end]) {
				end++
			}
			word := s[i:end]
			// Skip qualified names and cast type names (tbl.end, x::any).
			qualified := (i > 0 && s[i-1] == '.') || (end < len(s) && s[end] == '.') || (i > 1 && s[i-2:i] == "::")
			if !qualified && isDeparsedKeyword(word) {
				word = strings.ToUpper(word)
			} else if !qualified && (strings.EqualFold(word, "true") || strings.EqualFold(word, "false")) {
				word = strings.ToLower(word)
			}
			b.WriteString(word)
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isDeparsedKeyword(word string) bool {
	for _, kw := range postgresDeparsedKeywords {
		if strings.EqualFold(word, kw) {
			return true
		}
	}
	return false
}

func hasTopLevelComma(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}
