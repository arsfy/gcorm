package dbpush

import (
	"strings"
	"unicode"
)

// sqliteIndexPredicate finds WHERE after the indexed columns. Quoted names,
// comments, and expressions can themselves contain parentheses or WHERE.
func sqliteIndexPredicate(ddl string) string {
	depth := 0
	keysEnd := false
	for i := 0; i < len(ddl); {
		switch {
		case strings.HasPrefix(ddl[i:], "--"):
			end := strings.IndexByte(ddl[i:], '\n')
			if end < 0 {
				return ""
			}
			i += end + 1
		case strings.HasPrefix(ddl[i:], "/*"):
			end := strings.Index(ddl[i+2:], "*/")
			if end < 0 {
				return ""
			}
			i += end + 4
		case keysEnd && !unicode.IsSpace(rune(ddl[i])):
			if len(ddl[i:]) > len("WHERE") && strings.EqualFold(ddl[i:i+len("WHERE")], "WHERE") {
				return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ddl[i+len("WHERE"):]), ";"))
			}
			return ""
		case ddl[i] == '\'' || ddl[i] == '"' || ddl[i] == '`' || ddl[i] == '[':
			i = sqliteQuotedTokenEnd(ddl, i)
		case ddl[i] == '(':
			depth++
			i++
		case ddl[i] == ')':
			depth--
			keysEnd = depth == 0
			i++
		default:
			i++
		}
	}
	return ""
}

func sqliteQuotedTokenEnd(sql string, start int) int {
	quote := sql[start]
	if quote == '[' {
		quote = ']'
	}
	for i := start + 1; i < len(sql); i++ {
		if sql[i] != quote {
			continue
		}
		if quote != ']' && i+1 < len(sql) && sql[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(sql)
}
