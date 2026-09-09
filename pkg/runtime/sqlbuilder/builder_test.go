package sqlbuilder

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// Simple SELECT with WHERE
// ---------------------------------------------------------------------------

func TestSelectWhere(t *testing.T) {
	q, args := New("postgresql").
		Select("id", "name").
		From("users").
		Where("id = ?", 1).
		Build()

	wantSQL := "SELECT id, name FROM users WHERE id = $1"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{1}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// SELECT with JOIN, ORDER BY, LIMIT, OFFSET
// ---------------------------------------------------------------------------

func TestSelectJoinOrderLimitOffset(t *testing.T) {
	q, args := New("postgresql").
		Select("u.id", "o.total").
		From("users u").
		Join("LEFT", "orders o", "u.id = o.user_id").
		Where("u.active = ?", true).
		OrderBy("o.total DESC").
		Limit(10).
		Offset(20).
		Build()

	wantSQL := "SELECT u.id, o.total FROM users u LEFT JOIN orders o ON u.id = o.user_id WHERE u.active = $1 ORDER BY o.total DESC LIMIT 10 OFFSET 20"
	if q != wantSQL {
		t.Errorf("SQL = %q\nwant %q", q, wantSQL)
	}
	wantArgs := []any{true}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// INSERT with VALUES and RETURNING
// ---------------------------------------------------------------------------

func TestInsertReturning(t *testing.T) {
	q, args := New("postgresql").
		Insert("users").
		Columns("name", "email").
		Values("Alice", "alice@example.com").
		Returning("id").
		Build()

	wantSQL := "INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{"Alice", "alice@example.com"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// UPDATE with SET and WHERE
// ---------------------------------------------------------------------------

func TestUpdateSetWhere(t *testing.T) {
	q, args := New("postgresql").
		Update("users").
		Set("name", "Bob").
		Set("email", "bob@example.com").
		Where("id = ?", 42).
		Build()

	wantSQL := "UPDATE users SET name = $1, email = $2 WHERE id = $3"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{"Bob", "bob@example.com", 42}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// DELETE with WHERE
// ---------------------------------------------------------------------------

func TestDeleteWhere(t *testing.T) {
	q, args := New("postgresql").
		Delete("sessions").
		Where("expired_at < ?", "2024-01-01").
		Build()

	wantSQL := "DELETE FROM sessions WHERE expired_at < $1"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{"2024-01-01"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// PostgreSQL numbered placeholders ($1, $2, ...)
// ---------------------------------------------------------------------------

func TestPostgresPlaceholders(t *testing.T) {
	q, args := New("postgresql").
		Select("*").
		From("t").
		Where("a = ? AND b = ?", 1, 2).
		And("c = ?", 3).
		Build()

	wantSQL := "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{1, 2, 3}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

func TestPostgresPlaceholderRewriteSkipsSQLTokens(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		args      []any
		want      string
	}{
		{
			name:      "single quoted string",
			condition: "note = 'it''s ?' AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE note = 'it''s ?' AND id = $1",
		},
		{
			name:      "standard string trailing backslash",
			condition: `path = 'C:\' AND id = ?`,
			args:      []any{7},
			want:      `SELECT * FROM t WHERE path = 'C:\' AND id = $1`,
		},
		{
			name:      "standard string doubled backslash",
			condition: `path = 'C:\\' AND id = ?`,
			args:      []any{7},
			want:      `SELECT * FROM t WHERE path = 'C:\\' AND id = $1`,
		},
		{
			name:      "escape string escaped quote",
			condition: `note = E'it\'s ?' AND id = ?`,
			args:      []any{7},
			want:      `SELECT * FROM t WHERE note = E'it\'s ?' AND id = $1`,
		},
		{
			name:      "double quoted identifier",
			condition: `"question?column" = ?`,
			args:      []any{"value"},
			want:      `SELECT * FROM t WHERE "question?column" = $1`,
		},
		{
			name:      "dollar quoted string",
			condition: "body = $tag$?$tag$ AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE body = $tag$?$tag$ AND id = $1",
		},
		{
			name:      "line and nested block comments",
			condition: "id = ? /* outer ? /* inner ? */ outer ? */ -- line ?\nAND active = true",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE id = $1 /* outer ? /* inner ? */ outer ? */ -- line ?\nAND active = true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotArgs := New("postgresql").
				Select("*").
				From("t").
				Where(tt.condition, tt.args...).
				Build()
			if got != tt.want {
				t.Fatalf("SQL = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.args)
			}
		})
	}
}

func TestPostgresPlaceholderRewriteAfterExpressionKeywords(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		args      []any
		want      string
	}{
		{
			name:      "is distinct from",
			condition: "a IS DISTINCT FROM ? AND b = ?",
			args:      []any{1, 2},
			want:      "SELECT * FROM t WHERE a IS DISTINCT FROM $1 AND b = $2",
		},
		{
			name:      "fetch first",
			condition: "EXISTS (SELECT 1 FROM child FETCH FIRST ? ROWS ONLY) AND id = ?",
			args:      []any{5, 7},
			want:      "SELECT * FROM t WHERE EXISTS (SELECT 1 FROM child FETCH FIRST $1 ROWS ONLY) AND id = $2",
		},
		{
			name:      "select expression",
			condition: "EXISTS (SELECT ? AS value) AND id = ?",
			args:      []any{true, 7},
			want:      "SELECT * FROM t WHERE EXISTS (SELECT $1 AS value) AND id = $2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotArgs := New("postgresql").
				Select("*").
				From("t").
				Where(tt.condition, tt.args...).
				Build()
			if got != tt.want {
				t.Fatalf("SQL = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.args)
			}
		})
	}
}

func TestPostgresPlaceholderRewritePreservesJSONOperators(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		args      []any
		want      string
	}{
		{
			name:      "existence with literal key",
			condition: "attrs ? 'admin' AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs ? 'admin' AND id = $1",
		},
		{
			name:      "existence with parameter key",
			condition: "attrs ? ? AND id = ?",
			args:      []any{"admin", 7},
			want:      "SELECT * FROM t WHERE attrs ? $1 AND id = $2",
		},
		{
			name:      "any keys",
			condition: "attrs ?| ARRAY[?] AND id = ?",
			args:      []any{"admin", 7},
			want:      "SELECT * FROM t WHERE attrs ?| ARRAY[$1] AND id = $2",
		},
		{
			name:      "all keys",
			condition: "attrs ?& ARRAY[?, ?] AND id = ?",
			args:      []any{"admin", "owner", 7},
			want:      "SELECT * FROM t WHERE attrs ?& ARRAY[$1, $2] AND id = $3",
		},
		{
			name:      "json path",
			condition: "attrs @? '$.admin' AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs @? '$.admin' AND id = $1",
		},
		{
			name:      "custom hash operator",
			condition: "attrs ?# key AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs ?# key AND id = $1",
		},
		{
			name:      "custom prefixed question operator",
			condition: "attrs #? key AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs #? key AND id = $1",
		},
		{
			name:      "custom minus operator",
			condition: "attrs ?- key AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs ?- key AND id = $1",
		},
		{
			name:      "custom multi operator",
			condition: "attrs ?-| key AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs ?-| key AND id = $1",
		},
		{
			name:      "operators with comment lookaround",
			condition: "attrs /* before ? */ ?# /* after ? */ key AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE attrs /* before ? */ ?# /* after ? */ key AND id = $1",
		},
		{
			name:      "bare operator and parameter with comment lookaround",
			condition: "attrs /* before ? */ ? /* after ? */ ? AND id = ?",
			args:      []any{"admin", 7},
			want:      "SELECT * FROM t WHERE attrs /* before ? */ ? /* after ? */ $1 AND id = $2",
		},
		{
			name:      "parameter followed by subtraction without whitespace",
			condition: "id = ?-1 AND other_id = ?",
			args:      []any{8, 9},
			want:      "SELECT * FROM t WHERE id = $1-1 AND other_id = $2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotArgs := New("postgresql").
				Select("*").
				From("t").
				Where(tt.condition, tt.args...).
				Build()
			if got != tt.want {
				t.Fatalf("SQL = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.args)
			}
		})
	}
}

func TestPostgresPlaceholderRewritePreservesQuantifiedOperators(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		args      []any
		want      string
	}{
		{
			name:      "question operator with ALL array",
			condition: "data ? ALL(ARRAY['a','b']) AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE data ? ALL(ARRAY['a','b']) AND id = $1",
		},
		{
			name:      "question operator with SOME subquery",
			condition: "data ? SOME (SELECT v FROM vals) AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE data ? SOME (SELECT v FROM vals) AND id = $1",
		},
		{
			name:      "question operator with ANY array",
			condition: "data ? ANY(ARRAY['a','b']) AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE data ? ANY(ARRAY['a','b']) AND id = $1",
		},
		{
			name:      "question operator with lowercase quantifier",
			condition: "data ? all (array['a']) AND id = ?",
			args:      []any{7},
			want:      "SELECT * FROM t WHERE data ? all (array['a']) AND id = $1",
		},
		{
			name:      "quantified operator mixed with inner parameter",
			condition: "data ? ANY(ARRAY[?]) AND id = ?",
			args:      []any{"a", 7},
			want:      "SELECT * FROM t WHERE data ? ANY(ARRAY[$1]) AND id = $2",
		},
		{
			name:      "quantified operators mixed with existence operator",
			condition: "attrs ? 'key' AND data ? ALL(ARRAY[?]) AND id = ?",
			args:      []any{"a", 7},
			want:      "SELECT * FROM t WHERE attrs ? 'key' AND data ? ALL(ARRAY[$1]) AND id = $2",
		},
		{
			name:      "parameter as left operand of ANY comparison",
			condition: "? = ANY(tags) AND id = ?",
			args:      []any{"a", 7},
			want:      "SELECT * FROM t WHERE $1 = ANY(tags) AND id = $2",
		},
		{
			name:      "parameter inside ANY array expression",
			condition: "id = ANY(?) AND other_id = ?",
			args:      []any{7, 9},
			want:      "SELECT * FROM t WHERE id = ANY($1) AND other_id = $2",
		},
		{
			name:      "quantifier word without parens stays parameter",
			condition: "data = ? AND all = ?",
			args:      []any{1, 2},
			want:      "SELECT * FROM t WHERE data = $1 AND all = $2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotArgs := New("postgresql").
				Select("*").
				From("t").
				Where(tt.condition, tt.args...).
				Build()
			if got != tt.want {
				t.Fatalf("SQL = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MySQL placeholders (?)
// ---------------------------------------------------------------------------

func TestMySQLPlaceholders(t *testing.T) {
	q, args := New("mysql").
		Select("*").
		From("t").
		Where("a = ? AND b = ?", 1, 2).
		And("c = ?", 3).
		Build()

	wantSQL := "SELECT * FROM t WHERE a = ? AND b = ? AND c = ?"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{1, 2, 3}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// SQLite placeholders (?)
// ---------------------------------------------------------------------------

func TestSQLitePlaceholders(t *testing.T) {
	q, args := New("sqlite").
		Select("id").
		From("items").
		Where("name = ?", "widget").
		Build()

	wantSQL := "SELECT id FROM items WHERE name = ?"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{"widget"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// QuoteIdent — all three dialects
// ---------------------------------------------------------------------------

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		dialect string
		parts   []string
		want    string
	}{
		{"postgresql", []string{"users"}, `"users"`},
		{"postgresql", []string{"public", "users"}, `"public"."users"`},
		{"mysql", []string{"users"}, "`users`"},
		{"mysql", []string{"mydb", "users"}, "`mydb`.`users`"},
		{"sqlite", []string{"users"}, `"users"`},
		{"sqlite", []string{"main", "users"}, `"main"."users"`},
		{"postgresql", []string{`weird"name`}, `"weird""name"`},
		{"mysql", []string{"weird`name"}, "`weird``name`"},
		{"sqlite", []string{`weird"name`}, `"weird""name"`},
	}
	for _, tc := range cases {
		got := QuoteIdent(tc.dialect, tc.parts...)
		if got != tc.want {
			t.Errorf("QuoteIdent(%q, %v) = %q, want %q", tc.dialect, tc.parts, got, tc.want)
		}
	}
}

func TestOffsetOnlyByDialect(t *testing.T) {
	tests := []struct {
		dialect string
		want    string
	}{
		{"postgresql", "SELECT * FROM t OFFSET 20"},
		{"mysql", "SELECT * FROM t LIMIT 18446744073709551615 OFFSET 20"},
		{"sqlite", "SELECT * FROM t LIMIT -1 OFFSET 20"},
	}

	for _, tt := range tests {
		t.Run(tt.dialect, func(t *testing.T) {
			got, _ := New(tt.dialect).Select("*").From("t").Offset(20).Build()
			if got != tt.want {
				t.Fatalf("SQL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLimitOffsetOrderIsCanonical(t *testing.T) {
	got, _ := New("sqlite").Select("*").From("t").Offset(20).Limit(10).Build()
	want := "SELECT * FROM t LIMIT 10 OFFSET 20"
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Complex query with multiple conditions (AND + OR)
// ---------------------------------------------------------------------------

func TestComplexConditions(t *testing.T) {
	q, args := New("postgresql").
		Select("*").
		From("products").
		Where("category = ?", "electronics").
		And("price > ?", 100).
		Or("featured = ?", true).
		Build()

	wantSQL := "SELECT * FROM products WHERE category = $1 AND price > $2 OR featured = $3"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{"electronics", 100, true}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// GroupBy and Having
// ---------------------------------------------------------------------------

func TestGroupByHaving(t *testing.T) {
	q, args := New("postgresql").
		Select("category", "COUNT(*) AS cnt").
		From("products").
		GroupBy("category").
		Having("COUNT(*) > ?", 5).
		Build()

	wantSQL := "SELECT category, COUNT(*) AS cnt FROM products GROUP BY category HAVING COUNT(*) > $1"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{5}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// MySQL INSERT (no RETURNING, ? placeholders)
// ---------------------------------------------------------------------------

func TestMySQLInsert(t *testing.T) {
	q, args := New("mysql").
		Insert("users").
		Columns("name", "email").
		Values("Alice", "a@b.com").
		Build()

	wantSQL := "INSERT INTO users (name, email) VALUES (?, ?)"
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	wantArgs := []any{"Alice", "a@b.com"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// Placeholder method
// ---------------------------------------------------------------------------

func TestPlaceholderMethod(t *testing.T) {
	b := New("postgresql")
	if p := b.Placeholder(); p != "$1" {
		t.Errorf("first placeholder = %q, want $1", p)
	}
	if p := b.Placeholder(); p != "$2" {
		t.Errorf("second placeholder = %q, want $2", p)
	}

	b2 := New("mysql")
	if p := b2.Placeholder(); p != "?" {
		t.Errorf("mysql placeholder = %q, want ?", p)
	}
}

// ---------------------------------------------------------------------------
// Empty builder
// ---------------------------------------------------------------------------

func TestEmptyBuilder(t *testing.T) {
	q, args := New("postgresql").Build()
	if q != "" {
		t.Errorf("empty Build() SQL = %q, want empty", q)
	}
	if args != nil {
		t.Errorf("empty Build() args = %v, want nil", args)
	}
}
