package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// The tests in this file use PostgreSQL as an oracle for index predicate and
// expression normalization. Each corpus entry is stored in a real index and
// read back with the same catalog functions db push introspects with.
//
//   - Convergence: N(schema text) == N(stored text), otherwise every push
//     rebuilds the index (or falls back to the db push probe).
//   - Soundness: entries PostgreSQL stores differently must not normalize to
//     the same text, otherwise a real change is silently skipped. The db push
//     probe cannot recover from this, so it is the critical property.
//   - Idempotence: N(N(x)) == N(x).

const oracleTableDDL = `
CREATE TYPE role_t AS ENUM ('USER', 'admin');
CREATE TABLE t (
  id bigint, n int, sm smallint, f double precision, r real, d numeric(10,2),
  email varchar(255), name text, code char(4), b boolean,
  ts timestamp, tstz timestamptz, dt date, ended timestamptz,
  j jsonb, u uuid, tags text[], nums int[], role role_t
);`

var oraclePredicates = []string{
	// Comparison operators and literals.
	"n > 0", "n > 1", "n >= 0", "n < 0", "n <> 0", "n != 0", "n = 0", "0 < n", "n >0", "(n > 0)",
	"n > -1", "n > (-1)", "-n > 1", "n > 0.5", "n > 1e3",
	// Boolean structure.
	"n > 0 AND sm > 0", "n > 0 and sm > 0", "n > 0 OR sm > 0", "sm > 0 AND n > 0",
	"(n > 0 OR sm > 0) AND b", "n > 0 OR (sm > 0 AND b)", "n > 0 OR sm > 0 AND b",
	"(n > 0 OR sm > 0) AND (b OR id > 0)", "n > 0 AND (sm > 0 AND b)", "n > 0 AND sm > 0 AND b",
	"NOT b", "NOT (n > 0 AND b)", "NOT n > 0", "b", "b = true", "b = TRUE", "b = false", "b IS TRUE", "b IS NOT FALSE",
	"n IS NULL", "n IS NOT NULL", "NOT (n IS NULL)", "ended IS NULL", "ended IS NULL OR b",
	// Lists.
	"n IN (1,2,3)", "n IN (1, 2)", "n IN (2,1,3)", "n NOT IN (1,2,3)", "n = ANY ('{1,2,3}')", "n = 1 OR n = 2",
	"email IN ('a','b')", "email IN ('a','b','c')", "name IN ('a','b')", "role IN ('admin','USER')", "role NOT IN ('admin')",
	// Text.
	"email = 'a'", "email = 'A'", "lower(email) = 'a'", "upper(email) = 'A'", "email::text = 'a'",
	"email LIKE 'a%'", "email ILIKE 'a%'", "email NOT LIKE 'a%'", "email NOT ILIKE 'a%'", "email like 'a%'",
	"email ~ '^a'", "email ~* '^a'", "name LIKE 'a%'", "name = 'it''s'", "name = 'x'", "name <> ''", "name > 'a'",
	"code = 'ab'", "code::text = 'ab'", "name IS DISTINCT FROM 'x'", "name IS NOT DISTINCT FROM 'x'",
	"length(name) > 0", "char_length(name) > 0", "name || 'x' = 'ax'", "name || email = 'x'", "(name || email) || code = 'x'",
	"name || (email || code) = 'x'",
	// Numerics.
	"f > 1.5", "f > 1", "f >= 1.5", "r > 1.5", "d > 1.5", "d > 1.50", "d > 1",
	"n BETWEEN 1 AND 10", "n BETWEEN 1 AND 11", "n NOT BETWEEN 1 AND 10", "d BETWEEN 1 AND 10", "f BETWEEN 1 AND 10",
	"n BETWEEN SYMMETRIC 1 AND 10", "n >= 1 AND n <= 10",
	"n + 1 > 0", "n - 1 > 0", "n - (1 - sm) > 0", "n - 1 - sm > 0", "(n - 1) - sm > 0", "n * 2 + 1 > 0",
	"n * (2 + 1) > 0", "(n * 2) + 1 > 0", "n % 2 = 0", "n / 2 = 0", "abs(n) > 1",
	"coalesce(n, 0) > 0", "coalesce(n, 1) > 0", "COALESCE(n, 0) > 0", "nullif(n, 0) > 0", "greatest(n, sm) > 0",
	// Casts.
	"id::text = '1'", "id = 1", "n::bigint > 0", "n::int8 > 0", "n::int > 0", "n::numeric > 0", "f::int > 0",
	"name::int > 0", "name::bigint > 0", "ts::date > '2020-01-01'", "CAST(n AS bigint) > 0",
	// Dates and times.
	"ts > '2020-01-01'", "ts > '2020-01-02'", "dt > '2020-01-01'", "dt = '2020-01-01'::date",
	"tstz > '2020-01-01'", "tstz > '2020-01-01'::timestamptz",
	// JSON, UUID, arrays, enums.
	"j ? 'k'", "j ->> 'k' = 'v'", "j -> 'a' ->> 'b' = 'v'", "(j -> 'a') ->> 'b' = 'v'", "j @> '{\"a\":1}'", "j ->> 'k' IS NOT NULL",
	"u = '00000000-0000-0000-0000-000000000000'", "u <> '00000000-0000-0000-0000-000000000000'", "u IS NOT NULL",
	"tags <> '{}'", "cardinality(tags) > 0", "'a' = ANY (tags)", "tags @> ARRAY['a']", "nums @> ARRAY[1]",
	"role = 'admin'", "role <> 'USER'",
	// Literal contents and quoting are significant.
	"name = 'a  b'", "name = 'a b'", "name = 'a\nb'", "name = '('", "name = ')'", "name = 'A'", "name = 'a'",
	"name = ''", "name = 'it''s fine'", "name = 'and'", "name = 'AND'", "(name = '(') AND (code = ')')",
	"email = 'abcdefghijkl'", "email = 'abcdefghijkl'::varchar(3)", "email::varchar(3) = 'abc'", "email::varchar(4) = 'abc'",
	"code = 'ab'::bpchar", "code::varchar = 'ab'", "u::text = '00000000-0000-0000-0000-000000000000'",
	"\"left\"(name, 1) = 'a'", "left(name, 1) = 'a'", "lower(name) = 'a'", "upper(name) = 'a'",
	// Rewrites that must respect precedence.
	"n + 1 BETWEEN 1 AND 10", "NOT n BETWEEN 1 AND 10", "NOT (n BETWEEN 1 AND 10)", "b OR n BETWEEN 1 AND 10",
	"n BETWEEN 1 AND 10 AND b", "n IN (1)", "n NOT IN (1)", "n IN (1) OR b",
	"name IS NOT DISTINCT FROM 'x' AND b", "NOT name IS NOT DISTINCT FROM 'x'",
	"- (n + 1) > 0", "-(n + 1) > 0", "- n + 1 > 0", "n * -1 > 0", "(n > 0) = b",
	"(n > 0 OR b) AND sm > 0", "n > 0 OR b AND sm > 0", "(n > 0 OR b) OR sm > 0", "n > 0 OR (b OR sm > 0)",
	"NOT (b AND n > 0)", "NOT b AND n > 0", "NOT (b OR n > 0)",
	"(n + 1) * 2 > 0", "n + 1 * 2 > 0", "(n ^ 2) ^ 3 > 0", "n ^ (2 ^ 3) > 0", "n - (sm - 1) > 0",
	"(j -> 'a') ? 'b'", "j -> ('a') ? 'b'",
	// CASE.
	"CASE WHEN b THEN n ELSE 0 END > 0", "CASE WHEN b THEN n END > 0", "CASE WHEN b THEN n ELSE NULL END > 0",
	"CASE n WHEN 1 THEN true ELSE false END",
}

