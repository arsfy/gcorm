// Package golang implements Go code generation from a resolved GCORM schema IR.
// It produces a client/query/model package structure that provides Prisma-like
// type-safe query builders.
package golang

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// Generator produces Go source files from a resolved schema.
type Generator struct {
	schema     *ir.Schema
	pkg        string
	output     string
	dialect    string
	modulePath string
}

// GenOption configures the Generator.
type GenOption func(*Generator)

// WithModulePath sets the base Go import path for generated sub-packages.
func WithModulePath(path string) GenOption {
	return func(g *Generator) { g.modulePath = path }
}

// NewGenerator creates a new Go code generator.
func NewGenerator(schema *ir.Schema, opts ...GenOption) *Generator {
	pkg := "db"
	output := "./gen"
	dialect := "postgresql"

	if len(schema.Generators) > 0 {
		g := schema.Generators[0]
		if g.Package != "" {
			pkg = g.Package
		}
		if g.Output != "" {
			output = g.Output
		}
	}
	if schema.Datasource != nil {
		dialect = schema.Datasource.Provider
	}

	gen := &Generator{
		schema:  schema,
		pkg:     pkg,
		output:  output,
		dialect: dialect,
	}
	for _, opt := range opts {
		opt(gen)
	}
	return gen
}

// GeneratedFile represents a single generated Go source file.
type GeneratedFile struct {
	Path    string // Relative path from output root.
	Content []byte // Go source content.
}

// Generate produces all Go source files for the schema.
func (g *Generator) Generate() ([]*GeneratedFile, error) {
	if err := validateSchemaNames(g.schema); err != nil {
		return nil, err
	}

	if len(g.schema.Models) > 0 && g.baseImportPath() == "" {
		return nil, fmt.Errorf("cannot determine the Go module path for generated imports in output %q: no go.mod found in the output directory or any parent; pass WithModulePath to set the base import path explicitly", g.output)
	}

	var files []*GeneratedFile

	// Generate model types.
	modelFiles, err := g.generateModels()
	if err != nil {
		return nil, fmt.Errorf("generate models: %w", err)
	}
	files = append(files, modelFiles...)

	// Generate query builders.
	queryFiles, err := g.generateQueries()
	if err != nil {
		return nil, fmt.Errorf("generate queries: %w", err)
	}
	files = append(files, queryFiles...)

	// Generate client entry point.
	clientFile, err := g.generateClient()
	if err != nil {
		return nil, fmt.Errorf("generate client: %w", err)
	}
	files = append(files, clientFile)

	return files, nil
}

// Manifest returns the generation manifest.
func (g *Generator) Manifest() *ir.GenerationManifest {
	m := &ir.GenerationManifest{
		Generator: "gco-go",
		Output:    g.output,
		Package:   g.pkg,
	}
	for _, model := range g.schema.Models {
		m.Models = append(m.Models, model.Name)
	}
	for _, enum := range g.schema.Enums {
		m.Enums = append(m.Enums, enum.Name)
	}
	return m
}

// generateModels produces model struct and enum type files.
func (g *Generator) generateModels() ([]*GeneratedFile, error) {
	var files []*GeneratedFile

	// Enums file.
	if len(g.schema.Enums) > 0 {
		content, err := g.renderTemplate("enums", enumsTmpl, map[string]any{
			"Package": g.pkg,
			"Enums":   g.schema.Enums,
		})
		if err != nil {
			return nil, err
		}
		files = append(files, &GeneratedFile{
			Path:    filepath.Join("model", "enums.go"),
			Content: content,
		})
	}

	// Model structs file.
	if len(g.schema.Models) > 0 {
		models := make([]*ir.Model, len(g.schema.Models))
		copy(models, g.schema.Models)
		sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })

		content, err := g.renderTemplate("models", modelsTmpl, map[string]any{
			"Package": g.pkg,
			"Models":  models,
			"GoType":  goTypeForField,
		})
		if err != nil {
			return nil, err
		}
		files = append(files, &GeneratedFile{
			Path:    filepath.Join("model", "models.go"),
			Content: content,
		})
	}

	return files, nil
}

