package migrate

import (
	"testing"
	"unicode/utf8"
)

// FuzzIndexNormalization checks that normalizing arbitrary text never panics
// (db push normalizes every index it reads) and is idempotent.
func FuzzIndexNormalization(f *testing.F) {
	for _, seed := range oraclePredicates {
		f.Add(seed)
	}
	for _, seed := range oracleExpressions {
		f.Add(seed)
	}
	for _, seed := range []string{
		"", "(", ")", "'", "\"", "''", "('", "::", "a::", "(a)::", "'x'::", "CAST(", "CAST(a AS", "x BETWEEN",
		"x IN (", "= ANY (ARRAY[", "NOT", "- -", "((()))", "E'\\''", "$$x$$", "a ->> 'b' :: text",
	} {
		f.Add(seed)
	}
	normalizer := indexNormalizer{
		enumTypes:   map[string]struct{}{"role_t": {}},
		columnTypes: map[string]string{"email": "character varying(255)", "n": "integer", "name": "text"},
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			t.Skip("schema files and catalog output are UTF-8")
		}
		for _, normalize := range []func(string) string{normalizer.predicate, normalizer.expression} {
			once := normalize(s)
			if twice := normalize(once); twice != once {
				t.Fatalf("not idempotent for %q:\n  once:  %q\n  twice: %q", s, once, twice)
			}
		}
	})
}
