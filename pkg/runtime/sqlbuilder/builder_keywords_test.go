package sqlbuilder

import (
	"errors"
	"reflect"
	"testing"
)

func TestPostgresRewriteNonReservedKeywordsAsColumns(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		args      []any
		want      string
	}{
		{"rows column", "rows ? 'k' AND id = ?", []any{7}, "rows ? 'k' AND id = $1"},
		{"range column", "range ?| array['a'] AND id = ?", []any{7}, "range ?| array['a'] AND id = $1"},
		{"partition column", "partition ? 'p' AND id = ?", []any{7}, "partition ? 'p' AND id = $1"},
		{"first column", "first ? 'k' AND id = ?", []any{7}, "first ? 'k' AND id = $1"},
		{"next column", "next ? 'k' AND id = ?", []any{7}, "next ? 'k' AND id = $1"},
		{"zone column", "zone ? 'k' AND id = ?", []any{7}, "zone ? 'k' AND id = $1"},
		{"qualified rows column", "t.rows ? 'k' AND id = ?", []any{7}, "t.rows ? 'k' AND id = $1"},
		{"parenthesized rows column", "(rows ? 'k') AND id = ?", []any{7}, "(rows ? 'k') AND id = $1"},
		{"operator before column named rows", "data ? rows AND id = ?", []any{7}, "data ? rows AND id = $1"},
		{"fetch first param", "EXISTS (SELECT 1 FROM c FETCH FIRST ? ROWS ONLY) AND id = ?", []any{1, 7}, "EXISTS (SELECT 1 FROM c FETCH FIRST $1 ROWS ONLY) AND id = $2"},
		{"fetch next param", "EXISTS (SELECT 1 FROM c OFFSET ? ROWS FETCH NEXT ? ROWS ONLY)", []any{1, 2}, "EXISTS (SELECT 1 FROM c OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY)"},
		{"at time zone param", "created_at AT TIME ZONE ? > ?", []any{"UTC", "2020-01-01"}, "created_at AT TIME ZONE $1 > $2"},
		{"window rows frame", "x > (SELECT sum(v) OVER (ORDER BY id ROWS ? PRECEDING) FROM c LIMIT 1)", []any{3}, "x > (SELECT sum(v) OVER (ORDER BY id ROWS $1 PRECEDING) FROM c LIMIT 1)"},
		{"window range frame after over paren", "x > (SELECT sum(v) OVER (RANGE ? PRECEDING) FROM c LIMIT 1)", []any{3}, "x > (SELECT sum(v) OVER (RANGE $1 PRECEDING) FROM c LIMIT 1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New("postgresql").Select("*").From("t").Where(tt.condition, tt.args...)
			got, gotArgs := b.Build()
			if want := "SELECT * FROM t WHERE " + tt.want; got != want {
				t.Fatalf("SQL = %q, want %q", got, want)
			}
			if !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.args)
			}
			if err := b.Err(); err != nil {
				t.Fatalf("Err() = %v", err)
			}
		})
	}
}

func TestPostgresRewriteDollarInIdentifiers(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		args      []any
		want      string
	}{
		{"dollar inside identifier", "a$b$ = ? AND c$ = ?", []any{1, 2}, "a$b$ = $1 AND c$ = $2"},
		{"dollar identifier before json operator", "data$ ? 'k' AND id = ?", []any{1}, "data$ ? 'k' AND id = $1"},
		{"digit tag is not a dollar quote", "$1$ = ? AND $2 = ?", []any{1, 2}, "$1$ = $1 AND $2 = $2"},
		{"real dollar quote", "body = $q$ a ? b $q$ AND id = ?", []any{1}, "body = $q$ a ? b $q$ AND id = $1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New("postgresql").Select("*").From("t").Where(tt.condition, tt.args...)
			got, _ := b.Build()
			if want := "SELECT * FROM t WHERE " + tt.want; got != want {
				t.Fatalf("SQL = %q, want %q", got, want)
			}
			if err := b.Err(); err != nil {
				t.Fatalf("Err() = %v", err)
			}
		})
	}
}

func TestBuilderErrPlaceholderMismatch(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			ok := New(dialect).Select("*").From("t").Where("a = ? AND note = 'x?' -- ?", 1)
			if err := ok.Err(); err != nil {
				t.Fatalf("matching placeholders: Err() = %v", err)
			}

			tooFewArgs := New(dialect).Select("*").From("t").Where("a = ? AND b = ?", 1)
			if err := tooFewArgs.Err(); !errors.Is(err, ErrPlaceholderMismatch) {
				t.Fatalf("too few args: Err() = %v", err)
			}

			tooManyArgs := New(dialect).Select("*").From("t").Where("a = ?", 1, 2)
			if err := tooManyArgs.Err(); !errors.Is(err, ErrPlaceholderMismatch) {
				t.Fatalf("too many args: Err() = %v", err)
			}
		})
	}
}

func TestBuilderErrPlaceholderEdgeCases(t *testing.T) {
	// PostgreSQL: a placeholder without any argument is a mismatch, while a
	// jsonb ? operator without arguments is not.
	if err := New("postgresql").Select("*").From("t").Where("a = ?").Err(); !errors.Is(err, ErrPlaceholderMismatch) {
		t.Fatalf("postgres placeholder without args: Err() = %v", err)
	}
	op := New("postgresql").Select("*").From("t").Where("data ? 'k'")
	if err := op.Err(); err != nil {
		t.Fatalf("postgres ? operator without args: Err() = %v", err)
	}
	if sql, _ := op.Build(); sql != "SELECT * FROM t WHERE data ? 'k'" {
		t.Fatalf("operator rewritten: %s", sql)
	}

	// SQLite numbered and named parameters cannot be counted: no false error.
	for _, cond := range []string{"a = ?1 OR b = ?1", "a = :name", "a = @name", "a = $name"} {
		if err := New("sqlite").Select("*").From("t").Where(cond, 1).Err(); err != nil {
			t.Fatalf("sqlite %q: Err() = %v", cond, err)
		}
	}
	// MySQL user variables are not parameters; plain ? are still counted.
	if err := New("mysql").Select("*").From("t").Where("a = @v AND b = ?", 1).Err(); err != nil {
		t.Fatalf("mysql user variable: Err() = %v", err)
	}
}

func TestPostgresNumberingStaysAlignedAfterMismatch(t *testing.T) {
	b := New("postgresql").Select("*").From("t").
		Where("a = ?", 1, 2).
		And("b = ?", 3)
	got, args := b.Build()
	if want := "SELECT * FROM t WHERE a = $1 AND b = $3"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if len(args) != 3 {
		t.Fatalf("args = %#v", args)
	}
	if !errors.Is(b.Err(), ErrPlaceholderMismatch) {
		t.Fatalf("Err() = %v", b.Err())
	}
}
