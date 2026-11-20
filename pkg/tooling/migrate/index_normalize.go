package migrate

import "strings"

// PostgreSQL deparses index expressions and predicates into a canonical form
// that differs textually from what users typically write in a schema. The
// helpers below rewrite both sides into a shared shape so that db push does
// not drop and recreate indexes whose definitions are semantically unchanged.

// stripPostgresLiteralCasts removes casts PostgreSQL attaches to constants when
// it resolves their type from context, e.g. (0)::bigint and, when
// includeStrings is set, '…'::uuid or 'x'::my_enum.
func stripPostgresLiteralCasts(s string, includeStrings bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '"':
			end := postgresQuotedIdentifierEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		case s[i] == '\'':
			end := postgresStringLiteralEnd(s, i)
			castEnd := skipPostgresCast(s, end)
			// PostgreSQL stores negative constants as '-1'::integer.
			if castEnd > end && end-1 > i && s[end-1] == '\'' && isPostgresNumericType(s[end+2:castEnd]) {
				if inner := s[i+1 : end-1]; isNumericLiteral(inner) {
					b.WriteString(inner)
					i = castEnd
					continue
				}
			}
			b.WriteString(s[i:end])
			i = end
			if includeStrings {
				i = castEnd
			}
			continue
		case s[i] == '(':
			if numEnd, ok := numericLiteralEnd(s, skipSQLSpace(s, i+1)); ok {
				closeIdx := skipSQLSpace(s, numEnd)
				if closeIdx < len(s) && s[closeIdx] == ')' && strings.HasPrefix(s[closeIdx+1:], "::") {
					if next := skipPostgresCast(s, closeIdx+1); next > closeIdx+1 {
						b.WriteString(strings.TrimSpace(s[i+1 : closeIdx]))
						i = next
						continue
					}
				}
			}
		case isDigit(s[i]) && (i == 0 || !isSQLIdentifierByte(s[i-1]) && s[i-1] != '.'):
			if numEnd, ok := numericLiteralEnd(s, i); ok {
				b.WriteString(s[i:numEnd])
				i = skipPostgresCast(s, numEnd)
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
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

// canonicalPostgresTypeName rewrites a cast type such as int4, timestamptz(3)
// or pg_catalog.float8[] into PostgreSQL's display name. Unknown types, such as
// enums, are returned unchanged.
func canonicalPostgresTypeName(castType string) string {
	t := strings.Join(strings.Fields(strings.TrimSpace(castType)), " ")
	arrays := ""
	for strings.HasSuffix(t, "[]") {
		arrays += "[]"
		t = strings.TrimSpace(t[:len(t)-2])
	}
	lower := strings.ToLower(t)
	lower = strings.TrimPrefix(lower, "pg_catalog.")
	args, suffix := "", ""
	if open := strings.IndexByte(lower, '('); open >= 0 {
		close := matchingParenIndex(lower, open)
		if close < 0 {
			return castType
		}
		args = strings.ReplaceAll(lower[open:close+1], " ", "")
		suffix = lower[close+1:]
		lower = lower[:open]
	}
	canonical, ok := postgresTypeAliases[strings.TrimSpace(lower)+suffix]
	if !ok {
		return castType
	}
	if args == "" {
		return canonical + arrays
	}
	// Precision goes after the base word: timestamp(3) with time zone.
	base, zone, _ := strings.Cut(canonical, " with")
	if zone != "" {
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

// rewritePostgresBetween expands x BETWEEN a AND b into x >= a AND x <= b and
// x NOT BETWEEN a AND b into (x < a OR x > b), as PostgreSQL stores them.
// Only whitespace-free operands are rewritten. Expects upper-case keywords.
func rewritePostgresBetween(s string) string {
	if !strings.Contains(s, "BETWEEN") {
		return s
	}
	tokens := splitTopLevelTokens(mapParenGroups(s, rewritePostgresBetween))
	var out []string
	for i := 0; i < len(tokens); i++ {
		if i+4 < len(tokens) && tokens[i+1] == "BETWEEN" && tokens[i+3] == "AND" &&
			isBetweenOperand(tokens[i]) && isBetweenOperand(tokens[i+2]) && isBetweenOperand(tokens[i+4]) {
			x, lo, hi := tokens[i], tokens[i+2], tokens[i+4]
			out = append(out, x, ">=", lo, "AND", x, "<=", hi)
			i += 4
			continue
		}
		if i+5 < len(tokens) && tokens[i+1] == "NOT" && tokens[i+2] == "BETWEEN" && tokens[i+4] == "AND" &&
			isBetweenOperand(tokens[i]) && isBetweenOperand(tokens[i+3]) && isBetweenOperand(tokens[i+5]) {
			x, lo, hi := tokens[i], tokens[i+3], tokens[i+5]
			out = append(out, "("+x, "<", lo, "OR", x, ">", hi+")")
			i += 5
			continue
		}
		out = append(out, tokens[i])
	}
	return strings.Join(out, " ")
}

func isBetweenOperand(token string) bool {
	return token != "" && !isDeparsedKeyword(token) && !isSQLOperatorToken(token)
}

func isSQLOperatorToken(token string) bool {
	return strings.Trim(token, "+-*/<>=~!@#%^&|`?") == ""
}

// postgresOperatorPrecedence groups left-associative binary operators that
// share a precedence level.
func postgresOperatorPrecedence(op string) int {
	switch op {
	case "+", "-":
		return 1
	case "*", "/", "%":
		return 2
	case "||", "->", "->>", "#>", "#>>":
		return 3
	}
	return 0
}

// stripLeftAssociativeParens removes the parentheses PostgreSQL adds around
// the left operand of a left-associative chain: ((a || b) || c) has the same
// meaning as a || b || c. Only a group that starts an operand list (after
// "(", ",", or at the start) is unwrapped.
func stripLeftAssociativeParens(s string) string {
	for {
		next := stripOneLeftAssociativeGroup(s)
		if next == s {
			return s
		}
		s = next
	}
}

func stripOneLeftAssociativeGroup(s string) string {
	prev := byte(0)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
			prev = '\''
			continue
		case '"':
			i = postgresQuotedIdentifierEnd(s, i) - 1
			prev = '"'
			continue
		case ' ', '\t', '\n', '\r':
			continue
		case '(':
			if prev != 0 && prev != '(' && prev != ',' {
				break
			}
			end := matchingParenIndex(s, i)
			if end < 0 {
				return s
			}
			after := splitTopLevelTokens(s[end+1:])
			if len(after) == 0 {
				break
			}
			level := postgresOperatorPrecedence(after[0])
			if level == 0 || !isLeftAssociativeChain(s[i+1:end], level) {
				break
			}
			return s[:i] + strings.TrimSpace(s[i+1:end]) + s[end+1:]
		}
		prev = s[i]
	}
	return s
}

// isLeftAssociativeChain reports whether s is operand (op operand)+ with
// every operator at the given precedence level.
func isLeftAssociativeChain(s string, level int) bool {
	tokens := splitTopLevelTokens(s)
	if len(tokens) < 3 || len(tokens)%2 == 0 {
		return false
	}
	for i, token := range tokens {
		if i%2 == 1 {
			if postgresOperatorPrecedence(token) != level {
				return false
			}
		} else if isSQLOperatorToken(token) || isDeparsedKeyword(token) {
			return false
		}
	}
	return true
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
