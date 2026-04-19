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
