package golang

import (
	"regexp"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func generateFeatureFiles(t *testing.T, provider string) []*GeneratedFile {
	t.Helper()
	files, err := NewGenerator(featureSchema(provider), WithModulePath("example.com/gentest/gen")).Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	return files
}

func TestEnumConstantsUseMappedDBValue(t *testing.T) {
	enums := fileContent(t, generateFeatureFiles(t, "postgresql"), "model/enums.go")
	if !regexp.MustCompile(`RoleADMIN\s+Role = "admin"`).MatchString(enums) {
		t.Errorf("RoleADMIN should hold the @map value \"admin\":\n%s", enums)
	}
	if !regexp.MustCompile(`RoleUSER\s+Role = "USER"`).MatchString(enums) {
		t.Errorf("RoleUSER should fall back to the member name:\n%s", enums)
	}
}

func TestEnumDBValue(t *testing.T) {
	if got := enumDBValue(&ir.EnumValue{Name: "ADMIN", DBName: "admin"}); got != "admin" {
		t.Errorf("enumDBValue(mapped) = %q", got)
	}
	if got := enumDBValue(&ir.EnumValue{Name: "USER"}); got != "USER" {
		t.Errorf("enumDBValue(unmapped) = %q", got)
	}
}

func TestCreateInputUsesPointersForDatabaseDefaults(t *testing.T) {
	q := fileContent(t, generateFeatureFiles(t, "postgresql"), "query/account.go")
	start := strings.Index(q, "type AccountCreateInput struct {")
	end := strings.Index(q[start:], "}")
	input := q[start : start+end]
	for _, want := range []string{
		`Id\s+\*int\b`,           // autoincrement()
		`Code\s+string\b`,        // cuid(): generated client-side, not a DB default
		`Uid\s+\*string\b`,       // uuid()
		`Role\s+\*model\.Role\b`, // enum literal default
		`Roles\s+\[\]model\.Role\b`,
		`Balance\s+\*string\b`,       // numeric literal default
		`CreatedAt\s+\*time\.Time\b`, // now()
		`Nickname\s+\*string\b`,      // optional with default
		`Note\s+\*string\b`,          // optional without default
	} {
		if !regexp.MustCompile(want).MatchString(input) {
			t.Errorf("AccountCreateInput missing %s:\n%s", want, input)
		}
	}
	if strings.Contains(input, "Slug") {
		t.Errorf("generated column must not appear in AccountCreateInput:\n%s", input)
	}
}

func TestGeneratedColumnHasNoSetters(t *testing.T) {
	files := generateFeatureFiles(t, "postgresql")
	q := fileContent(t, files, "query/account.go")
	if strings.Contains(q, "func (accountSlugField) Set(") || strings.Contains(q, "func (accountSlugField) SetNull(") {
		t.Error("generated column slug must not expose Set/SetNull")
	}
	if !strings.Contains(q, "func (accountSlugField) Equals(") {
		t.Error("generated column slug should stay queryable")
	}
	c := fileContent(t, files, "client/client.go")
	if !strings.Contains(c, `return "", fmt.Errorf("Account field %q is a generated column and cannot be written", field)`) {
		t.Error("client should reject writes to generated columns")
	}
	createRow := c[strings.Index(c, "func accountCreateInputRow("):]
	createRow = createRow[:strings.Index(createRow, "\n}\n")]
	if strings.Contains(createRow, "Slug") {
		t.Errorf("bulk create must not write generated columns:\n%s", createRow)
	}
}

func TestClientTableReferenceHonoursSchemaOnPostgres(t *testing.T) {
	c := fileContent(t, generateFeatureFiles(t, "postgresql"), "client/client.go")
	want := "\tif c.dialect == \"postgresql\" {\n\t\treturn c.quoteIdentifier(\"audit\") + \".\" + c.quoteIdentifier(\"accounts\")\n\t}"
	if !strings.Contains(c, want) {
		t.Errorf("quotedAccountTable should schema-qualify on postgresql")
	}
	event := c[strings.Index(c, "func quotedEventTable("):]
	event = event[:strings.Index(event, "\n}\n")]
	if strings.Contains(event, "postgresql") {
		t.Errorf("models without @@schema should use the bare table name:\n%s", event)
	}
}

func TestQuotedGeneratedTable(t *testing.T) {
	m := &ir.Model{Name: "Account", DBName: "accounts", Schema: "audit"}
	tests := []struct {
		ds   *ir.Datasource
		want string
	}{
		{nil, `"audit"."accounts"`},
		{&ir.Datasource{Provider: "postgresql"}, `"audit"."accounts"`},
		{&ir.Datasource{Provider: "mysql"}, "`accounts`"},
		{&ir.Datasource{Provider: "sqlite"}, `"accounts"`},
	}
	for _, tt := range tests {
		if got := quotedGeneratedTable(tt.ds, m); got != tt.want {
			t.Errorf("quotedGeneratedTable(%v) = %s, want %s", tt.ds, got, tt.want)
		}
	}
	if got := quotedGeneratedTable(nil, &ir.Model{Name: "Plain"}); got != `"Plain"` {
		t.Errorf("model without schema = %s", got)
	}
}

func TestFieldClassificationHelpers(t *testing.T) {
	fn := func(name string) *ir.DefaultValue { return &ir.DefaultValue{IsFunction: true, FuncName: name} }
	tests := []struct {
		name      string
		field     *ir.Field
		cuid      bool
		dbDefault bool
		inputType string
	}{
		{"cuid", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", Default: fn("cuid")}, true, false, "string"},
		{"optional cuid", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", IsOptional: true, Default: fn("cuid")}, true, false, "*string"},
		{"autoincrement", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "BigInt", Default: fn("autoincrement")}, false, true, "*int64"},
		{"dbgenerated", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", Default: &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{`"gen_random_uuid()::text"`}}}, false, true, "*string"},
		{"literal", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "Boolean", Default: &ir.DefaultValue{IsBool: true, Value: "false"}}, false, true, "*bool"},
		{"optional literal", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "Int", IsOptional: true, Default: &ir.DefaultValue{IsNumber: true, Value: "1"}}, false, true, "*int"},
		{"list default", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", IsList: true, Default: &ir.DefaultValue{IsArray: true}}, false, true, "*[]string"},
		{"no default", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String"}, false, false, "string"},
		{"generated", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", IsGenerated: true, Default: fn("dbgenerated")}, false, false, "string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCUIDField(tt.field); got != tt.cuid {
				t.Errorf("isCUIDField = %v, want %v", got, tt.cuid)
			}
			if got := hasDBDefault(tt.field); got != tt.dbDefault {
				t.Errorf("hasDBDefault = %v, want %v", got, tt.dbDefault)
			}
			if got := createInputGoType(tt.field); got != tt.inputType {
				t.Errorf("createInputGoType = %q, want %q", got, tt.inputType)
			}
		})
	}
}

func TestModelFieldSelectionHelpers(t *testing.T) {
	m := featureSchema("postgresql").Models[0]
	names := func(fields []*ir.Field) string {
		out := make([]string, len(fields))
		for i, f := range fields {
			out[i] = f.Name
		}
		return strings.Join(out, ",")
	}
	if got := names(writableFields(m)); strings.Contains(got, "slug") || !strings.HasPrefix(got, "id,code") {
		t.Errorf("writableFields = %s", got)
	}
	if got := names(generatedFields(m)); got != "slug" {
		t.Errorf("generatedFields = %s", got)
	}
	if got := names(listFields(m)); got != "roles,tags,scores" {
		t.Errorf("listFields = %s", got)
	}
	if got := names(cuidFields(m)); got != "code" {
		t.Errorf("cuidFields = %s", got)
	}
	if got := createInputValues(m); strings.Contains(got, "Slug") {
		t.Errorf("createInputValues must skip generated columns: %s", got)
	}
}

func TestPKColumnsLiteral(t *testing.T) {
	tests := []struct {
		model *ir.Model
		want  string
	}{
		{&ir.Model{Name: "NoPK"}, "nil"},
		{&ir.Model{
			Name:       "Single",
			Fields:     []*ir.Field{{Name: "userId", Type: ir.FieldKindScalar, ScalarType: "Int"}},
			PrimaryKey: &ir.PrimaryKey{Fields: []string{"userId"}},
		}, `[]string{"user_id"}`},
		{&ir.Model{
			Name: "Composite",
			Fields: []*ir.Field{
				{Name: "a", DBName: "col_a", Type: ir.FieldKindScalar, ScalarType: "Int"},
				{Name: "b", Type: ir.FieldKindScalar, ScalarType: "Int"},
			},
			PrimaryKey: &ir.PrimaryKey{Fields: []string{"a", "b"}, IsComposite: true},
		}, `[]string{"col_a", "b"}`},
	}
	for _, tt := range tests {
		if got := pkColumnsLiteral(tt.model); got != tt.want {
			t.Errorf("pkColumnsLiteral(%s) = %s, want %s", tt.model.Name, got, tt.want)
		}
	}
}

func TestScanDestsWrapListFields(t *testing.T) {
	got := scanDestsStr(featureSchema("postgresql").Models[0])
	if !strings.Contains(got, "listScanner{dialect: c.dialect, dest: &item.Tags}") || !strings.Contains(got, "&item.Slug") {
		t.Errorf("scanDestsStr = %s", got)
	}
}

func TestClientExposesNewAPIs(t *testing.T) {
	c := fileContent(t, generateFeatureFiles(t, "postgresql"), "client/client.go")
	for _, want := range []string{
		"func WithErrorHandler(handler func(error) error) Option",
		"func (a AccountActions) AggregateWhere(ctx context.Context, wheres []query.AccountWhereClause, opts ...query.AccountAggregateOption) (*query.AccountAggregateResult, error)",
		"func (a AccountActions) GroupByWhere(ctx context.Context, fields []string, wheres []query.AccountWhereClause, opts ...query.AccountAggregateOption) ([]query.AccountGroupByResult, error)",
		"func (a AccountActions) Aggregate(ctx context.Context, opts ...query.AccountAggregateOption) (*query.AccountAggregateResult, error)",
		"func (a AccountActions) GroupBy(ctx context.Context, fields []string, opts ...query.AccountAggregateOption) ([]query.AccountGroupByResult, error)",
		`"SAVEPOINT "+name`,
		`"ROLLBACK TO SAVEPOINT "+name`,
		`"RELEASE SAVEPOINT "+name`,
		"func buildAccountWhere(c *Client, wheres []query.AccountWhereClause, argIdx *int) (string, []any, error)",
		"a.client.singleRowWhere(table, []string{\"id\"}, whereSQL)",
		"a.client.singleRowWhere(table, nil, whereSQL)",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("client.go missing %q", want)
		}
	}
	if strings.Contains(c, `parts = append(parts, "1 = 0"); continue`) {
		t.Error("unknown where fields must not degrade to 1 = 0")
	}
}

func TestEmptySchemaClientSupportsNestedTx(t *testing.T) {
	files, err := NewGenerator(&ir.Schema{}).Generate()
	if err != nil {
		t.Fatal(err)
	}
	c := string(files[0].Content)
	if !strings.Contains(c, "func (c *Client) savepointTx(") || strings.Contains(c, "dbError") {
		t.Errorf("empty-schema client should support savepoints without model helpers:\n%s", c)
	}
}
