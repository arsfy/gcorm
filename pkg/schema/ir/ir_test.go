package ir

import "testing"

func TestModelTableName(t *testing.T) {
	if got := (&Model{Name: "User"}).TableName(); got != "User" {
		t.Fatalf("TableName() = %q, want %q", got, "User")
	}

	if got := (&Model{Name: "User", DBName: "users"}).TableName(); got != "users" {
		t.Fatalf("TableName() with DBName = %q, want %q", got, "users")
	}
}

func TestModelScalarFields(t *testing.T) {
	id := &Field{Name: "id", Type: FieldKindScalar}
	role := &Field{Name: "role", Type: FieldKindEnum}
	posts := &Field{Name: "posts", Type: FieldKindRelation}
	model := &Model{
		Name:   "User",
		Fields: []*Field{id, role, posts},
	}

	got := model.ScalarFields()
	if len(got) != 2 {
		t.Fatalf("len(ScalarFields()) = %d, want 2", len(got))
	}
	if got[0] != id || got[1] != role {
		t.Fatalf("ScalarFields() = %#v, want scalar and enum fields in order", got)
	}
}
