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
	}
	for _, tc := range cases {
		got := QuoteIdent(tc.dialect, tc.parts...)
		if got != tc.want {
			t.Errorf("QuoteIdent(%q, %v) = %q, want %q", tc.dialect, tc.parts, got, tc.want)
		}
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
