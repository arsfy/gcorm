package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arsfy/gco-orm/pkg/schema/ir"
)

// schemaWithUser returns a minimal schema containing a single User model.
func schemaWithUser() *ir.Schema {
	return &ir.Schema{
		Models: []*ir.Model{
			{
				Name:   "User",
				DBName: "users",
				Fields: []*ir.Field{
					{Name: "id", Type: ir.FieldKindScalar, ScalarType: "Int", IsID: true},
					{Name: "email", Type: ir.FieldKindScalar, ScalarType: "String", IsUnique: true},
					{Name: "name", Type: ir.FieldKindScalar, ScalarType: "String"},
				},
				PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
			},
		},
	}
}

// schemaWithUserAndBio extends schemaWithUser with an optional bio column.
func schemaWithUserAndBio() *ir.Schema {
	s := schemaWithUser()
	s.Models[0].Fields = append(s.Models[0].Fields, &ir.Field{
		Name:       "bio",
		Type:       ir.FieldKindScalar,
		ScalarType: "String",
		IsOptional: true,
	})
	return s
}

func TestMigrationSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		old     *ir.Schema
		new     *ir.Schema
		dialect string
	}{
		{
			name:    "create_model",
			old:     &ir.Schema{},
			new:     schemaWithUser(),
			dialect: "postgresql",
		},
		{
			name:    "add_field",
			old:     schemaWithUser(),
			new:     schemaWithUserAndBio(),
			dialect: "postgresql",
		},
		{
			name:    "drop_model",
			old:     schemaWithUser(),
			new:     &ir.Schema{},
			dialect: "postgresql",
		},
		{
			name:    "create_model_mysql",
			old:     &ir.Schema{},
			new:     schemaWithUser(),
			dialect: "mysql",
		},
		{
			name:    "create_model_sqlite",
			old:     &ir.Schema{},
			new:     schemaWithUser(),
			dialect: "sqlite",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := Diff(tt.old, tt.new)
			gen := DDLGenerator{Dialect: tt.dialect, Schema: tt.new}
			upSQL := gen.GenerateUp(cs)
			downSQL := gen.GenerateDown(cs)

			goldenDir := filepath.Join("testdata", "snapshots")
			if err := os.MkdirAll(goldenDir, 0755); err != nil {
				t.Fatal(err)
			}

			goldenUp := filepath.Join(goldenDir, tt.name+"_up.sql")
			goldenDown := filepath.Join(goldenDir, tt.name+"_down.sql")

			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(goldenUp, []byte(upSQL), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenDown, []byte(downSQL), 0644); err != nil {
					t.Fatal(err)
				}
				t.Logf("Updated golden files for %s", tt.name)
				return
			}

			// Auto-create golden files on first run.
			if _, err := os.Stat(goldenUp); os.IsNotExist(err) {
				if err := os.WriteFile(goldenUp, []byte(upSQL), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenDown, []byte(downSQL), 0644); err != nil {
					t.Fatal(err)
				}
				t.Logf("Created golden files for %s", tt.name)
				return
			}

			expectedUp, err := os.ReadFile(goldenUp)
			if err != nil {
				t.Fatal(err)
			}
			expectedDown, err := os.ReadFile(goldenDown)
			if err != nil {
				t.Fatal(err)
			}

			if upSQL != string(expectedUp) {
				t.Errorf("up.sql mismatch for %s:\ngot:\n%s\nwant:\n%s", tt.name, upSQL, string(expectedUp))
			}
			if downSQL != string(expectedDown) {
				t.Errorf("down.sql mismatch for %s:\ngot:\n%s\nwant:\n%s", tt.name, downSQL, string(expectedDown))
			}
		})
	}
}
