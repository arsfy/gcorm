package validator

import (
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mkField builds a field declaration with the given name, type and attributes.
func mkField(name, typ string, attrs ...ast.Attribute) ast.FieldDecl {
	return ast.FieldDecl{Name: name, Type: ast.FieldType{Name: typ}, Attributes: attrs}
}

// mkModel builds a model with the given fields and model-level attributes.
func mkModel(name string, fields []ast.FieldDecl, attrs ...ast.Attribute) ast.ModelDecl {
	return ast.ModelDecl{Name: name, Fields: fields, Attributes: attrs}
}

// validationCase is a table entry for schema validation tests.
type validationCase struct {
	name      string
	doc       *ast.Document
	wantErrs  []string
	wantWarns []string
}

// runValidationCases runs table-driven validation tests against Validate.
func runValidationCases(t *testing.T, cases []validationCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Validate(tc.doc)
			if len(tc.wantErrs) == 0 && len(tc.wantWarns) == 0 {
				requireNoErrors(t, r)
				if len(r.Warnings) > 0 {
					t.Fatalf("unexpected warnings: %v", r.Warnings)
				}
				return
			}
			for _, want := range tc.wantErrs {
				requireErrors(t, r, want)
			}
			for _, want := range tc.wantWarns {
				requireWarnings(t, r, want)
			}
		})
	}
}

// idField is a plain integer primary key field.
func idField() ast.FieldDecl {
	return mkField("id", "Int", ast.Attribute{Name: "id"})
}

// userModel returns a User model usable as a relation target.
func userModel() ast.ModelDecl {
	return mkModel("User", []ast.FieldDecl{idField()})
}

// postWithRelation returns a Post model whose user field carries the given
// relation attribute.
func postWithRelation(rel ast.Attribute) ast.ModelDecl {
	return mkModel("Post", []ast.FieldDecl{
		idField(),
		mkField("userId", "Int"),
		{Name: "user", Type: ast.FieldType{Name: "User"}, Attributes: []ast.Attribute{rel}},
	})
}

func relAttr(args ...ast.AttributeArg) ast.Attribute {
	return ast.Attribute{Name: "relation", Args: args}
}

// ---------------------------------------------------------------------------
// ValidationResult API
// ---------------------------------------------------------------------------

