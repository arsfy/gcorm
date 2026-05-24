package sqlbuilder

import "testing"

func BenchmarkSelectWherePostgres(b *testing.B) {
	for i := 0; i < b.N; i++ {
		q, args := New("postgresql").
			Select("id", "name", "email").
			From("users").
			Where("active = ? AND tenant_id = ?", true, "tenant-1").
			OrderBy("created_at DESC").
			Limit(50).
			Offset(100).
			Build()
		if q == "" || len(args) != 2 {
			b.Fatal("unexpected empty query")
		}
	}
}

func BenchmarkSelectWhereMySQL(b *testing.B) {
	for i := 0; i < b.N; i++ {
		q, args := New("mysql").
			Select("id", "name", "email").
			From("users").
			Where("active = ? AND tenant_id = ?", true, "tenant-1").
			OrderBy("created_at DESC").
			Limit(50).
			Offset(100).
			Build()
		if q == "" || len(args) != 2 {
			b.Fatal("unexpected empty query")
		}
	}
}

func BenchmarkComplexWherePostgres(b *testing.B) {
	for i := 0; i < b.N; i++ {
		q, args := New("postgresql").
			Select("*").
			From("events").
			Where("tenant_id = ?", "tenant-1").
			And("status IN (?, ?, ?)", "open", "pending", "closed").
			And("created_at >= ? AND created_at < ?", "2026-01-01", "2026-02-01").
			Or("priority = ?", "high").
			OrderBy("created_at DESC, id DESC").
			Limit(100).
			Build()
		if q == "" || len(args) != 7 {
			b.Fatal("unexpected query")
		}
	}
}

func BenchmarkInsertValuesPostgres(b *testing.B) {
	for i := 0; i < b.N; i++ {
		q, args := New("postgresql").
			Insert("users").
			Columns("id", "email", "name", "created_at").
			Values("u1", "user@example.com", "User Name", "2026-01-01T00:00:00Z").
			Build()
		if q == "" || len(args) != 4 {
			b.Fatal("unexpected query")
		}
	}
}