var oracleExpressions = []string{
	"lower(email)", "upper(email)", "lower(name)", "(lower(email))", "lower(email::text)", "lower((email)::text)",
	"coalesce(name, '')", "coalesce(name, 'x')", "COALESCE(name, '')",
	"(name || '-' || code)", "(name || code)", "(name || ('-' || code))", "((name || '-') || code)",
	"(j ->> 'k')", "(j -> 'a' ->> 'b')", "(j -> 'a' -> 'b')", "(j -> (j ->> 'b'))",
	"(n + 1)", "(n - 1)", "(n + sm)", "(n - (1 - sm))", "(n - 1 - sm)", "(n * 2)", "(n * 2 + 1)", "(n * (2 + 1))",
	"(CASE WHEN ended IS NULL THEN 0 ELSE id END)", "(CASE WHEN ended IS NULL THEN 1 ELSE id END)",
	"(CASE WHEN ended IS NULL THEN 0 END)", "(CASE WHEN ended IS NULL THEN 0 ELSE NULL END)",
	"(CASE WHEN ended IS NOT NULL THEN 0 END)",
	"date_trunc('day', ts)", "date_trunc('hour', ts)", "(ts::date)", "(id::text)", "(n::bigint)", "(n::int8)",
	"md5(name)", "abs(n)", "(CASE WHEN role = 'admin' THEN 1 ELSE 0 END)",
	"(CASE WHEN role = 'USER' THEN 1 ELSE 0 END)", "(b::int)", "(f::numeric)", "(d * 100)", "(tags[1])",
	"(nums[1] + 1)", "(name::varchar(10))", "(name::varchar(20))", "left(name, 3)", "substr(name, 1, 3)",
	"(n > -1)", "(n = ANY (ARRAY[1, 2]))", "(email LIKE 'a%')", "(n BETWEEN 1 AND 10)",
	"(name || '  ')", "(name || ' ')", "lower(name || ',')", "lower(name || ', ')", "coalesce(name, 'a,b')", "coalesce(name, 'a, b')",
	"(email::varchar(10))", "(email::varchar(300))", "(code::text)", "(code || 'x')", "((code)::text || 'x')",
	"(n::bigint + 1)", "(n + 1)::bigint", "((n + 1)::bigint)", "(- n)", "(-n)", "(- (n + 1))",
	"\"left\"(name, 3)", "\"right\"(name, 3)",
}