func TestValidationResultAPI(t *testing.T) {
	t.Run("error formatting", func(t *testing.T) {
		err := &ValidationError{Message: "boom", Pos: ast.Position{File: "schema.gcorm", Line: 3, Column: 5}}
		if got, want := err.Error(), "schema.gcorm:3:5: error: boom"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("warning formatting", func(t *testing.T) {
		warn := &ValidationError{Message: "meh", Pos: ast.Position{Line: 1, Column: 1}, Severity: SeverityWarning}
		if got, want := warn.Error(), ":1:1: warning: meh"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("AllErrors and AllIssues", func(t *testing.T) {
		r := &ValidationResult{
			Errors:   []*ValidationError{{Message: "e1"}, {Message: "e2"}},
			Warnings: []*ValidationError{{Message: "w1"}},
		}
		if errs := r.AllErrors(); len(errs) != 2 || errs[0].Error() == "" {
			t.Fatalf("AllErrors returned %v", errs)
		}
		if issues := r.AllIssues(); len(issues) != 3 {
			t.Fatalf("AllIssues returned %d issues, want 3", len(issues))
		}
	})

	t.Run("nil document", func(t *testing.T) {
		requireNoErrors(t, Validate(nil))
	})

	t.Run("nil document set", func(t *testing.T) {
		requireErrors(t, ValidateDocumentSet(nil), "no documents to validate")
	})

	t.Run("empty document set", func(t *testing.T) {
		requireErrors(t, ValidateDocumentSet(&ast.DocumentSet{}), "no documents to validate")
	})
}

// ---------------------------------------------------------------------------
// Datasource validation
// ---------------------------------------------------------------------------

func TestDatasourceEdgeCases(t *testing.T) {
	ds := func(entries ...ast.ConfigEntry) *ast.Document {
		return &ast.Document{Datasources: []ast.DatasourceDecl{{Name: "db", Entries: entries}}}
	}
	provider := func(p string) ast.ConfigEntry { return ast.ConfigEntry{Key: "provider", Value: strLit(p)} }
	url := func(v ast.Expression) ast.ConfigEntry { return ast.ConfigEntry{Key: "url", Value: v} }

	runValidationCases(t, []validationCase{
		{
			name: "valid env url",
			doc:  ds(provider("sqlite"), url(funcCall("env", strLit("DATABASE_URL")))),
		},
		{
			name:     "empty url string",
			doc:      ds(provider("sqlite"), url(strLit("  "))),
			wantErrs: []string{"datasource url must not be empty"},
		},
		{
			name:     "url is a number",
			doc:      ds(provider("sqlite"), url(numLit("42"))),
			wantErrs: []string{`datasource url must be a string or env("NAME")`},
		},
		{
			name:     "url with wrong function name",
			doc:      ds(provider("sqlite"), url(funcCall("config", strLit("x")))),
			wantErrs: []string{`datasource url must be a string or env("NAME")`},
		},
		{
			name:     "env with non-string argument",
			doc:      ds(provider("sqlite"), url(funcCall("env", ident("NAME")))),
			wantErrs: []string{"env() requires exactly one non-empty string argument"},
		},
		{
			name:     "env with empty string argument",
			doc:      ds(provider("sqlite"), url(funcCall("env", strLit("  ")))),
			wantErrs: []string{"env() requires exactly one non-empty string argument"},
		},
		{
			name:     "missing provider",
			doc:      ds(url(strLit("file:test.db"))),
			wantErrs: []string{`datasource block requires a "provider" entry`},
		},
		{
			name:     "empty schema string",
			doc:      ds(provider("postgresql"), url(strLit("x")), ast.ConfigEntry{Key: "schema", Value: strLit(" ")}),
			wantErrs: []string{"datasource schema must be a non-empty string"},
		},
		{
			name:     "schema not a string",
			doc:      ds(provider("postgresql"), url(strLit("x")), ast.ConfigEntry{Key: "schema", Value: numLit("1")}),
			wantErrs: []string{"datasource schema must be a non-empty string"},
		},
		{
			name:     "schemas not an array",
			doc:      ds(provider("postgresql"), url(strLit("x")), ast.ConfigEntry{Key: "schemas", Value: strLit("public")}),
			wantErrs: []string{"datasource schemas must be a non-empty string array"},
		},
		{
			name:     "schemas empty array",
			doc:      ds(provider("postgresql"), url(strLit("x")), ast.ConfigEntry{Key: "schemas", Value: arrayLit()}),
			wantErrs: []string{"datasource schemas must be a non-empty string array"},
		},
		{
			name: "schemas with non-string element",
			doc: ds(provider("postgresql"), url(strLit("x")),
				ast.ConfigEntry{Key: "schemas", Value: arrayLit(strLit("public"), numLit("1"))}),
			wantErrs: []string{"datasource schemas values must be non-empty strings"},
		},
		{
			name: "valid schemas",
			doc: ds(provider("postgresql"), url(strLit("x")),
				ast.ConfigEntry{Key: "schemas", Value: arrayLit(strLit("public"), strLit("app"))}),
		},
		{
			name: "schema with mysql provider",
			doc: ds(provider("mysql"), url(strLit("x")),
				ast.ConfigEntry{Key: "schema", Value: strLit("public")}),
			wantErrs: []string{"datasource schema/schemas are only supported by postgresql"},
		},
		{
			name: "schemas with sqlite provider",
			doc: ds(provider("sqlite"), url(strLit("x")),
				ast.ConfigEntry{Key: "schemas", Value: arrayLit(strLit("public"))}),
			wantErrs: []string{"datasource schema/schemas are only supported by postgresql"},
		},
		{
			name: "known optional keys are accepted",
			doc: ds(provider("postgresql"), url(strLit("x")),
				ast.ConfigEntry{Key: "shadowDatabaseUrl", Value: strLit("y")},
				ast.ConfigEntry{Key: "directUrl", Value: strLit("z")},
				ast.ConfigEntry{Key: "relationMode", Value: strLit("prisma")},
				ast.ConfigEntry{Key: "extensions", Value: arrayLit(strLit("pgcrypto"))}),
		},
	})
}

// ---------------------------------------------------------------------------
// Field attribute validation
// ---------------------------------------------------------------------------

func TestFieldAttributeEdgeCases(t *testing.T) {
	modelWithField := func(f ast.FieldDecl) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{idField(), f}),
		}}
	}
	attr := func(name string, args ...ast.AttributeArg) ast.Attribute {
		return ast.Attribute{Name: name, Args: args}
	}

	runValidationCases(t, []validationCase{
		{
			name:     "@id with argument",
			doc:      modelWithField(mkField("code", "Int", attr("id", posArg(numLit("1"))))),
			wantErrs: []string{"@id does not accept arguments", "@id on multiple fields"},
		},
		{
			name:     "@unique with argument",
			doc:      modelWithField(mkField("email", "String", attr("unique", posArg(strLit("x"))))),
			wantErrs: []string{"@unique does not accept arguments"},
		},
		{
			name:     "@map without argument",
			doc:      modelWithField(mkField("email", "String", attr("map"))),
			wantErrs: []string{"@map requires exactly one argument"},
		},
		{
			name:     "@map with two arguments",
			doc:      modelWithField(mkField("email", "String", attr("map", posArg(strLit("a")), posArg(strLit("b"))))),
			wantErrs: []string{"@map requires exactly one argument"},
		},
		{
			name:     "@map with non-string argument",
			doc:      modelWithField(mkField("email", "String", attr("map", posArg(numLit("1"))))),
			wantErrs: []string{"@map argument must be a string"},
		},
		{
			name: "valid @map",
			doc:  modelWithField(mkField("email", "String", attr("map", posArg(strLit("email_address"))))),
		},
		{
			name:     "@updatedAt with argument",
			doc:      modelWithField(mkField("updatedAt", "DateTime", attr("updatedAt", posArg(numLit("1"))))),
			wantErrs: []string{"@updatedAt does not accept arguments"},
		},
		{
			name: "valid @updatedAt",
			doc:  modelWithField(mkField("updatedAt", "DateTime", attr("updatedAt"))),
		},
		{
			name: "valid native type with integer argument",
			doc:  modelWithField(mkField("email", "String", attr("db.VarChar", posArg(numLit("255"))))),
		},
		{
			name: "valid native type without arguments",
			doc:  modelWithField(mkField("payload", "Json", attr("db.JsonB"))),
		},
		{
			name:     "unknown native type",
			doc:      modelWithField(mkField("geo", "String", attr("db.Geometry"))),
			wantErrs: []string{"unknown native type @db.Geometry"},
		},
		{
			name: "native type with float argument",
			doc: modelWithField(mkField("email", "String",
				attr("db.VarChar", posArg(ast.NumberLiteral{Value: "1.5", IsFloat: true})))),
			wantErrs: []string{"native type arguments must be integers"},
		},
		{
			name:     "native type with string argument",
			doc:      modelWithField(mkField("email", "String", attr("db.VarChar", posArg(strLit("255"))))),
			wantErrs: []string{"native type arguments must be integers"},
		},
	})
}

