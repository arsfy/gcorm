package validator

import (
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/parser"
)

func validateSource(t *testing.T, src string) *ValidationResult {
	t.Helper()
	doc, err := parser.Parse("schema.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Validate(doc)
}

func datasourceBlock(provider, extra string) string {
	return "datasource db {\n  provider = \"" + provider + "\"\n  url = \"x\"\n" + extra + "}\n"
}

func TestValidateNativeTypePerProvider(t *testing.T) {
	tests := []struct {
		provider string
		field    string
		wantErr  string
	}{
		{"postgresql", "n Int @db.TinyInt", "@db.TinyInt is not supported by provider postgresql"},
		{"postgresql", "b Bytes @db.Blob", "@db.Blob is not supported by provider postgresql"},
		{"postgresql", "n Int @db.UnsignedInt", "@db.UnsignedInt is not supported by provider postgresql"},
		{"postgresql", "n BigInt @db.UnsignedBigInt", "@db.UnsignedBigInt is not supported by provider postgresql"},
		{"postgresql", "b Bytes @db.Bytes", "@db.Bytes is not supported by provider postgresql"},
		{"mysql", "j Json @db.JsonB", "@db.JsonB is not supported by provider mysql"},
		{"mysql", "u String @db.Uuid", "@db.Uuid is not supported by provider mysql"},
		{"mysql", "d DateTime @db.Timestamptz", "@db.Timestamptz is not supported by provider mysql"},
		{"mysql", "s String @db.VarChar", "@db.VarChar requires a length argument on mysql"},
		{"postgresql", "s String @db.VarChar(10, 2)", "@db.VarChar accepts at most 1 argument(s)"},
		{"postgresql", "s String @db.Text(10)", "@db.Text accepts at most 0 argument(s)"},
	}
	for _, tc := range tests {
		t.Run(tc.provider+" "+tc.field, func(t *testing.T) {
			r := validateSource(t, datasourceBlock(tc.provider, "")+"model M {\n  id Int @id\n  "+tc.field+"\n}\n")
			requireErrors(t, r, tc.wantErr)
		})
	}
}

func TestValidateNativeTypeAgainstScalar(t *testing.T) {
	tests := []struct {
		field   string
		wantErr string
	}{
		{"n Int @db.Uuid", "@db.Uuid is not compatible with field type Int"},
		{"n Int @db.VarChar(20)", "@db.VarChar is not compatible with field type Int"},
		{"s String @db.Decimal(10, 2)", "@db.Decimal is not compatible with field type String"},
		{"s String @db.Timestamp(6)", "@db.Timestamp is not compatible with field type String"},
		{"d DateTime @db.Int", "@db.Int is not compatible with field type DateTime"},
		{"s String @db.JsonB", "@db.JsonB is not compatible with field type String"},
		{"r Role @db.Text", "@db.Text is not compatible with field type Role"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			r := validateSource(t, datasourceBlock("postgresql", "")+"enum Role {\n  A\n}\nmodel M {\n  id Int @id\n  "+tc.field+"\n}\n")
			requireErrors(t, r, tc.wantErr)
		})
	}
}

func TestValidateNativeTypeAcceptsSupportedCombinations(t *testing.T) {
	tests := []struct {
		provider string
		fields   string
	}{
		{"postgresql", `
  a String @db.VarChar(20)
  b String @db.Char(2)
  c String @db.Text
  d Decimal @db.Decimal(10, 2)
  e Json @db.JsonB
  f Json @db.Json
  g String @db.Uuid
  h UUID @db.Uuid
  i DateTime @db.Timestamp(6)
  j DateTime @db.Timestamptz(3)
  k DateTime @db.Date
  l DateTime @db.Time
  m Int @db.SmallInt
  n BigInt @db.BigInt
  o Int @db.Int`},
		{"mysql", `
  a String @db.VarChar(191)
  b Bytes @db.Blob
  c Boolean @db.TinyInt(1)
  d Int @db.UnsignedInt
  e BigInt @db.UnsignedBigInt
  f DateTime @db.Timestamp(3)
  g Json @db.Json`},
		{"sqlite", `
  a Bytes @db.Bytes
  b Bytes @db.Blob
  c String @db.Text`},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			r := validateSource(t, datasourceBlock(tc.provider, "")+"model M {\n  id Int @id"+tc.fields+"\n}\n")
			requireNoErrors(t, r)
		})
	}
}

func TestValidateModelSchemaProviderAndDatasourceSchemas(t *testing.T) {
	model := "model M {\n  id Int @id\n  @@schema(\"audit\")\n}\n"

	requireErrors(t, validateSource(t, datasourceBlock("mysql", "")+model), "@@schema is only supported by postgresql")
	requireErrors(t, validateSource(t, datasourceBlock("sqlite", "")+model), "@@schema is only supported by postgresql")
	requireErrors(t, validateSource(t, datasourceBlock("postgresql", "  schemas = [\"public\", \"app\"]\n")+model),
		`@@schema("audit") is not listed in datasource schemas [public, app]`)

	requireNoErrors(t, validateSource(t, datasourceBlock("postgresql", "")+model))
	requireNoErrors(t, validateSource(t, datasourceBlock("postgresql", "  schemas = [\"public\", \"audit\"]\n")+model))
	// Without a datasource the provider is unknown; @@schema stays accepted.
	requireNoErrors(t, validateSource(t, model))
}

func TestValidateDBGeneratedArguments(t *testing.T) {
	base := datasourceBlock("postgresql", "")
	requireErrors(t, validateSource(t, base+"model M {\n  id Int @id\n  s String @default(dbgenerated())\n}\n"), "dbgenerated() requires 1 argument(s)")
	requireErrors(t, validateSource(t, base+"model M {\n  id Int @id\n  s String @default(dbgenerated(\"a\", \"b\"))\n}\n"), "dbgenerated() requires 1 argument(s)")
	requireErrors(t, validateSource(t, base+"model M {\n  id Int @id\n  s String @default(dbgenerated(1))\n}\n"), "dbgenerated() argument must be a string")
	requireErrors(t, validateSource(t, base+"model M {\n  id Int @id\n  s String @default(dbgenerated(\"  \"))\n}\n"), "dbgenerated() argument must be a non-empty SQL expression")
	requireNoErrors(t, validateSource(t, base+"model M {\n  id Int @id\n  s String @default(dbgenerated(\"gen_random_uuid()::text\"))\n}\n"))
}
