package dialect

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/runtime"
	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// contractDialects returns all dialect instances for contract testing.
func contractDialects() map[string]runtime.Dialect {
	return map[string]runtime.Dialect{
		"postgresql": PostgreSQL{},
		"mysql":      MySQL{},
		"sqlite":     SQLite{},
	}
}

// ---------------------------------------------------------------------------
// 1. All dialects return non-empty Name()
// ---------------------------------------------------------------------------

func TestContract_NonEmptyName(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			if d.Name() == "" {
				t.Errorf("%s: Name() must not be empty", label)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. All dialects return non-empty Placeholder(1)
// ---------------------------------------------------------------------------

func TestContract_NonEmptyPlaceholder(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			ph := d.Placeholder(1)
			if ph == "" {
				t.Errorf("%s: Placeholder(1) must not be empty", label)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. PostgreSQL uses $N, MySQL/SQLite use ?
// ---------------------------------------------------------------------------

func TestContract_PlaceholderStyle(t *testing.T) {
	pg := contractDialects()["postgresql"]
	if got := pg.Placeholder(1); got != "$1" {
		t.Errorf("postgresql: Placeholder(1) = %q, want $1", got)
	}
	if got := pg.Placeholder(7); got != "$7" {
		t.Errorf("postgresql: Placeholder(7) = %q, want $7", got)
	}

	for _, name := range []string{"mysql", "sqlite"} {
		d := contractDialects()[name]
		for _, n := range []int{1, 5, 100} {
			if got := d.Placeholder(n); got != "?" {
				t.Errorf("%s: Placeholder(%d) = %q, want ?", name, n, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 4. QuoteIdent with single part returns properly quoted identifier
// ---------------------------------------------------------------------------

func TestContract_QuoteIdentSingle(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			got := d.QuoteIdent("users")
			if got == "" || got == "users" {
				t.Errorf("%s: QuoteIdent(\"users\") = %q, expected quoted form", label, got)
			}
			if !strings.Contains(got, "users") {
				t.Errorf("%s: QuoteIdent(\"users\") = %q, should contain 'users'", label, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. QuoteIdent with multiple parts returns properly quoted multi-part identifier
// ---------------------------------------------------------------------------

func TestContract_QuoteIdentMultiPart(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			got := d.QuoteIdent("schema", "table")
			if !strings.Contains(got, ".") {
				t.Errorf("%s: QuoteIdent(\"schema\", \"table\") = %q, expected dot-separated parts", label, got)
			}
			if !strings.Contains(got, "schema") || !strings.Contains(got, "table") {
				t.Errorf("%s: QuoteIdent result %q should contain both 'schema' and 'table'", label, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. TypeMapping handles all schema types
// ---------------------------------------------------------------------------

func TestContract_TypeMappingAllTypes(t *testing.T) {
	schemaTypes := []string{
		"String", "Int", "SmallInt", "BigInt", "Float", "Decimal",
		"Boolean", "DateTime", "Bytes", "Json", "UUID",
	}

	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			for _, st := range schemaTypes {
				got := d.TypeMapping(st, false)
				if got == "" {
					t.Errorf("%s: TypeMapping(%q, false) returned empty string", label, st)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 7. All dialects return non-empty TypeMapping results
// ---------------------------------------------------------------------------

func TestContract_TypeMappingOptionalNonEmpty(t *testing.T) {
	schemaTypes := []string{
		"String", "Int", "SmallInt", "BigInt", "Float", "Decimal",
		"Boolean", "DateTime", "Bytes", "Json", "UUID",
	}

	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			for _, st := range schemaTypes {
				got := d.TypeMapping(st, true)
				if got == "" {
					t.Errorf("%s: TypeMapping(%q, true) returned empty string", label, st)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 8. SupportsReturning: postgresql=true, mysql=false, sqlite=false
// ---------------------------------------------------------------------------

func TestContract_SupportsReturning(t *testing.T) {
	expected := map[string]bool{
		"postgresql": true,
		"mysql":      false,
		"sqlite":     false,
	}
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			if got := d.SupportsReturning(); got != expected[label] {
				t.Errorf("%s: SupportsReturning() = %v, want %v", label, got, expected[label])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 9. SupportsSchemas: postgresql=true, mysql=true, sqlite=false
// ---------------------------------------------------------------------------

func TestContract_SupportsSchemas(t *testing.T) {
	expected := map[string]bool{
		"postgresql": true,
		"mysql":      true,
		"sqlite":     false,
	}
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			if got := d.SupportsSchemas(); got != expected[label] {
				t.Errorf("%s: SupportsSchemas() = %v, want %v", label, got, expected[label])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 10. DefaultValueExpression for "uuid", "now", "autoincrement" returns non-empty
// ---------------------------------------------------------------------------

func TestContract_DefaultValueExpression(t *testing.T) {
	funcs := []string{"uuid", "now", "autoincrement"}

	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			for _, fn := range funcs {
				got := d.DefaultValueExpression(fn, nil)
				if got == "" {
					t.Errorf("%s: DefaultValueExpression(%q, nil) returned empty string", label, fn)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 11. ClassifyError(nil) does not panic and returns nil
// ---------------------------------------------------------------------------

func TestContract_ClassifyErrorNil(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			got := d.ClassifyError(nil)
			if got != nil {
				t.Errorf("%s: ClassifyError(nil) = %+v, want nil", label, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 13. ClassifyError(sql.ErrNoRows) returns CodeNotFound for all dialects
// ---------------------------------------------------------------------------

func TestContract_ClassifyErrorNotFound(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			got := d.ClassifyError(sql.ErrNoRows)
			if got == nil {
				t.Fatalf("%s: ClassifyError(sql.ErrNoRows) = nil, want NotFound", label)
			}
			if got.Code != gcoerr.CodeNotFound {
				t.Errorf("%s: ClassifyError(sql.ErrNoRows).Code = %v, want CodeNotFound", label, got.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 14. ClassifyError with unrecognised error returns nil for all dialects
// ---------------------------------------------------------------------------

func TestContract_ClassifyErrorUnknown(t *testing.T) {
	randomErr := fmt.Errorf("some completely random error xyz")
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			got := d.ClassifyError(randomErr)
			if got != nil {
				t.Errorf("%s: ClassifyError(random) = %+v, want nil", label, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 12. RewriteLimitOffset produces valid SQL fragments
// ---------------------------------------------------------------------------

func TestContract_RewriteLimitOffset(t *testing.T) {
	intPtr := func(n int) *int { return &n }

	cases := []struct {
		name   string
		limit  *int
		offset *int
	}{
		{"nil/nil", nil, nil},
		{"limit_only", intPtr(10), nil},
		{"offset_only", nil, intPtr(5)},
		{"both", intPtr(10), intPtr(5)},
		{"zero_limit", intPtr(0), nil},
		{"zero_offset", intPtr(10), intPtr(0)},
		{"both_zero", intPtr(0), intPtr(0)},
	}

	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					got := d.RewriteLimitOffset(tc.limit, tc.offset)

					// nil/nil should produce empty string
					if tc.limit == nil && tc.offset == nil {
						if got != "" {
							t.Errorf("%s: RewriteLimitOffset(nil, nil) = %q, want empty", label, got)
						}
						return
					}

					// Any non-nil arg should produce non-empty output
					if got == "" {
						t.Errorf("%s: RewriteLimitOffset(%v, %v) returned empty string", label, tc.limit, tc.offset)
						return
					}

					// LIMIT keyword present when limit is set
					if tc.limit != nil && !strings.Contains(got, "LIMIT") {
						t.Errorf("%s: result %q should contain LIMIT", label, got)
					}

					// OFFSET keyword present when offset is set
					if tc.offset != nil && !strings.Contains(got, "OFFSET") {
						t.Errorf("%s: result %q should contain OFFSET", label, got)
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SupportsFeature consistency: matches SupportsReturning / SupportsSchemas
// ---------------------------------------------------------------------------

func TestContract_SupportsFeatureConsistency(t *testing.T) {
	for label, d := range contractDialects() {
		t.Run(label, func(t *testing.T) {
			if d.SupportsFeature(runtime.FeatureReturning) != d.SupportsReturning() {
				t.Errorf("%s: SupportsFeature(FeatureReturning) != SupportsReturning()", label)
			}
			if d.SupportsFeature(runtime.FeatureSchemas) != d.SupportsSchemas() {
				t.Errorf("%s: SupportsFeature(FeatureSchemas) != SupportsSchemas()", label)
			}
		})
	}
}