// ---------------------------------------------------------------------------
// @default validation
// ---------------------------------------------------------------------------

func TestDefaultValueEdgeCases(t *testing.T) {
	modelWithDefault := func(typ string, val ast.Expression) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{
				idField(),
				{Name: "value", Type: ast.FieldType{Name: typ}, Attributes: []ast.Attribute{
					{Name: "default", Args: []ast.AttributeArg{posArg(val)}},
				}},
			}),
		}}
	}

	runValidationCases(t, []validationCase{
		{name: "string default", doc: modelWithDefault("String", strLit("hi"))},
		{
			name:     "string field with number default",
			doc:      modelWithDefault("String", numLit("1")),
			wantErrs: []string{"@default value for String field must be a string or function call"},
		},
		{name: "int default", doc: modelWithDefault("Int", numLit("42"))},
		{name: "smallint default", doc: modelWithDefault("SmallInt", numLit("1"))},
		{name: "float default", doc: modelWithDefault("Float", ast.NumberLiteral{Value: "1.5", IsFloat: true})},
		{name: "decimal default", doc: modelWithDefault("Decimal", ast.NumberLiteral{Value: "9.99", IsFloat: true})},
		{
			name:     "int field with string default",
			doc:      modelWithDefault("Int", strLit("42")),
			wantErrs: []string{"@default value for Int field must be a number or function call"},
		},
		{
			name:     "bigint field with boolean default",
			doc:      modelWithDefault("BigInt", boolLit(true)),
			wantErrs: []string{"@default value for BigInt field must be a number or function call"},
		},
		{name: "boolean default", doc: modelWithDefault("Boolean", boolLit(false))},
		{
			name:     "boolean field with string default",
			doc:      modelWithDefault("Boolean", strLit("true")),
			wantErrs: []string{"@default value for Boolean field must be a boolean or function call"},
		},
	})
}

func TestDefaultFunctionEdgeCases(t *testing.T) {
	modelWithDefaultFunc := func(typ string, fc ast.FunctionCall) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{
				idField(),
				{Name: "value", Type: ast.FieldType{Name: typ}, Attributes: []ast.Attribute{
					{Name: "default", Args: []ast.AttributeArg{posArg(fc)}},
				}},
			}),
		}}
	}

	runValidationCases(t, []validationCase{
		{name: "now on DateTime", doc: modelWithDefaultFunc("DateTime", funcCall("now"))},
		{
			name:     "now with argument",
			doc:      modelWithDefaultFunc("DateTime", funcCall("now", numLit("1"))),
			wantErrs: []string{"now() requires 0 argument(s)"},
		},
		{
			name:     "now on String",
			doc:      modelWithDefaultFunc("String", funcCall("now")),
			wantErrs: []string{"now() is only valid for DateTime fields"},
		},
		{name: "autoincrement on Int", doc: modelWithDefaultFunc("Int", funcCall("autoincrement"))},
		{name: "autoincrement on SmallInt", doc: modelWithDefaultFunc("SmallInt", funcCall("autoincrement"))},
		{name: "autoincrement on BigInt", doc: modelWithDefaultFunc("BigInt", funcCall("autoincrement"))},
		{
			name:     "autoincrement on String",
			doc:      modelWithDefaultFunc("String", funcCall("autoincrement")),
			wantErrs: []string{"autoincrement() is only valid for integer fields"},
		},
		{
			name:     "autoincrement with argument",
			doc:      modelWithDefaultFunc("Int", funcCall("autoincrement", numLit("1"))),
			wantErrs: []string{"autoincrement() requires 0 argument(s)"},
		},
		{name: "uuid on UUID", doc: modelWithDefaultFunc("UUID", funcCall("uuid"))},
		{name: "uuid on String", doc: modelWithDefaultFunc("String", funcCall("uuid"))},
		{
			name:     "uuid on Int",
			doc:      modelWithDefaultFunc("Int", funcCall("uuid")),
			wantErrs: []string{"uuid() is only valid for UUID or String fields"},
		},
		{
			name:     "uuid with argument",
			doc:      modelWithDefaultFunc("UUID", funcCall("uuid", numLit("4"))),
			wantErrs: []string{"uuid() requires 0 argument(s)"},
		},
		{name: "cuid on String", doc: modelWithDefaultFunc("String", funcCall("cuid"))},
		{
			name:     "cuid on Int",
			doc:      modelWithDefaultFunc("Int", funcCall("cuid")),
			wantErrs: []string{"cuid() is only valid for String fields"},
		},
		{name: "dbgenerated with string", doc: modelWithDefaultFunc("String", funcCall("dbgenerated", strLit("gen_random_uuid()")))},
		{
			name:     "dbgenerated without argument",
			doc:      modelWithDefaultFunc("String", funcCall("dbgenerated")),
			wantErrs: []string{"dbgenerated() requires 1 argument(s)"},
		},
		{
			name:     "dbgenerated with non-string argument",
			doc:      modelWithDefaultFunc("String", funcCall("dbgenerated", numLit("1"))),
			wantErrs: []string{"dbgenerated() argument must be a string"},
		},
	})
}

