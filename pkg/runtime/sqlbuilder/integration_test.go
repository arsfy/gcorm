package sqlbuilder

import (
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 1. Complete SELECT with WHERE, ORDER BY, LIMIT, OFFSET for each dialect
// ---------------------------------------------------------------------------

func TestIntegration_SelectFullQuery(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"SELECT id, name, email FROM users WHERE active = $1 ORDER BY name ASC LIMIT 10 OFFSET 20",
		},
		{
			"mysql",
			"SELECT id, name, email FROM users WHERE active = ? ORDER BY name ASC LIMIT 10 OFFSET 20",
		},
		{
			"sqlite",
			"SELECT id, name, email FROM users WHERE active = ? ORDER BY name ASC LIMIT 10 OFFSET 20",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Select("id", "name", "email").
				From("users").
				Where("active = ?", true).
				OrderBy("name ASC").
				Limit(10).
				Offset(20).
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, []any{true}) {
				t.Errorf("args = %v, want [true]", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. INSERT INTO ... VALUES for each dialect
// ---------------------------------------------------------------------------

func TestIntegration_InsertValues(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"INSERT INTO products (name, price, stock) VALUES ($1, $2, $3)",
		},
		{
			"mysql",
			"INSERT INTO products (name, price, stock) VALUES (?, ?, ?)",
		},
		{
			"sqlite",
			"INSERT INTO products (name, price, stock) VALUES (?, ?, ?)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Insert("products").
				Columns("name", "price", "stock").
				Values("Widget", 9.99, 100).
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			wantArgs := []any{"Widget", 9.99, 100}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. UPDATE ... SET ... WHERE for each dialect
// ---------------------------------------------------------------------------

func TestIntegration_UpdateSetWhere(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"UPDATE orders SET status = $1, updated_at = $2 WHERE id = $3",
		},
		{
			"mysql",
			"UPDATE orders SET status = ?, updated_at = ? WHERE id = ?",
		},
		{
			"sqlite",
			"UPDATE orders SET status = ?, updated_at = ? WHERE id = ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Update("orders").
				Set("status", "shipped").
				Set("updated_at", "2024-06-01").
				Where("id = ?", 42).
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			wantArgs := []any{"shipped", "2024-06-01", 42}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. DELETE FROM ... WHERE for each dialect
// ---------------------------------------------------------------------------

func TestIntegration_DeleteWhere(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"DELETE FROM sessions WHERE expired_at < $1",
		},
		{
			"mysql",
			"DELETE FROM sessions WHERE expired_at < ?",
		},
		{
			"sqlite",
			"DELETE FROM sessions WHERE expired_at < ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Delete("sessions").
				Where("expired_at < ?", "2024-01-01").
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			wantArgs := []any{"2024-01-01"}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. SELECT with JOIN for each dialect
// ---------------------------------------------------------------------------

func TestIntegration_SelectWithJoin(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"SELECT u.id, u.name, o.total FROM users u INNER JOIN orders o ON u.id = o.user_id WHERE o.total > $1",
		},
		{
			"mysql",
			"SELECT u.id, u.name, o.total FROM users u INNER JOIN orders o ON u.id = o.user_id WHERE o.total > ?",
		},
		{
			"sqlite",
			"SELECT u.id, u.name, o.total FROM users u INNER JOIN orders o ON u.id = o.user_id WHERE o.total > ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Select("u.id", "u.name", "o.total").
				From("users u").
				Join("INNER", "orders o", "u.id = o.user_id").
				Where("o.total > ?", 50).
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			wantArgs := []any{50}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. SELECT with GROUP BY and HAVING
// ---------------------------------------------------------------------------

func TestIntegration_GroupByHaving(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"SELECT department, AVG(salary) AS avg_salary FROM employees GROUP BY department HAVING AVG(salary) > $1",
		},
		{
			"mysql",
			"SELECT department, AVG(salary) AS avg_salary FROM employees GROUP BY department HAVING AVG(salary) > ?",
		},
		{
			"sqlite",
			"SELECT department, AVG(salary) AS avg_salary FROM employees GROUP BY department HAVING AVG(salary) > ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Select("department", "AVG(salary) AS avg_salary").
				From("employees").
				GroupBy("department").
				Having("AVG(salary) > ?", 50000).
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			wantArgs := []any{50000}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 7. INSERT with RETURNING for postgresql
// ---------------------------------------------------------------------------

func TestIntegration_InsertReturningPostgresql(t *testing.T) {
	q, args := New("postgresql").
		Insert("users").
		Columns("name", "email").
		Values("Alice", "alice@example.com").
		Returning("id", "created_at").
		Build()

	wantSQL := "INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id, created_at"
	if q != wantSQL {
		t.Errorf("SQL = %q\nwant %q", q, wantSQL)
	}
	wantArgs := []any{"Alice", "alice@example.com"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// 8. Placeholder numbering: postgresql uses $1, $2, $3...
// ---------------------------------------------------------------------------

func TestIntegration_PostgresqlPlaceholderNumbering(t *testing.T) {
	q, args := New("postgresql").
		Select("*").
		From("users").
		Where("age > ?", 18).
		And("name LIKE ?", "%alice%").
		And("active = ?", true).
		Build()

	wantSQL := "SELECT * FROM users WHERE age > $1 AND name LIKE $2 AND active = $3"
	if q != wantSQL {
		t.Errorf("SQL = %q\nwant %q", q, wantSQL)
	}
	wantArgs := []any{18, "%alice%", true}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}

	// Verify sequential $N tokens
	if !strings.Contains(q, "$1") || !strings.Contains(q, "$2") || !strings.Contains(q, "$3") {
		t.Errorf("expected $1, $2, $3 in query: %s", q)
	}
}

// ---------------------------------------------------------------------------
// 9. Placeholder style: mysql/sqlite use ?, ?, ?...
// ---------------------------------------------------------------------------

func TestIntegration_QuestionMarkPlaceholders(t *testing.T) {
	for _, dialect := range []string{"mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			q, args := New(dialect).
				Select("*").
				From("users").
				Where("age > ?", 18).
				And("name LIKE ?", "%bob%").
				And("active = ?", true).
				Build()

			wantSQL := "SELECT * FROM users WHERE age > ? AND name LIKE ? AND active = ?"
			if q != wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, wantSQL)
			}
			wantArgs := []any{18, "%bob%", true}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}

			// Ensure no $N-style placeholders
			if strings.Contains(q, "$") {
				t.Errorf("%s query should not contain $-style placeholders: %s", dialect, q)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 10. Complex query: SELECT with multiple WHERE conditions (AND/OR)
// ---------------------------------------------------------------------------

func TestIntegration_ComplexWhereConditions(t *testing.T) {
	cases := []struct {
		dialect string
		wantSQL string
	}{
		{
			"postgresql",
			"SELECT * FROM products WHERE category = $1 AND price >= $2 AND price <= $3 OR featured = $4",
		},
		{
			"mysql",
			"SELECT * FROM products WHERE category = ? AND price >= ? AND price <= ? OR featured = ?",
		},
		{
			"sqlite",
			"SELECT * FROM products WHERE category = ? AND price >= ? AND price <= ? OR featured = ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			q, args := New(tc.dialect).
				Select("*").
				From("products").
				Where("category = ?", "electronics").
				And("price >= ?", 100).
				And("price <= ?", 500).
				Or("featured = ?", true).
				Build()

			if q != tc.wantSQL {
				t.Errorf("SQL = %q\nwant %q", q, tc.wantSQL)
			}
			wantArgs := []any{"electronics", 100, 500, true}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("args = %v, want %v", args, wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 11. Args collected in correct order
// ---------------------------------------------------------------------------

func TestIntegration_ArgsOrder(t *testing.T) {
	_, args := New("postgresql").
		Update("users").
		Set("name", "first").
		Set("email", "second").
		Set("age", "third").
		Where("id = ?", "fourth").
		And("active = ?", "fifth").
		Build()

	wantArgs := []any{"first", "second", "third", "fourth", "fifth"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args order = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// 12. Empty builder produces valid (if minimal) output
// ---------------------------------------------------------------------------

func TestIntegration_EmptyBuilder(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			q, args := New(dialect).Build()
			if q != "" {
				t.Errorf("empty builder: SQL = %q, want empty string", q)
			}
			if args != nil {
				t.Errorf("empty builder: args = %v, want nil", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Bonus: Multi-value INSERT with correct placeholder count
// ---------------------------------------------------------------------------

func TestIntegration_InsertMultipleColumns(t *testing.T) {
	q, args := New("postgresql").
		Insert("events").
		Columns("type", "payload", "user_id", "created_at").
		Values("click", `{"x":1}`, 42, "2024-06-01").
		Build()

	wantSQL := "INSERT INTO events (type, payload, user_id, created_at) VALUES ($1, $2, $3, $4)"
	if q != wantSQL {
		t.Errorf("SQL = %q\nwant %q", q, wantSQL)
	}
	wantArgs := []any{"click", `{"x":1}`, 42, "2024-06-01"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

// ---------------------------------------------------------------------------
// Bonus: LEFT JOIN combined with GROUP BY, HAVING, ORDER BY
// ---------------------------------------------------------------------------

func TestIntegration_JoinGroupByHavingOrderBy(t *testing.T) {
	q, args := New("postgresql").
		Select("u.name", "COUNT(o.id) AS order_count").
		From("users u").
		Join("LEFT", "orders o", "u.id = o.user_id").
		GroupBy("u.name").
		Having("COUNT(o.id) > ?", 3).
		OrderBy("order_count DESC").
		Limit(5).
		Build()

	wantSQL := "SELECT u.name, COUNT(o.id) AS order_count FROM users u LEFT JOIN orders o ON u.id = o.user_id GROUP BY u.name HAVING COUNT(o.id) > $1 ORDER BY order_count DESC LIMIT 5"
	if q != wantSQL {
		t.Errorf("SQL = %q\nwant %q", q, wantSQL)
	}
	wantArgs := []any{3}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}