// generateQueries produces query builder files per model.
func (g *Generator) generateQueries() ([]*GeneratedFile, error) {
	var files []*GeneratedFile

	models := make([]*ir.Model, len(g.schema.Models))
	copy(models, g.schema.Models)
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })

	for _, model := range models {
		content, err := g.renderTemplate("query_"+model.Name, queryTmpl, map[string]any{
			"Package":     g.pkg,
			"Model":       model,
			"GoType":      queryGoTypeForField,
			"SetGoType":   setGoTypeForField,
			"Lower":       toLowerFirst,
			"ModelImport": g.baseImportPath() + "/model",
		})
		if err != nil {
			return nil, err
		}
		files = append(files, &GeneratedFile{
			Path:    filepath.Join("query", strings.ToLower(model.Name)+".go"),
			Content: content,
		})
	}

	return files, nil
}

// generatedTypeSuffixes are the query-package types that share a namespace
// with model query variables. Client-package types do not collide with them.
var generatedTypeSuffixes = []string{
	"Query", "QueryOption", "QueryConfig",
	"WhereClause", "OrderByClause", "IncludeClause",
	"TakeOption", "SkipOption", "SetClause",
	"AggregateOption", "AggregateResult", "GroupByResult",
	"CreateInput", "CreateNestedInput", "WhereUniqueInput",
}

// validateSchemaNames rejects schemas whose model or field names would produce
// colliding Go identifiers or generated file names.
func validateSchemaNames(s *ir.Schema) error {
	// Model names that differ only in case map to the same generated query
	// file (strings.ToLower(name) + ".go") and would overwrite each other.
	seenLower := make(map[string]string, len(s.Models))
	for _, m := range s.Models {
		key := strings.ToLower(m.Name)
		if prev, ok := seenLower[key]; ok {
			return fmt.Errorf("model names %q and %q collide: both generate query/%s.go", prev, m.Name, key)
		}
		seenLower[key] = m.Name
	}

	modelNames := make(map[string]bool, len(s.Models))
	for _, m := range s.Models {
		modelNames[m.Name] = true
	}

	// A model name must not collide with a type name derived from another
	// model (e.g. model "UserQuery" vs. type "UserQuery" from model "User").
	for _, m := range s.Models {
		for _, suffix := range generatedTypeSuffixes {
			derived := m.Name + suffix
			if modelNames[derived] {
				return fmt.Errorf("model name %q conflicts with generated type %q for model %q; rename one of the models", derived, derived, m.Name)
			}
		}
	}

	// Field helper types are named lower(model) + Upper(field); two distinct
	// model+field pairs can produce the same helper (e.g. User.nameX and
	// UserName.x both yield userNameXField).
	type fieldRef struct{ model, field string }
	helpers := make(map[string]fieldRef)
	for _, m := range s.Models {
		fields := make(map[string]string, len(m.Fields))
		for _, f := range m.Fields {
			field := toUpperFirst(f.Name)
			if prev, ok := fields[field]; ok {
				return fmt.Errorf("fields %s.%s and %s.%s both generate Go field %q; rename one of the fields", m.Name, prev, m.Name, f.Name, field)
			}
			fields[field] = f.Name
			kind := "Field"
			if f.Type == ir.FieldKindRelation {
				kind = "Relation"
			}
			helper := toLowerFirst(m.Name) + field + kind
			if prev, ok := helpers[helper]; ok {
				return fmt.Errorf("fields %s.%s and %s.%s both generate helper type %q; rename one of the fields",
					prev.model, prev.field, m.Name, f.Name, helper)
			}
			helpers[helper] = fieldRef{model: m.Name, field: f.Name}
		}
	}

	return nil
}

// generateClient produces the main client entry point.
func (g *Generator) generateClient() (*GeneratedFile, error) {
	models := make([]*ir.Model, len(g.schema.Models))
	copy(models, g.schema.Models)
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })

	content, err := g.renderTemplate("client", clientTmpl, map[string]any{
		"Package":    g.pkg,
		"Models":     models,
		"BaseImport": g.baseImportPath(),
		"Dialect":    g.dialect,
	})
	if err != nil {
		return nil, err
	}
	return &GeneratedFile{
		Path:    filepath.Join("client", "client.go"),
		Content: content,
	}, nil
}