func TestListDefaultEdgeCases(t *testing.T) {
	modelWithListDefault := func(typ string, val ast.Expression) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{
				idField(),
				{Name: "items", Type: ast.FieldType{Name: typ, IsList: true}, Attributes: []ast.Attribute{
					{Name: "default", Args: []ast.AttributeArg{posArg(val)}},
				}},
			}),
		}}
	}

	runValidationCases(t, []validationCase{
		{
			name:     "list default not an array",
			doc:      modelWithListDefault("String", strLit("x")),
			wantErrs: []string{"@default value for String[] field must be an array literal or function call"},
		},
		{name: "string list default", doc: modelWithListDefault("String", arrayLit(strLit("a"), strLit("b")))},
		{
			name:     "string list with number element",
			doc:      modelWithListDefault("String", arrayLit(numLit("1"))),
			wantErrs: []string{"@default array values for String[] field must be strings"},
		},
		{
			name:     "uuid list with number element",
			doc:      modelWithListDefault("UUID", arrayLit(numLit("1"))),
			wantErrs: []string{"@default array values for UUID[] field must be strings"},
		},
		{name: "int list default", doc: modelWithListDefault("Int", arrayLit(numLit("1"), numLit("2")))},
		{name: "smallint list default", doc: modelWithListDefault("SmallInt", arrayLit(numLit("1")))},
		{name: "float list default", doc: modelWithListDefault("Float", arrayLit(ast.NumberLiteral{Value: "1.5", IsFloat: true}))},
		{
			name:     "decimal list with string element",
			doc:      modelWithListDefault("Decimal", arrayLit(strLit("9.99"))),
			wantErrs: []string{"@default array values for Decimal[] field must be numbers"},
		},
		{name: "boolean list default", doc: modelWithListDefault("Boolean", arrayLit(boolLit(true)))},
		{
			name:     "boolean list with string element",
			doc:      modelWithListDefault("Boolean", arrayLit(strLit("true"))),
			wantErrs: []string{"@default array values for Boolean[] field must be booleans"},
		},
		{name: "datetime list default", doc: modelWithListDefault("DateTime", arrayLit(strLit("2024-01-01T00:00:00Z")))},
		{
			name:     "datetime list with number element",
			doc:      modelWithListDefault("DateTime", arrayLit(numLit("1"))),
			wantErrs: []string{"@default array values for DateTime[] field must be strings or use a function default"},
		},
	})
}

// ---------------------------------------------------------------------------
// @relation validation
// ---------------------------------------------------------------------------

func TestRelationEdgeCases(t *testing.T) {
	doc := func(rel ast.Attribute) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{userModel(), postWithRelation(rel)}}
	}
	validArgs := []ast.AttributeArg{
		namedArg("fields", arrayLit(ident("userId"))),
		namedArg("references", arrayLit(ident("id"))),
	}

	runValidationCases(t, []validationCase{
		{name: "valid relation", doc: doc(relAttr(validArgs...))},
		{
			name: "valid relation with referential actions",
			doc: doc(relAttr(append(append([]ast.AttributeArg{}, validArgs...),
				namedArg("onDelete", ident("Cascade")),
				namedArg("onUpdate", ident("NoAction")),
				namedArg("name", strLit("PostToUser")))...)),
		},
		{
			name: "duplicate argument",
			doc: doc(relAttr(
				namedArg("fields", arrayLit(ident("userId"))),
				namedArg("fields", arrayLit(ident("userId"))),
				namedArg("references", arrayLit(ident("id"))),
			)),
			wantErrs: []string{"duplicate @relation argument"},
		},
		{
			name:     "fields not an array",
			doc:      doc(relAttr(namedArg("fields", ident("userId")), namedArg("references", arrayLit(ident("id"))))),
			wantErrs: []string{`@relation "fields" argument must be an array`},
		},
		{
			name:     "fields empty array",
			doc:      doc(relAttr(namedArg("fields", arrayLit()), namedArg("references", arrayLit(ident("id"))))),
			wantErrs: []string{"@relation fields must not be empty"},
		},
		{
			name:     "fields non-identifier element",
			doc:      doc(relAttr(namedArg("fields", arrayLit(strLit("userId"))), namedArg("references", arrayLit(ident("id"))))),
			wantErrs: []string{"@relation fields must be identifiers"},
		},
		{
			name: "duplicate field in fields array",
			doc: doc(relAttr(
				namedArg("fields", arrayLit(ident("userId"), ident("userId"))),
				namedArg("references", arrayLit(ident("id"))),
			)),
			wantErrs: []string{`duplicate @relation field "userId"`},
		},
		{
			name: "field not present in model",
			doc: doc(relAttr(
				namedArg("fields", arrayLit(ident("zzz"))),
				namedArg("references", arrayLit(ident("id"))),
			)),
			wantErrs: []string{`@relation field "zzz" does not exist in model "Post"`},
		},
		{
			name:     "references not an array",
			doc:      doc(relAttr(namedArg("fields", arrayLit(ident("userId"))), namedArg("references", ident("id")))),
			wantErrs: []string{`@relation "references" argument must be an array`},
		},
		{
			name:     "references empty array",
			doc:      doc(relAttr(namedArg("fields", arrayLit(ident("userId"))), namedArg("references", arrayLit()))),
			wantErrs: []string{"@relation references must not be empty"},
		},
		{
			name:     "references non-identifier element",
			doc:      doc(relAttr(namedArg("fields", arrayLit(ident("userId"))), namedArg("references", arrayLit(numLit("1"))))),
			wantErrs: []string{"@relation references must be identifiers"},
		},
		{
			name: "invalid onUpdate action",
			doc: doc(relAttr(append(append([]ast.AttributeArg{}, validArgs...),
				namedArg("onUpdate", ident("Explode")))...)),
			wantErrs: []string{`invalid referential action "Explode"`},
		},
		{
			name: "referential action with non-string value",
			doc: doc(relAttr(append(append([]ast.AttributeArg{}, validArgs...),
				namedArg("onDelete", numLit("1")))...)),
			wantErrs: []string{`invalid referential action ""`},
		},
		{
			name: "name not a string",
			doc: doc(relAttr(append(append([]ast.AttributeArg{}, validArgs...),
				namedArg("name", numLit("1")))...)),
			wantErrs: []string{"@relation name must be a string"},
		},
		{
			name: "positional name not a string",
			doc: doc(relAttr(append(append([]ast.AttributeArg{}, validArgs...),
				posArg(numLit("1")))...)),
			wantErrs: []string{"@relation name must be a string"},
		},
		{
			name: "unknown argument",
			doc: doc(relAttr(append(append([]ast.AttributeArg{}, validArgs...),
				namedArg("foo", strLit("x")))...)),
			wantErrs: []string{`unknown @relation argument "foo"`},
		},
	})
}

