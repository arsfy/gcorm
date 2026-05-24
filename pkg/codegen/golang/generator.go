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
		"tableName":         func(m *ir.Model) string { return m.TableName() },
		"columnCSV":         columnCSV,
		"scanFields":        scanFieldsStr,
		"columnName":        columnName,
		"isNumeric":         isNumericField,
		"uniqueFields":      uniqueFields,
		"hasRelations":      hasRelationFields,
		"createInputValues": createInputValues,
		"scalarColNamesCSV": scalarColNamesCSV,
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
		// Return unformatted if gofmt fails, for debugging.
		return buf.Bytes(), nil
	}
	return formatted, nil
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
		return "model." + f.EnumType
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

	modulePath := findModulePath()
	if modulePath == "" {
		return ""
	}

	output := strings.TrimSpace(filepath.ToSlash(filepath.Clean(g.output)))
	output = strings.TrimPrefix(output, "./")
	output = strings.TrimPrefix(output, "/")
	if output == "" || output == "." {
		return modulePath
	}
	return modulePath + "/" + output
}

func findModulePath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						return fields[1]
					}
				}
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
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
	fields := m.ScalarFields()
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