type oracleEntry struct {
	input, stored string
}

func TestPostgresIndexPredicateNormalizationOracle(t *testing.T) {
	entries, normalizer := loadOracleEntries(t, oraclePredicates, func(i int, p string) string {
		return fmt.Sprintf("CREATE INDEX i%d ON t (id) WHERE %s", i, p)
	}, "pg_get_expr(ix.indpred, ix.indrelid)")
	checkOracle(t, entries, normalizer.predicate, oraclePredicateUnconverged)
}

func TestPostgresIndexExpressionNormalizationOracle(t *testing.T) {
	entries, normalizer := loadOracleEntries(t, oracleExpressions, func(i int, e string) string {
		return fmt.Sprintf("CREATE INDEX i%d ON t ((%s))", i, e)
	}, "pg_get_indexdef(ix.indexrelid, 1, false)")
	checkOracle(t, entries, normalizer.expression, oracleExpressionUnconverged)
}

// Entries text normalization does not settle; db push compares them with the
// database probe instead. Each needs a reason.
var oraclePredicateUnconverged = map[string]string{
	"n > 0.5":                          "implicit int-to-numeric promotion needs operator resolution",
	"n > 1e3":                          "implicit int-to-numeric promotion needs operator resolution",
	"greatest(n, sm) > 0":              "implicit smallint-to-integer promotion needs operator resolution",
	"(n ^ 2) ^ 3 > 0":                  "implicit int-to-double promotion needs operator resolution",
	"n ^ (2 ^ 3) > 0":                  "implicit int-to-double promotion needs operator resolution",
	"(name || email) || code = 'x'":    "char(n) is coerced to text; stripping that cast is unsound in comparisons",
	"name || (email || code) = 'x'":    "char(n) is coerced to text; stripping that cast is unsound in comparisons",
	"n BETWEEN SYMMETRIC 1 AND 10":     "BETWEEN SYMMETRIC expands into four comparisons",
	"ts > '2020-01-01'":                "timestamp literals are reformatted",
	"ts > '2020-01-02'":                "timestamp literals are reformatted",
	"tstz > '2020-01-01'":              "timestamptz literals depend on the session TimeZone",
	"tstz > '2020-01-01'::timestamptz": "timestamptz literals depend on the session TimeZone",
	"j @> '{\"a\":1}'":                 "jsonb literals are reformatted",
}

var oracleExpressionUnconverged = map[string]string{
	"(name || '-' || code)":   "char(n) is coerced to text; stripping that cast is unsound in comparisons",
	"(name || code)":          "char(n) is coerced to text; stripping that cast is unsound in comparisons",
	"(name || ('-' || code))": "char(n) is coerced to text; stripping that cast is unsound in comparisons",
	"((name || '-') || code)": "char(n) is coerced to text; stripping that cast is unsound in comparisons",
	"(code || 'x')":           "char(n) is coerced to text; stripping that cast is unsound in comparisons",
}

// Pairs PostgreSQL stores differently that still mean the same, so they may
// normalize alike. AND and OR are associative.
var oracleEquivalent = [][2]string{
	{"n > 0 AND (sm > 0 AND b)", "n > 0 AND sm > 0 AND b"},
	{"(n > 0 OR b) OR sm > 0", "n > 0 OR (b OR sm > 0)"},
}

func oracleEquivalentPair(a, b string) bool {
	for _, p := range oracleEquivalent {
		if p[0] == a && p[1] == b || p[0] == b && p[1] == a {
			return true
		}
	}
	return false
}