// ---------------------------------------------------------------------------
// Model-level attribute validation
// ---------------------------------------------------------------------------

func TestModelAttributeEdgeCases(t *testing.T) {
	modelWithAttr := func(attr ast.Attribute) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{
				mkField("a", "Int"),
				mkField("b", "String"),
			}, attr),
		}}
	}
	mattr := func(name string, args ...ast.AttributeArg) ast.Attribute {
		return ast.Attribute{Name: name, IsModelLevel: true, Args: args}
	}

	runValidationCases(t, []validationCase{
		{
			name:     "@@id without arguments",
			doc:      modelWithAttr(mattr("id")),
			wantErrs: []string{"@@id requires a fields array"},
		},
		{
			name:     "@@id fields not an array",
			doc:      modelWithAttr(mattr("id", posArg(ident("a")))),
			wantErrs: []string{"@@id fields argument must be an array"},
		},
		{
			name:     "@@id unknown field",
			doc:      modelWithAttr(mattr("id", posArg(arrayLit(ident("zzz"))))),
			wantErrs: []string{`@@id references unknown field "zzz" in model "User"`},
		},
		{
			name:     "@@id non-identifier field",
			doc:      modelWithAttr(mattr("id", posArg(arrayLit(strLit("a"))))),
			wantErrs: []string{"@@id fields must be identifiers"},
		},
		{
			name:     "@@id duplicate field",
			doc:      modelWithAttr(mattr("id", posArg(arrayLit(ident("a"), ident("a"))))),
			wantErrs: []string{`@@id contains duplicate field "a"`},
		},
		{
			name:     "@@id duplicate argument",
			doc:      modelWithAttr(mattr("id", posArg(arrayLit(ident("a"))), posArg(arrayLit(ident("b"))))),
			wantErrs: []string{`duplicate @@id argument "fields"`},
		},
		{
			name:     "@@id unknown argument",
			doc:      modelWithAttr(mattr("id", posArg(arrayLit(ident("a"))), namedArg("foo", strLit("x")))),
			wantErrs: []string{`unknown @@id argument "foo"`},
		},
		{
			name:     "@@id name not a string",
			doc:      modelWithAttr(mattr("id", posArg(arrayLit(ident("a"))), namedArg("name", numLit("1")))),
			wantErrs: []string{"@@id name must be a string"},
		},
		{
			name: "@@unique with name",
			doc:  modelWithAttr(mattr("unique", posArg(arrayLit(ident("a"))), namedArg("name", strLit("uq_user_a")))),
		},
		{
			name:     "@@index without arguments",
			doc:      modelWithAttr(mattr("index")),
			wantErrs: []string{"@@index requires at least one argument"},
		},
		{
			name:     "@@index fields not an array",
			doc:      modelWithAttr(mattr("index", posArg(ident("a")))),
			wantErrs: []string{"@@index fields argument must be an array"},
		},
		{
			name:     "@@index non-identifier field",
			doc:      modelWithAttr(mattr("index", posArg(arrayLit(numLit("1"))))),
			wantErrs: []string{"@@index fields must be identifiers"},
		},
		{
			name:     "@@index unknown field",
			doc:      modelWithAttr(mattr("index", posArg(arrayLit(ident("zzz"))))),
			wantErrs: []string{`@@index references unknown field "zzz" in model "User"`},
		},
		{
			name:     "@@map without argument",
			doc:      modelWithAttr(mattr("map")),
			wantErrs: []string{"@@map requires exactly one argument"},
		},
		{
			name:     "@@map with non-string argument",
			doc:      modelWithAttr(mattr("map", posArg(numLit("1")))),
			wantErrs: []string{"@@map argument must be a string"},
		},
		{
			name: "valid @@map",
			doc:  modelWithAttr(mattr("map", posArg(strLit("users")))),
		},
		{
			name:     "@@schema without argument",
			doc:      modelWithAttr(mattr("schema")),
			wantErrs: []string{"@@schema requires exactly one argument"},
		},
		{
			name:     "@@schema with non-string argument",
			doc:      modelWithAttr(mattr("schema", posArg(numLit("1")))),
			wantErrs: []string{"@@schema argument must be a non-empty string"},
		},
		{
			name:     "@@schema with empty string",
			doc:      modelWithAttr(mattr("schema", posArg(strLit(" ")))),
			wantErrs: []string{"@@schema argument must be a non-empty string"},
		},
		{
			name: "valid @@schema",
			doc:  modelWithAttr(mattr("schema", posArg(strLit("app")))),
		},
		{
			name:     "unknown model attribute",
			doc:      modelWithAttr(mattr("foo")),
			wantErrs: []string{"unknown model attribute @@foo"},
		},
		{
			name: "@@unique expression without name",
			doc: modelWithAttr(mattr("unique",
				posArg(arrayLit(ident("b"))),
				namedArg("expression", strLit("lower(b)")))),
			wantErrs: []string{"@@unique with expressions requires an explicit non-empty name"},
		},
		{
			name: "@@index expression without name",
			doc: modelWithAttr(mattr("index",
				posArg(arrayLit(ident("b"))),
				namedArg("expression", strLit("lower(b)")))),
			wantErrs: []string{"@@index with expressions requires an explicit non-empty name"},
		},
		{
			name: "@@unique duplicate field allowed when expression is set",
			doc: modelWithAttr(mattr("unique",
				posArg(arrayLit(ident("b"), ident("b"))),
				namedArg("name", strLit("uq_b")),
				namedArg("expression", strLit("lower(b)")))),
		},
	})
}

