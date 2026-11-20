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
			b.WriteString(s[i:end])
			i = end
			if includeStrings {
				i = skipPostgresCast(s, end)
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
}

// uppercaseSQLKeywords upper-cases the keywords PostgreSQL emits in upper case
// so that hand-written lower-case predicates compare equal.
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