func (g *Generator) renderTemplate(name, tmplSrc string, data any) ([]byte, error) {
	funcMap := template.FuncMap{
		"goType":            goTypeForField,
		"queryGoType":       queryGoTypeForField,
		"setGoType":         setGoTypeForField,
		"lower":             toLowerFirst,
		"upper":             toUpperFirst,
		"snakeCase":         toSnakeCase,
		"pluralize":         simplePluralize,
		"scalarCols":        scalarColumns,
		"hasEnumFields":     hasEnumFields,
		"quote":             func(s string) string { return fmt.Sprintf("%q", s) },
		"quoteTagValue":     func(s string) string { return strings.ReplaceAll(fmt.Sprintf("%q", s), "`", `\x60`) },
		"tableName":         func(m *ir.Model) string { return m.TableName() },
		"quotedTableName":   func(m *ir.Model) string { return quotedGeneratedTable(g.schema.Datasource, m) },
		"columnCSV":         columnCSV,
		"quotedColumnCSV":   func(m *ir.Model) string { return quotedColumnCSV(g.schema.Datasource, m) },
		"scanFields":        scanFieldsStr,
		"columnName":        columnName,
		"isNumeric":         isNumericField,
		"uniqueFields":      uniqueFields,
		"hasRelations":      hasRelationFields,
		"createInputValues": createInputValues,
		"scalarColNamesCSV": scalarColNamesCSV,
		"writableFields":    writableFields,
		"writableColNamesCSV": func(m *ir.Model) string {
			return quotedGoStrings(fieldColumns(writableFields(m)))
		},
		"generatedFields":   generatedFields,
		"listFields":        listFields,
		"cuidFields":        cuidFields,
		"isCUID":            isCUIDField,
		"hasDBDefault":      hasDBDefault,
		"createInputGoType": createInputGoType,
		"scanDests":         scanDestsStr,
		"pkColumnsLiteral":  pkColumnsLiteral,
		"enumDBValue":       enumDBValue,
	}

	tmpl, err := template.New(name).Funcs(funcMap).Parse(tmplSrc)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template %s: %w", name, err)
	}

	// Format with gofmt.
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated template %s: %w", name, err)
	}
	return formatted, nil
}

func quoteGeneratedIdentifier(ds *ir.Datasource, name string) string {
	dialect := "postgresql"
	if ds != nil && ds.Provider != "" {
		dialect = ds.Provider
	}
	if dialect == "mysql" {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quotedGeneratedTable returns the quoted table reference for a model. Only
// PostgreSQL honours @@schema; other dialects use the bare table name.
func quotedGeneratedTable(ds *ir.Datasource, m *ir.Model) string {
	table := quoteGeneratedIdentifier(ds, m.TableName())
	if m.Schema != "" && (ds == nil || ds.Provider == "" || ds.Provider == "postgresql") {
		return quoteGeneratedIdentifier(ds, m.Schema) + "." + table
	}
	return table
}

func quotedColumnCSV(ds *ir.Datasource, m *ir.Model) string {
	fields := m.ScalarFields()
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = quoteGeneratedIdentifier(ds, columnName(f))
	}
	return strings.Join(cols, ", ")
}

// goTypeForField returns the Go type for an IR field.
func goTypeForField(f *ir.Field) string {
	var base string
	switch f.Type {
	case ir.FieldKindEnum:
		base = f.EnumType
	case ir.FieldKindRelation:
		base = "*" + f.ModelType
		if f.IsList {
			return "[]*" + f.ModelType
		}
		if f.IsOptional {
			return "*" + f.ModelType
		}
		return "*" + f.ModelType
	default:
		base = scalarGoType(f.ScalarType)
	}

	if f.IsList {
		return "[]" + base
	}
	if f.IsOptional {
		return "*" + base
	}
	return base
}

func queryGoTypeForField(f *ir.Field) string {
	if f.Type == ir.FieldKindEnum {
		base := "model." + f.EnumType
		if f.IsList {
			return "[]" + base
		}
		if f.IsOptional {
			return "*" + base
		}
		return base
	}
	return goTypeForField(f)
}

func setGoTypeForField(f *ir.Field) string {
	if f == nil {
		return "any"
	}
	if f.Type == ir.FieldKindEnum {
		return "model." + f.EnumType
	}
	clone := *f
	clone.IsOptional = false
	return goTypeForField(&clone)
}

func hasEnumFields(m *ir.Model) bool {
	for _, f := range m.Fields {
		if f.Type == ir.FieldKindEnum {
			return true
		}
	}
	return false
}

func (g *Generator) baseImportPath() string {
	if g.modulePath != "" {
		return strings.TrimSuffix(g.modulePath, "/")
	}

	output, err := filepath.Abs(g.output)
	if err != nil {
		return ""
	}
	modulePath, moduleDir := findModule(output)
	if modulePath == "" {
		return ""
	}

	rel, err := filepath.Rel(moduleDir, output)
	if err != nil {
		return ""
	}
	if rel == "." {
		return modulePath
	}
	return modulePath + "/" + filepath.ToSlash(rel)
}

func findModule(dir string) (modulePath, moduleDir string) {
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						return fields[1], dir
					}
				}
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