// ---------------------------------------------------------------------------
// @@index option validation
// ---------------------------------------------------------------------------

func TestIndexOptionEdgeCases(t *testing.T) {
	modelWithIndex := func(args ...ast.AttributeArg) *ast.Document {
		return &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{
				mkField("a", "Int"),
				mkField("b", "String"),
			}, ast.Attribute{Name: "index", IsModelLevel: true, Args: args}),
		}}
	}

	runValidationCases(t, []validationCase{
		{
			name:     "name not a string",
			doc:      modelWithIndex(posArg(arrayLit(ident("a"))), namedArg("name", numLit("1"))),
			wantErrs: []string{"@@index name must be a string"},
		},
		{
			name:     "where not a string",
			doc:      modelWithIndex(posArg(arrayLit(ident("a"))), namedArg("where", numLit("1"))),
			wantErrs: []string{"@@index where must be a string"},
		},
		{
			name:     "nulls invalid value",
			doc:      modelWithIndex(posArg(arrayLit(ident("a"))), namedArg("nulls", ident("Middle"))),
			wantErrs: []string{"@@index nulls values must be First or Last"},
		},
		{
			name: "nulls count mismatch",
			doc: modelWithIndex(posArg(arrayLit(ident("a"), ident("b"))),
				namedArg("nulls", arrayLit(ident("First"), ident("Last"), ident("First")))),
			wantErrs: []string{"@@index nulls option has 3 value(s), expected 1 or 2"},
		},
		{
			name: "opclass count mismatch",
			doc: modelWithIndex(posArg(arrayLit(ident("a"), ident("b"))),
				namedArg("opclass", arrayLit(strLit("int4_ops"), strLit("text_ops"), strLit("int4_ops")))),
			wantErrs: []string{"@@index opclass option has 3 value(s), expected 1 or 2"},
		},
		{
			name: "single opclass applies to all fields",
			doc: modelWithIndex(posArg(arrayLit(ident("a"), ident("b"))),
				namedArg("ops", strLit("int4_ops"))),
		},
		{
			name: "order alias for sort",
			doc:  modelWithIndex(posArg(arrayLit(ident("a"))), namedArg("order", ident("Desc"))),
		},
		{
			name: "empty option values are skipped",
			doc: modelWithIndex(posArg(arrayLit(ident("a"), ident("b"))),
				namedArg("sort", arrayLit(ident("Asc"), numLit("1")))),
		},
		{
			name: "collation alias",
			doc:  modelWithIndex(posArg(arrayLit(ident("b"))), namedArg("collation", strLit("C"))),
		},
		{
			name:      "unknown index argument warns",
			doc:       modelWithIndex(posArg(arrayLit(ident("a"))), namedArg("foo", strLit("x"))),
			wantWarns: []string{`unknown @@index argument "foo"`},
		},
		{
			name: "expression array count mismatch on single field",
			doc: modelWithIndex(posArg(arrayLit(ident("b"))),
				namedArg("name", strLit("idx_b")),
				namedArg("expression", arrayLit(strLit("lower(b)"), strLit("upper(b)")))),
			wantErrs: []string{"@@index expression option has 2 value(s), expected 1 or 1"},
		},
		{
			name: "expressions plural matching field count",
			doc: modelWithIndex(posArg(arrayLit(ident("a"), ident("b"))),
				namedArg("name", strLit("idx_ab")),
				namedArg("expressions", arrayLit(strLit(""), strLit("lower(b)")))),
		},
		{
			name: "single expression repeated across fields",
			doc: modelWithIndex(posArg(arrayLit(ident("a"), ident("b"))),
				namedArg("name", strLit("idx_ab")),
				namedArg("expression", strLit("lower(b)"))),
		},
	})
}

// ---------------------------------------------------------------------------
// Enum validation
// ---------------------------------------------------------------------------

