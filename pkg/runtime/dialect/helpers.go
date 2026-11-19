package dialect

import "strings"

// containsAny reports whether s contains any of the given patterns
// (case-insensitive).
func containsAny(s string, patterns ...string) bool {
	lower := strings.ToLower(s)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// extractBetweenQuotes returns the first quoted substring that appears after
// the given marker in msg. It recognises double-quotes, single-quotes and
// backticks as delimiters. Returns "" when nothing is found.
func extractBetweenQuotes(msg, marker string) string {
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, strings.ToLower(marker))
	if idx < 0 {
		return ""
	}
	rest := msg[idx+len(marker):]
	rest = strings.TrimLeft(rest, " ")
	if len(rest) < 2 {
		return ""
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' && quote != '`' {
		return ""
	}
	end := strings.IndexByte(rest[1:], quote)
	if end < 0 {
		return ""
	}
	return rest[1 : end+1]
}

// dbGeneratedDefaultExpression returns the raw SQL expression of a
// dbgenerated("expr") default. The expression is emitted verbatim; args[0]
// may be either the resolved (already unquoted) schema string or the schema
// string literal as written ("..." with backslash escapes). Extra args are
// rejoined so an expression containing commas that a caller split apart is
// restored instead of truncated.
func dbGeneratedDefaultExpression(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) == 1 {
		return unquoteSchemaString(args[0])
	}
	return strings.Join(args, ", ")
}

// unquoteSchemaString strips the quotes of a complete schema string literal
// ("..." whose inner quotes are backslash-escaped). Any other value is
// returned unchanged, so already-resolved expressions such as
// `"a" || "b"` keep their quoted identifiers.
func unquoteSchemaString(value string) string {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return value
	}
	var out strings.Builder
	inner := value[1 : len(value)-1]
	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		switch ch {
		case '"':
			// An unescaped quote means the value is not a single literal.
			return value
		case '\\':
			if i+1 >= len(inner) {
				return value
			}
			i++
			switch inner[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case '\\', '"':
				out.WriteByte(inner[i])
			default:
				return value
			}
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}

// quoteSQLStringLiteral returns value as a single-quoted SQL string literal.
func quoteSQLStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// functionCallExpression renders funcName(args...) without dropping args.
func functionCallExpression(funcName string, args []string) string {
	return funcName + "(" + strings.Join(args, ", ") + ")"
}