// scalarGoType maps schema scalar types to Go types.
func scalarGoType(t string) string {
	switch t {
	case "String":
		return "string"
	case "Int":
		return "int"
	case "SmallInt":
		return "int16"
	case "BigInt":
		return "int64"
	case "Float":
		return "float64"
	case "Decimal":
		return "string" // simplified; production would use decimal.Decimal
	case "Boolean":
		return "bool"
	case "DateTime":
		return "time.Time"
	case "Bytes":
		return "[]byte"
	case "Json":
		return "json.RawMessage"
	case "UUID":
		return "string"
	default:
		return t
	}
}

func toLowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func toUpperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func toSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			result = append(result, '_')
		}
		result = append(result, unicode.ToLower(r))
	}
	return string(result)
}

func simplePluralize(s string) string {
	if strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") || strings.HasSuffix(s, "z") ||
		strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "sh") {
		return s + "es"
	}
	if strings.HasSuffix(s, "y") && len(s) > 1 {
		prev := s[len(s)-2]
		if prev != 'a' && prev != 'e' && prev != 'i' && prev != 'o' && prev != 'u' {
			return s[:len(s)-1] + "ies"
		}
	}
	return s + "s"
}

func scalarColumns(model *ir.Model) []*ir.Field {
	return model.ScalarFields()
}

// needsTimeImport checks if any field uses time.Time.
func needsTimeImport(fields []*ir.Field) bool {
	for _, f := range fields {
		if f.Type == ir.FieldKindScalar && f.ScalarType == "DateTime" {
			return true
		}
	}
	return false
}

// needsJSONImport checks if any field uses json.RawMessage.
func needsJSONImport(fields []*ir.Field) bool {
	for _, f := range fields {
		if f.Type == ir.FieldKindScalar && f.ScalarType == "Json" {
			return true
		}
	}
	return false
}

// columnName returns the database column name for a field.
func columnName(f *ir.Field) string {
	if f.DBName != "" {
		return f.DBName
	}
	return toSnakeCase(f.Name)
}

// columnCSV returns comma-separated snake_case column names for scalar fields.
func columnCSV(m *ir.Model) string {
	fields := m.ScalarFields()
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = columnName(f)
	}
	return strings.Join(cols, ", ")
}

// scanFieldsStr returns "&item.Field1, &item.Field2, ..." for Scan calls.
func scanFieldsStr(m *ir.Model) string {
	fields := m.ScalarFields()
	refs := make([]string, len(fields))
	for i, f := range fields {
		refs[i] = "&item." + toUpperFirst(f.Name)
	}
	return strings.Join(refs, ", ")
}

// isNumericField returns true if the field is a numeric scalar type.
func isNumericField(f *ir.Field) bool {
	if f.Type != ir.FieldKindScalar {
		return false
	}
	switch f.ScalarType {
	case "Int", "SmallInt", "BigInt", "Float", "Decimal":
		return true
	}
	return false
}

// uniqueFields returns fields that are ID or unique.
func uniqueFields(m *ir.Model) []*ir.Field {
	var result []*ir.Field
	for _, f := range m.ScalarFields() {
		if f.IsID || f.IsUnique {
			result = append(result, f)
		}
	}
	return result
}

// hasRelationFields checks if a model has any relation fields.
func hasRelationFields(m *ir.Model) bool {
	for _, f := range m.Fields {
		if f.Type == ir.FieldKindRelation {
			return true
		}
	}
	return false
}