func TestEnumEdgeCases(t *testing.T) {
	enumWith := func(name string, values []ast.EnumValue, attrs ...ast.Attribute) *ast.Document {
		return &ast.Document{Enums: []ast.EnumDecl{
			{Name: name, Values: values, Attributes: attrs},
		}}
	}
	mattr := func(name string, args ...ast.AttributeArg) ast.Attribute {
		return ast.Attribute{Name: name, IsModelLevel: true, Args: args}
	}

	runValidationCases(t, []validationCase{
		{
			name: "duplicate enum names",
			doc: &ast.Document{Enums: []ast.EnumDecl{
				{Name: "Role", Values: []ast.EnumValue{{Name: "ADMIN"}}},
				{Name: "Role", Values: []ast.EnumValue{{Name: "USER"}}},
			}},
			wantErrs: []string{`duplicate enum name "Role"`},
		},
		{
			name: "enum @@map",
			doc:  enumWith("Role", []ast.EnumValue{{Name: "ADMIN"}}, mattr("map", posArg(strLit("roles")))),
		},
		{
			name:     "enum @@map without argument",
			doc:      enumWith("Role", []ast.EnumValue{{Name: "ADMIN"}}, mattr("map")),
			wantErrs: []string{"@@map requires exactly one argument"},
		},
		{
			name:     "enum @@map with non-string argument",
			doc:      enumWith("Role", []ast.EnumValue{{Name: "ADMIN"}}, mattr("map", posArg(numLit("1")))),
			wantErrs: []string{"@@map argument must be a string"},
		},
		{
			name:     "unknown enum attribute",
			doc:      enumWith("Role", []ast.EnumValue{{Name: "ADMIN"}}, mattr("index")),
			wantErrs: []string{"unknown enum attribute @@index"},
		},
		{
			name: "enum value @map",
			doc: enumWith("Role", []ast.EnumValue{
				{Name: "ADMIN", Attributes: []ast.Attribute{{Name: "map", Args: []ast.AttributeArg{posArg(strLit("admin"))}}}},
			}),
		},
		{
			name: "unknown enum value attribute",
			doc: enumWith("Role", []ast.EnumValue{
				{Name: "ADMIN", Attributes: []ast.Attribute{{Name: "foo"}}},
			}),
			wantErrs: []string{"unknown enum value attribute @foo"},
		},
		{
			name: "enum value @map without argument",
			doc: enumWith("Role", []ast.EnumValue{
				{Name: "ADMIN", Attributes: []ast.Attribute{{Name: "map"}}},
			}),
			wantErrs: []string{"@map requires exactly one argument"},
		},
		{
			name: "enum value @map with non-string argument",
			doc: enumWith("Role", []ast.EnumValue{
				{Name: "ADMIN", Attributes: []ast.Attribute{{Name: "map", Args: []ast.AttributeArg{posArg(numLit("1"))}}}},
			}),
			wantErrs: []string{"@map argument must be a string"},
		},
		{
			name:      "lowercase enum value warns",
			doc:       enumWith("Role", []ast.EnumValue{{Name: "admin"}}),
			wantWarns: []string{`enum value "admin" should be all uppercase`},
		},
		{
			name: "enum used as field type",
			doc: &ast.Document{
				Enums: []ast.EnumDecl{{Name: "Role", Values: []ast.EnumValue{{Name: "ADMIN"}}}},
				Models: []ast.ModelDecl{
					mkModel("User", []ast.FieldDecl{idField(), mkField("role", "Role")}),
				},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// Model structure validation
// ---------------------------------------------------------------------------

func TestModelStructureEdgeCases(t *testing.T) {
	runValidationCases(t, []validationCase{
		{
			name:     "model without fields",
			doc:      &ast.Document{Models: []ast.ModelDecl{{Name: "Empty"}}},
			wantErrs: []string{"model Empty has no fields"},
		},
		{
			name: "field-level @id combined with @@id",
			doc: &ast.Document{Models: []ast.ModelDecl{
				mkModel("User", []ast.FieldDecl{idField()},
					ast.Attribute{Name: "id", IsModelLevel: true, Args: []ast.AttributeArg{
						posArg(arrayLit(ident("id"))),
					}}),
			}},
			wantErrs: []string{`model "User" has both field-level @id and model-level @@id`},
		},
	})
}

// ---------------------------------------------------------------------------
// Cross-reference validation
// ---------------------------------------------------------------------------

func TestCrossReferenceEdgeCases(t *testing.T) {
	t.Run("references unknown field on target model", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			userModel(),
			postWithRelation(relAttr(
				namedArg("fields", arrayLit(ident("userId"))),
				namedArg("references", arrayLit(ident("zzz"))),
			)),
		}}
		r := Validate(doc)
		requireErrors(t, r, `@relation references unknown field "zzz" on model "User"`)
	})

	t.Run("field type mismatch", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			userModel(),
			mkModel("Post", []ast.FieldDecl{
				idField(),
				mkField("userId", "String"),
				{Name: "user", Type: ast.FieldType{Name: "User"}, Attributes: []ast.Attribute{relAttr(
					namedArg("fields", arrayLit(ident("userId"))),
					namedArg("references", arrayLit(ident("id"))),
				)}},
			}),
		}}
		requireErrors(t, Validate(doc), `type String does not match referenced field "id" type Int`)
	})

	t.Run("list field type mismatch", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			userModel(),
			mkModel("Post", []ast.FieldDecl{
				idField(),
				{Name: "userIds", Type: ast.FieldType{Name: "Int", IsList: true}},
				{Name: "user", Type: ast.FieldType{Name: "User"}, Attributes: []ast.Attribute{relAttr(
					namedArg("fields", arrayLit(ident("userIds"))),
					namedArg("references", arrayLit(ident("id"))),
				)}},
			}),
		}}
		requireErrors(t, Validate(doc), "does not match referenced field")
	})

	t.Run("onDelete SetNull requires optional fields", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			userModel(),
			postWithRelation(relAttr(
				namedArg("fields", arrayLit(ident("userId"))),
				namedArg("references", arrayLit(ident("id"))),
				namedArg("onDelete", ident("SetNull")),
			)),
		}}
		requireErrors(t, Validate(doc), "onDelete: SetNull requires optional relation fields")
	})

	t.Run("onDelete SetNull with optional field", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			userModel(),
			mkModel("Post", []ast.FieldDecl{
				idField(),
				{Name: "userId", Type: ast.FieldType{Name: "Int", IsOptional: true}},
				{Name: "user", Type: ast.FieldType{Name: "User"}, Attributes: []ast.Attribute{relAttr(
					namedArg("fields", arrayLit(ident("userId"))),
					namedArg("references", arrayLit(ident("id"))),
					namedArg("onDelete", ident("SET_NULL")),
				)}},
			}),
		}}
		requireNoErrors(t, Validate(doc))
	})

	t.Run("references without unique constraint", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{idField(), mkField("name", "String")}),
			mkModel("Post", []ast.FieldDecl{
				idField(),
				mkField("userName", "String"),
				{Name: "user", Type: ast.FieldType{Name: "User"}, Attributes: []ast.Attribute{relAttr(
					namedArg("fields", arrayLit(ident("userName"))),
					namedArg("references", arrayLit(ident("name"))),
				)}},
			}),
		}}
		requireErrors(t, Validate(doc), `@relation references on model "User" must match an id or unique constraint`)
	})

	t.Run("references matching composite @@unique", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{
				idField(),
				mkField("firstName", "String"),
				mkField("lastName", "String"),
			}, ast.Attribute{Name: "unique", IsModelLevel: true, Args: []ast.AttributeArg{
				posArg(arrayLit(ident("firstName"), ident("lastName"))),
			}}),
			mkModel("Post", []ast.FieldDecl{
				idField(),
				mkField("firstName", "String"),
				mkField("lastName", "String"),
				{Name: "user", Type: ast.FieldType{Name: "User"}, Attributes: []ast.Attribute{relAttr(
					namedArg("fields", arrayLit(ident("firstName"), ident("lastName"))),
					namedArg("references", arrayLit(ident("firstName"), ident("lastName"))),
				)}},
			}),
		}}
		requireNoErrors(t, Validate(doc))
	})

	t.Run("fields and references length mismatch", func(t *testing.T) {
		doc := &ast.Document{Models: []ast.ModelDecl{
			userModel(),
			postWithRelation(relAttr(
				namedArg("fields", arrayLit(ident("userId"))),
				namedArg("references", arrayLit(ident("id"), ident("id"))),
			)),
		}}
		requireErrors(t, Validate(doc), "must have the same length")
	})
}

