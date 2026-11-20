package migrate

import "testing"

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
			got, want := normalizeIndexPredicate(tt.database), normalizeIndexPredicate(tt.schema)
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

func TestStripPostgresLiteralCastsLeavesIdentifiersAlone(t *testing.T) {
	got := stripPostgresLiteralCasts(`col1::bigint + t2.x + (-1)::integer + 2.5::numeric + 'a'::uuid`, true)
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