// createInputValues generates "d.Field1, d.Field2, ..." for CreateInput extraction.
func createInputValues(m *ir.Model) string {
	fields := writableFields(m)
	refs := make([]string, len(fields))
	for i, f := range fields {
		refs[i] = "d." + toUpperFirst(f.Name)
	}
	return strings.Join(refs, ", ")
}

// scalarColNamesCSV returns quoted column names as Go source: "col1", "col2", ...
func scalarColNamesCSV(m *ir.Model) string {
	fields := m.ScalarFields()
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = fmt.Sprintf("%q", columnName(f))
	}
	return strings.Join(quoted, ", ")
}

// writableFields returns the scalar fields the generated client may write:
// every scalar field except database-generated (computed) columns.
func writableFields(m *ir.Model) []*ir.Field {
	var out []*ir.Field
	for _, f := range m.ScalarFields() {
		if !f.IsGenerated {
			out = append(out, f)
		}
	}
	return out
}

// generatedFields returns the read-only, database-generated scalar fields.
func generatedFields(m *ir.Model) []*ir.Field {
	var out []*ir.Field
	for _, f := range m.ScalarFields() {
		if f.IsGenerated {
			out = append(out, f)
		}
	}
	return out
}

// listFields returns the scalar and enum list fields of a model.
func listFields(m *ir.Model) []*ir.Field {
	var out []*ir.Field
	for _, f := range m.ScalarFields() {
		if f.IsList {
			out = append(out, f)
		}
	}
	return out
}

// cuidFields returns the writable fields whose id is generated client-side by
// @default(cuid()).
func cuidFields(m *ir.Model) []*ir.Field {
	var out []*ir.Field
	for _, f := range writableFields(m) {
		if isCUIDField(f) {
			out = append(out, f)
		}
	}
	return out
}

// isCUIDField reports whether the field uses @default(cuid()). The database
// has no default for such columns; the generated client fills the value.
func isCUIDField(f *ir.Field) bool {
	return f != nil && f.Default != nil && f.Default.IsFunction && f.Default.FuncName == "cuid"
}

// hasDBDefault reports whether the database fills the column when it is
// omitted: any @default except cuid() (which is generated client-side).
func hasDBDefault(f *ir.Field) bool {
	return f != nil && f.Default != nil && !isCUIDField(f) && !f.IsGenerated
}

// createInputGoType returns the CreateInput field type. Fields with a database
// default use a pointer so nil means "use the column DEFAULT".
func createInputGoType(f *ir.Field) string {
	t := queryGoTypeForField(f)
	if hasDBDefault(f) && !strings.HasPrefix(t, "*") {
		return "*" + t
	}
	return t
}

// scanDestsStr returns the Scan destinations for a model row in the client
// package; list fields scan through the dialect-aware listScanner.
func scanDestsStr(m *ir.Model) string {
	fields := m.ScalarFields()
	refs := make([]string, len(fields))
	for i, f := range fields {
		ref := "&item." + toUpperFirst(f.Name)
		if f.IsList {
			ref = "listScanner{dialect: c.dialect, dest: " + ref + "}"
		}
		refs[i] = ref
	}
	return strings.Join(refs, ", ")
}

// pkColumnsLiteral returns the primary key column names as a Go []string
// literal, or "nil" when the model has no primary key.
func pkColumnsLiteral(m *ir.Model) string {
	if m.PrimaryKey == nil || len(m.PrimaryKey.Fields) == 0 {
		return "nil"
	}
	byName := make(map[string]*ir.Field, len(m.Fields))
	for _, f := range m.ScalarFields() {
		byName[f.Name] = f
	}
	cols := make([]string, 0, len(m.PrimaryKey.Fields))
	for _, name := range m.PrimaryKey.Fields {
		if f, ok := byName[name]; ok {
			cols = append(cols, columnName(f))
		} else {
			cols = append(cols, name)
		}
	}
	return "[]string{" + quotedGoStrings(cols) + "}"
}

// enumDBValue returns the value stored in the database for an enum member:
// its @map name when present, otherwise the member name.
func enumDBValue(v *ir.EnumValue) string {
	if v.DBName != "" {
		return v.DBName
	}
	return v.Name
}

func fieldColumns(fields []*ir.Field) []string {
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = columnName(f)
	}
	return cols
}

func quotedGoStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}