// loadOracleEntries stores every input in an index on t and returns what
// PostgreSQL reads back, along with a normalizer for t's columns built from
// format_type, the names db push introspection sees.
func loadOracleEntries(t *testing.T, inputs []string, create func(int, string) string, readExpr string) ([]oracleEntry, indexNormalizer) {
	t.Helper()
	p := openPostgresMigrationDB(t)
	ctx := context.Background()
	conn, err := p.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Timestamp literals are stored in the session time zone.
	if _, err := conn.ExecContext(ctx, "SET TimeZone = 'UTC'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, oracleTableDDL); err != nil {
		t.Fatal(err)
	}
	normalizer := indexNormalizer{enumTypes: map[string]struct{}{"role_t": {}}, columnTypes: map[string]string{}}
	rows, err := conn.QueryContext(ctx, `SELECT attname, format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 't'::regclass AND attnum > 0 AND NOT attisdropped`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatal(err)
		}
		if canonical := canonicalPostgresTypeName(typ); canonical != typ {
			t.Errorf("canonicalPostgresTypeName(%q) = %q; format_type output must be canonical", typ, canonical)
		}
		normalizer.columnTypes[name] = typ
	}
	rows.Close()
	entries := make([]oracleEntry, 0, len(inputs))
	for i, input := range inputs {
		if _, err := conn.ExecContext(ctx, create(i, input)); err != nil {
			t.Fatalf("corpus entry %q is not a valid index definition: %v", input, err)
		}
		var stored sql.NullString
		err := conn.QueryRowContext(ctx, `
SELECT `+readExpr+`
FROM pg_index ix
WHERE ix.indexrelid = to_regclass($1)`, fmt.Sprintf("i%d", i)).Scan(&stored)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, oracleEntry{input: input, stored: stored.String})
	}
	return entries, normalizer
}

func checkOracle(t *testing.T, entries []oracleEntry, normalize func(string) string, unconverged map[string]string) {
	t.Helper()
	for _, e := range entries {
		n := normalize(e.stored)
		if again := normalize(n); again != n {
			t.Errorf("not idempotent for %q:\n  N(stored)    = %q\n  N(N(stored)) = %q", e.input, n, again)
		}
		converged := normalize(e.input) == n
		if _, allowed := unconverged[e.input]; allowed {
			if converged {
				t.Errorf("%q now converges; remove it from the unconverged list", e.input)
			}
			continue
		}
		if !converged {
			t.Errorf("does not converge: %q\n  stored:       %q\n  N(input):     %q\n  N(stored):    %q", e.input, e.stored, normalize(e.input), n)
		}
	}
	for i, a := range entries {
		for _, b := range entries[i+1:] {
			if a.stored == b.stored || oracleEquivalentPair(a.input, b.input) {
				continue
			}
			na, nb := normalize(a.stored), normalize(b.stored)
			if na == nb || normalize(a.input) == nb || na == normalize(b.input) {
				t.Errorf("different definitions normalize alike (a change would be missed):\n  %q stored as %q\n  %q stored as %q\n  normalized: %q",
					a.input, a.stored, b.input, b.stored, na)
			}
		}
	}
	if t.Failed() {
		var stored []string
		for _, e := range entries {
			stored = append(stored, fmt.Sprintf("%-45q -> %q", e.input, e.stored))
		}
		t.Logf("corpus:\n%s", strings.Join(stored, "\n"))
	}
}

// db push derives column types for cast normalization from the schema, while
// PostgreSQL prints them with format_type. They must agree for every field
// kind, or redundant casts stay (needless rebuilds) or the wrong ones go.
func TestPostgresIndexNormalizerColumnTypesMatchFormatType(t *testing.T) {
	p := openPostgresMigrationDB(t)
	schema := p.schema(`
enum Role {
  USER
  ADMIN @map("admin")
}

model Typed {
  id        BigInt   @id
  i         Int
  si        SmallInt
  f         Float
  d         Decimal
  dp        Decimal  @db.Decimal(12, 2)
  s         String
  vc        String   @db.VarChar(40)
  vcn       String   @db.VarChar
  ch        String   @db.Char(4)
  tx        String   @db.Text
  b         Boolean
  dt        DateTime
  ts        DateTime @db.Timestamp(6)
  tz        DateTime @db.Timestamptz(3)
  day       DateTime @db.Date
  tm        DateTime @db.Time(0)
  j         Json
  jb        Json     @db.JsonB
  u         UUID
  su        String   @db.Uuid
  by        Bytes
  role      Role
  roles     Role[]
  tags      String[]
  nums      Int[]
  dbl       Float[]
  small     Int      @db.SmallInt
  big       Int      @db.BigInt
  intType   Int      @db.Int
}
`)
	p.migrate(nil, schema)
	model := schema.Models[0]
	normalizer := newIndexNormalizer(schema, model)
	rows, err := p.db.Query(`SELECT attname, format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = '"Typed"'::regclass AND attnum > 0 AND NOT attisdropped`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, formatted string
		if err := rows.Scan(&name, &formatted); err != nil {
			t.Fatal(err)
		}
		seen++
		if got := normalizer.columnTypes[name]; got != formatted {
			t.Errorf("column %s: normalizer type %q, format_type %q", name, got, formatted)
		}
	}
	if seen != len(model.ScalarFields()) {
		t.Fatalf("checked %d columns, model has %d", seen, len(model.ScalarFields()))
	}
}