// ---------------------------------------------------------------------------
// Expression index provider gating
// ---------------------------------------------------------------------------

func TestExpressionIndexProviderGating(t *testing.T) {
	docWith := func(provider string, attr ast.Attribute) *ast.Document {
		doc := &ast.Document{Models: []ast.ModelDecl{
			mkModel("User", []ast.FieldDecl{mkField("email", "String")}, attr),
		}}
		if provider != "" {
			doc.Datasources = []ast.DatasourceDecl{{Name: "db", Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit(provider)},
				{Key: "url", Value: strLit("x")},
			}}}
		}
		return doc
	}
	exprIndex := ast.Attribute{Name: "index", IsModelLevel: true, Args: []ast.AttributeArg{
		posArg(arrayLit(ident("email"))),
		namedArg("name", strLit("idx_email")),
		namedArg("expression", strLit("lower(email)")),
	}}
	exprUnique := ast.Attribute{Name: "unique", IsModelLevel: true, Args: []ast.AttributeArg{
		posArg(arrayLit(ident("email"))),
		namedArg("name", strLit("uq_email")),
		namedArg("expression", strLit("lower(email)")),
	}}

	runValidationCases(t, []validationCase{
		{
			name:     "mysql rejects @@index expression",
			doc:      docWith("mysql", exprIndex),
			wantErrs: []string{"@@index expressions are only supported by postgresql"},
		},
		{
			name:     "sqlite rejects @@unique expression",
			doc:      docWith("sqlite", exprUnique),
			wantErrs: []string{"@@unique expressions are only supported by postgresql"},
		},
		{name: "postgresql allows expression index", doc: docWith("postgresql", exprIndex)},
		{name: "no datasource allows expression index", doc: docWith("", exprIndex)},
	})
}

// ---------------------------------------------------------------------------
// DocumentSet cross-document validation
// ---------------------------------------------------------------------------

func TestValidateDocumentSetDuplicates(t *testing.T) {
	t.Run("duplicate model across documents", func(t *testing.T) {
		r := ValidateDocumentSet(&ast.DocumentSet{Documents: []*ast.Document{
			{Models: []ast.ModelDecl{mkModel("User", []ast.FieldDecl{idField()})}},
			{Models: []ast.ModelDecl{mkModel("User", []ast.FieldDecl{idField()})}},
		}})
		requireErrors(t, r, `duplicate model name "User"`)
	})

	t.Run("duplicate enum across documents", func(t *testing.T) {
		r := ValidateDocumentSet(&ast.DocumentSet{Documents: []*ast.Document{
			{Enums: []ast.EnumDecl{{Name: "Role", Values: []ast.EnumValue{{Name: "ADMIN"}}}}},
			{Enums: []ast.EnumDecl{{Name: "Role", Values: []ast.EnumValue{{Name: "USER"}}}}},
		}})
		requireErrors(t, r, `duplicate enum name "Role"`)
	})

	t.Run("datasource in multiple documents", func(t *testing.T) {
		r := ValidateDocumentSet(&ast.DocumentSet{Documents: []*ast.Document{
			{Datasources: []ast.DatasourceDecl{validDatasource()}},
			{Datasources: []ast.DatasourceDecl{validDatasource()}},
		}})
		requireErrors(t, r, "multiple datasource blocks are not allowed")
	})
}
