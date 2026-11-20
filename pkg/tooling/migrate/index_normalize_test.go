package migrate

import "testing"

// varcharColumns is the context of a table whose status, email, and code
// columns are varchar, which PostgreSQL coerces to text in comparisons.
var varcharColumns = indexNormalizer{columnTypes: map[string]string{
	"status": "character varying(20)",
	"email":  "character varying(255)",
	"code":   "character varying(4)",
}}

func TestNormalizeIndexPredicateMatchesPostgresDeparse(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		database string
	}{
		{
			name:     "int IN list",
			schema:   `status IN (0,1,2,5)`,
			database: `(status = ANY (ARRAY[0, 1, 2, 5]))`,
		},
		{
			name:     "text IN list",
			schema:   `execution_status IN ('queued','scheduled','retry_wait','waiting')`,
			database: `(execution_status = ANY (ARRAY['queued'::text, 'scheduled'::text, 'retry_wait'::text, 'waiting'::text]))`,
		},
		{
			name:     "varchar IN list",
			schema:   `status in ('a', 'b')`,
			database: `((status)::text = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[]))`,
		},
		{
			name:     "NOT IN list",
			schema:   `status NOT IN (3, 4)`,
			database: `(status <> ALL (ARRAY[3, 4]))`,
		},
		{
			name:     "uuid literal",
			schema:   `server_id = '00000000-0000-0000-0000-000000000000'`,
			database: `(server_id = '00000000-0000-0000-0000-000000000000'::uuid)`,
		},
		{
			name:     "compound with IN",
			schema:   `deleted = false AND kind IN ('a','b')`,
			database: `((deleted = false) AND (kind = ANY (ARRAY['a'::text, 'b'::text])))`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, want := varcharColumns.predicate(tt.database), varcharColumns.predicate(tt.schema)
			if got != want {
				t.Fatalf("predicate mismatch:\n database: %q\n schema:   %q", got, want)
			}
		})
	}
}

func TestNormalizeIndexPredicateKeepsDistinctListsDistinct(t *testing.T) {
	a := normalizeIndexPredicate(`status IN (0,1,2)`)
	b := normalizeIndexPredicate(`(status = ANY (ARRAY[0, 1, 2, 5]))`)
	if a == b {
		t.Fatalf("expected different predicates to differ, both = %q", a)
	}
}

func TestNormalizeIndexExpressionStripsNumericLiteralCasts(t *testing.T) {
	schema := normalizeIndexExpression(`(CASE WHEN ended_at IS NULL THEN 0 ELSE id END)`)
	database := normalizeIndexExpression("(\nCASE\n    WHEN (ended_at IS NULL) THEN (0)::bigint\n    ELSE id\nEND)")
	if schema != database {
		t.Fatalf("expression mismatch:\n database: %q\n schema:   %q", database, schema)
	}
}

func TestStripCastsLeavesUnknownColumnsAlone(t *testing.T) {
	got := indexNormalizer{}.stripCasts(`col1::bigint + t2.x + (-1)::integer + 2.5::numeric + 'a'::uuid`, true)
	want := `col1::bigint + t2.x + -1 + 2.5 + 'a'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeIndexCollationIgnoresPgCatalogSchema(t *testing.T) {
	if got, want := normalizeIndexCollation(`pg_catalog."C"`), normalizeIndexCollation("C"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if normalizeIndexCollation("pg_catalog.C") == normalizeIndexCollation("POSIX") {
		t.Fatal("different collations must stay different")
	}
}

// Database strings below were read back from PostgreSQL 16.
func TestNormalizeIndexPredicateMatchesPostgresOperatorDeparse(t *testing.T) {
	tests := []struct{ schema, database string }{
		{`score BETWEEN 1 AND 10`, `((score >= (1)::double precision) AND (score <= (10)::double precision))`},
		{`score NOT BETWEEN 1 AND 10 AND active`, `(((score < (1)::double precision) OR (score > (10)::double precision)) AND active)`},
		{`sid::int > 0 AND length(sid) = 1`, `(((sid)::integer > 0) AND (length(sid) = 1))`},
		{`coalesce(neg, 0) > 0`, `(COALESCE(neg, 0) > 0)`},
		{`score > 1.5`, `(score > (1.5)::double precision)`},
		{`email ilike 'a%'`, `((email)::text ~~* 'a%'::text)`},
		{`email LIKE '%@x.com'`, `((email)::text ~~ '%@x.com'::text)`},
		{`email NOT LIKE 'a%'`, `((email)::text !~~ 'a%'::text)`},
		{`neg > -1`, `(neg > '-1'::integer)`},
		{`neg != 0`, `(neg <> 0)`},
		{`active = TRUE`, `(active = true)`},
		{`ts::timestamptz > now()`, `((ts)::timestamp with time zone > now())`},
		{`amount::decimal(10, 2) > 0`, `((amount)::numeric(10,2) > (0)::numeric)`},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			if got, want := varcharColumns.predicate(tt.database), varcharColumns.predicate(tt.schema); got != want {
				t.Fatalf("predicate mismatch:\n database: %q\n schema:   %q", got, want)
			}
		})
	}
}

func TestNormalizeIndexExpressionMatchesPostgresDeparse(t *testing.T) {
	tests := []struct{ schema, database string }{
		{`coalesce(name, '')`, `COALESCE(name, ''::text)`},
		{`(name || '-' || code)`, `(((name || '-'::text) || (code)::text))`},
		{`(metab -> 'a' ->> 'b')`, `(((metab -> 'a'::text) ->> 'b'::text))`},
		{`(a + b - c)`, `(((a + b) - c))`},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			if got, want := varcharColumns.expression(tt.database), varcharColumns.expression(tt.schema); got != want {
				t.Fatalf("expression mismatch:\n database: %q\n schema:   %q", got, want)
			}
		})
	}
}

func TestNormalizeIndexKeepsSemanticDifferences(t *testing.T) {
	pairs := [][2]string{
		{`score BETWEEN 1 AND 10`, `score BETWEEN 1 AND 11`},
		{`email LIKE 'a%'`, `email ILIKE 'a%'`},
		{`email LIKE 'a%'`, `email NOT LIKE 'a%'`},
		{`neg > -1`, `neg > 1`},
		{`a - (b - c) > 0`, `a - b - c > 0`},
		{`sid::int > 0`, `sid::bigint > 0`},
		{`lower(email) = 'a'`, `loweremail = 'a'`},
	}
	for _, p := range pairs {
		if normalizeIndexPredicate(p[0]) == normalizeIndexPredicate(p[1]) {
			t.Errorf("%q and %q normalize to the same predicate %q", p[0], p[1], normalizeIndexPredicate(p[0]))
		}
	}
	if normalizeIndexExpression(`(metab -> ('a' ->> 'b'))`) == normalizeIndexExpression(`(metab -> 'a' ->> 'b')`) {
		t.Error("right-nested operator groups must keep their parentheses")
	}
}

func TestCanonicalPostgresTypeName(t *testing.T) {
	tests := map[string]string{
		"int":                         "integer",
		"INT8[]":                      "bigint[]",
		"pg_catalog.float8":           "double precision",
		"timestamptz(3)":              "timestamp(3) with time zone",
		"timestamp(3) with time zone": "timestamp(3) with time zone",
		"decimal(10, 2)":              "numeric(10,2)",
		"varchar(20)":                 "character varying(20)",
		`"Role"`:                      `"Role"`,
	}
	for in, want := range tests {
		if got := canonicalPostgresTypeName(in); got != want {
			t.Errorf("canonicalPostgresTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}
