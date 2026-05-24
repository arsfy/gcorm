// Package validator provides semantic validation for GCORM schema ASTs.
package validator

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// Severity indicates the severity of a validation issue.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

// ValidationError represents a validation error with source location.
type ValidationError struct {
	Message  string
	Pos      ast.Position
	Severity Severity
}

func (e *ValidationError) Error() string {
	prefix := "error"
	if e.Severity == SeverityWarning {
		prefix = "warning"
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s", e.Pos.File, e.Pos.Line, e.Pos.Column, prefix, e.Message)
}

// ValidationResult holds all validation errors and warnings.
type ValidationResult struct {
	Errors   []*ValidationError
	Warnings []*ValidationError
}

// HasErrors returns true if there are any errors (not warnings).
func (r *ValidationResult) HasErrors() bool { return len(r.Errors) > 0 }

// AllErrors returns all errors as a plain []error slice (for backward compatibility).
func (r *ValidationResult) AllErrors() []error {
	out := make([]error, len(r.Errors))
	for i, e := range r.Errors {
		out[i] = e
	}
	return out
}

// AllIssues returns all errors and warnings combined.
func (r *ValidationResult) AllIssues() []*ValidationError {
	out := make([]*ValidationError, 0, len(r.Errors)+len(r.Warnings))
	out = append(out, r.Errors...)
	out = append(out, r.Warnings...)
	return out
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Validate checks semantic correctness of a single Document.
func Validate(doc *ast.Document) *ValidationResult {
	if doc == nil {
		return &ValidationResult{}
	}

	v := newValidator()
	v.collectTypes(doc)
	v.validateDatasourceCount(doc.Datasources)
	for _, d := range doc.Datasources {
		v.validateDatasource(d)
	}
	v.validateModelNames(doc.Models)
	for _, m := range doc.Models {
		v.validateModel(m)
	}
	v.validateEnumNames(doc.Enums)
	for _, e := range doc.Enums {
		v.validateEnum(e)
	}
	return v.result
}

// ValidateDocumentSet validates a multi-file schema.
func ValidateDocumentSet(ds *ast.DocumentSet) *ValidationResult {
	if ds == nil || len(ds.Documents) == 0 {
		res := &ValidationResult{}
		res.Errors = append(res.Errors, &ValidationError{Message: "no documents to validate"})
		return res
	}

	v := newValidator()

	// Collect types from all documents.
	for _, doc := range ds.Documents {
		v.collectTypes(doc)
	}

	// Check duplicate model/enum names across all documents.
	modelSeen := make(map[string]bool)
	for _, doc := range ds.Documents {
		for _, m := range doc.Models {
			if modelSeen[m.Name] {
				v.addError(m.Span.Start, fmt.Sprintf("duplicate model name %q", m.Name))
			}
			modelSeen[m.Name] = true
		}
	}
	enumSeen := make(map[string]bool)
	for _, doc := range ds.Documents {
		for _, e := range doc.Enums {
			if enumSeen[e.Name] {
				v.addError(e.Span.Start, fmt.Sprintf("duplicate enum name %q", e.Name))
			}
			enumSeen[e.Name] = true
		}
	}

	// Check datasource count across all documents.
	dsFound := 0
	for _, doc := range ds.Documents {
		for _, d := range doc.Datasources {
			dsFound++
			if dsFound > 1 {
				v.addError(d.Span.Start, "multiple datasource blocks are not allowed")
			}
		}
	}

	// Per-document validation.
	for _, doc := range ds.Documents {
		for _, d := range doc.Datasources {
			v.validateDatasource(d)
		}
		for _, m := range doc.Models {
			v.validateModelNaming(m)
			v.validateModel(m)
		}
		for _, e := range doc.Enums {
			v.validateEnumNaming(e)
			v.validateEnum(e)
		}
	}

	// Cross-reference validation.
	v.validateCrossReferences(ds)

	return v.result
}

// ---------------------------------------------------------------------------
// Internal validator
// ---------------------------------------------------------------------------

var scalarTypes = map[string]bool{
	"String":   true,
	"Int":      true,
	"SmallInt": true,
	"BigInt":   true,
	"Float":    true,
	"Decimal":  true,
	"Boolean":  true,
	"DateTime": true,
	"Bytes":    true,
	"Json":     true,
	"UUID":     true,
}

var knownDatasourceKeys = map[string]bool{
	"provider":          true,
	"url":               true,
	"shadowDatabaseUrl": true,
	"directUrl":         true,
	"relationMode":      true,
	"schemas":           true,
	"extensions":        true,
}

var validProviders = map[string]bool{
	"postgresql": true,
	"mysql":      true,
	"sqlite":     true,
}

type validator struct {
	result     *ValidationResult
	modelNames map[string]ast.Position
	enumNames  map[string]ast.Position
}

func newValidator() *validator {
	return &validator{
		result:     &ValidationResult{},
		modelNames: make(map[string]ast.Position),
		enumNames:  make(map[string]ast.Position),
	}
}

func (v *validator) addError(pos ast.Position, msg string) {
	v.result.Errors = append(v.result.Errors, &ValidationError{
		Message: msg, Pos: pos, Severity: SeverityError,
	})
}

func (v *validator) addWarning(pos ast.Position, msg string) {
	v.result.Warnings = append(v.result.Warnings, &ValidationError{
		Message: msg, Pos: pos, Severity: SeverityWarning,
	})
}

func (v *validator) collectTypes(doc *ast.Document) {
	for _, m := range doc.Models {
		v.modelNames[m.Name] = m.Span.Start
	}
	for _, e := range doc.Enums {
		v.enumNames[e.Name] = e.Span.Start
	}
}

func (v *validator) isKnownType(name string) bool {
	if scalarTypes[name] {
		return true
	}
	if _, ok := v.modelNames[name]; ok {
		return true
	}
	if _, ok := v.enumNames[name]; ok {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Datasource validation
// ---------------------------------------------------------------------------

func (v *validator) validateDatasourceCount(datasources []ast.DatasourceDecl) {
	if len(datasources) > 1 {
		for _, d := range datasources[1:] {
			v.addError(d.Span.Start, "multiple datasource blocks are not allowed")
		}
	}
}

func (v *validator) validateDatasource(d ast.DatasourceDecl) {
	hasURL := false
	for _, e := range d.Entries {
		switch e.Key {
		case "provider":
			if sl, ok := e.Value.(ast.StringLiteral); ok {
				if !validProviders[sl.Value] {
					v.addError(e.Span.Start, fmt.Sprintf(
						"invalid provider %q; must be one of: postgresql, mysql, sqlite", sl.Value))
				}
			}
		case "url":
			hasURL = true
		default:
			if !knownDatasourceKeys[e.Key] {
				v.addWarning(e.Span.Start, fmt.Sprintf("unknown datasource config key %q", e.Key))
			}
		}
	}
	if !hasURL {
		v.addError(d.Span.Start, "datasource block requires a \"url\" entry")
	}
}

// ---------------------------------------------------------------------------
// Model validation
// ---------------------------------------------------------------------------

func (v *validator) validateModelNames(models []ast.ModelDecl) {
	seen := make(map[string]bool)
	for _, m := range models {
		if seen[m.Name] {
			v.addError(m.Span.Start, fmt.Sprintf("duplicate model name %q", m.Name))
		}
		seen[m.Name] = true
		v.validateModelNaming(m)
	}
}

func (v *validator) validateModelNaming(m ast.ModelDecl) {
	if len(m.Name) > 0 && !unicode.IsUpper(rune(m.Name[0])) {
		v.addError(m.Span.Start, fmt.Sprintf("model name %q must start with an uppercase letter", m.Name))
	}
}

func (v *validator) validateModel(m ast.ModelDecl) {
	// Empty model body check (backward compatibility).
	if len(m.Fields) == 0 {
		v.addError(m.Span.Start, "model "+m.Name+" has no fields")
	}

	fieldNames := make(map[string]bool)
	idCount := 0

	for _, f := range m.Fields {
		if fieldNames[f.Name] {
			v.addError(f.Span.Start, fmt.Sprintf("duplicate field name %q in model %q", f.Name, m.Name))
		}
		fieldNames[f.Name] = true

		if len(f.Name) > 0 && !unicode.IsLower(rune(f.Name[0])) {
			v.addError(f.Span.Start, fmt.Sprintf("field name %q must start with a lowercase letter", f.Name))
		}

		if !v.isKnownType(f.Type.Name) {
			v.addError(f.Type.Span.Start, fmt.Sprintf("unknown type %q", f.Type.Name))
		}

		for _, a := range f.Attributes {
			v.validateFieldAttribute(a, f, m)
			if a.Name == "id" {
				idCount++
			}
		}
	}

	hasModelID := false
	for _, a := range m.Attributes {
		if a.Name == "id" {
			hasModelID = true
		}
		v.validateModelAttribute(a, m, fieldNames)
	}

	if idCount > 1 {
		v.addError(m.Span.Start, fmt.Sprintf("model %q has @id on multiple fields", m.Name))
	}
	if idCount > 0 && hasModelID {
		v.addError(m.Span.Start, fmt.Sprintf("model %q has both field-level @id and model-level @@id", m.Name))
	}
}

func (v *validator) validateFieldAttribute(a ast.Attribute, f ast.FieldDecl, m ast.ModelDecl) {
	switch a.Name {
	case "default":
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@default requires exactly one argument")
		} else {
			v.validateDefaultArg(a.Args[0], f)
		}
	case "map":
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@map requires exactly one argument")
		} else if _, ok := a.Args[0].Value.(ast.StringLiteral); !ok {
			v.addError(a.Span.Start, "@map argument must be a string")
		}
	case "relation":
		v.validateRelation(a, f, m)
	case "updatedAt":
		if f.Type.Name != "DateTime" {
			v.addError(a.Span.Start, "@updatedAt is only valid on DateTime fields")
		}
	}
}

func (v *validator) validateDefaultArg(arg ast.AttributeArg, f ast.FieldDecl) {
	if _, ok := arg.Value.(ast.FunctionCall); ok {
		return
	}
	switch f.Type.Name {
	case "String":
		if _, ok := arg.Value.(ast.StringLiteral); !ok {
			v.addError(arg.Span.Start,
				"@default value for String field must be a string or function call")
		}
	case "Int", "SmallInt", "BigInt", "Float", "Decimal":
		if _, ok := arg.Value.(ast.NumberLiteral); !ok {
			v.addError(arg.Span.Start, fmt.Sprintf(
				"@default value for %s field must be a number or function call", f.Type.Name))
		}
	case "Boolean":
		if _, ok := arg.Value.(ast.BooleanLiteral); !ok {
			v.addError(arg.Span.Start,
				"@default value for Boolean field must be a boolean or function call")
		}
	}
}

func (v *validator) validateRelation(a ast.Attribute, f ast.FieldDecl, m ast.ModelDecl) {
	if scalarTypes[f.Type.Name] {
		v.addError(a.Span.Start, "@relation is only valid on fields with a model type")
	}

	hasFields := false
	hasReferences := false

	for _, arg := range a.Args {
		switch arg.Name {
		case "fields":
			hasFields = true
			if arr, ok := arg.Value.(ast.ArrayLiteral); ok {
				fmap := make(map[string]bool)
				for _, ff := range m.Fields {
					fmap[ff.Name] = true
				}
				for _, elem := range arr.Elements {
					if id, ok := elem.(ast.Identifier); ok {
						if !fmap[id.Name] {
							v.addError(id.Span.Start, fmt.Sprintf(
								"@relation field %q does not exist in model %q", id.Name, m.Name))
						}
					}
				}
			} else {
				v.addError(arg.Span.Start, "@relation \"fields\" argument must be an array")
			}
		case "references":
			hasReferences = true
			if _, ok := arg.Value.(ast.ArrayLiteral); !ok {
				v.addError(arg.Span.Start, "@relation \"references\" argument must be an array")
			}
		}
	}

	if !hasFields {
		v.addError(a.Span.Start, "@relation requires a \"fields\" argument")
	}
	if !hasReferences {
		v.addError(a.Span.Start, "@relation requires a \"references\" argument")
	}
}

func (v *validator) validateModelAttribute(a ast.Attribute, m ast.ModelDecl, fieldNames map[string]bool) {
	switch a.Name {
	case "id":
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@@id requires exactly one argument")
		} else if _, ok := a.Args[0].Value.(ast.ArrayLiteral); !ok {
			v.addError(a.Span.Start, "@@id argument must be an array")
		}
	case "index":
		fieldArg, ok := indexFieldsArg(a)
		if !ok {
			v.addError(a.Span.Start, "@@index requires at least one argument")
		} else if arr, ok := fieldArg.Value.(ast.ArrayLiteral); ok {
			for _, elem := range arr.Elements {
				if id, ok := elem.(ast.Identifier); ok {
					if !fieldNames[id.Name] {
						v.addError(id.Span.Start, fmt.Sprintf(
							"@@index references unknown field %q in model %q", id.Name, m.Name))
					}
				} else {
					v.addError(elem.ExprSpan().Start, "@@index fields must be identifiers")
				}
			}
			v.validateIndexOptions(a, len(arr.Elements))
		} else {
			v.addError(fieldArg.Span.Start, "@@index fields argument must be an array")
		}
	case "map":
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@@map requires exactly one argument")
		} else if _, ok := a.Args[0].Value.(ast.StringLiteral); !ok {
			v.addError(a.Span.Start, "@@map argument must be a string")
		}
	}
}

func indexFieldsArg(a ast.Attribute) (ast.AttributeArg, bool) {
	for _, arg := range a.Args {
		if arg.Name == "" || arg.Name == "fields" {
			return arg, true
		}
	}
	return ast.AttributeArg{}, false
}

func (v *validator) validateIndexOptions(a ast.Attribute, fieldCount int) {
	for _, arg := range a.Args {
		switch arg.Name {
		case "", "fields":
			continue
		case "name", "where":
			if _, ok := arg.Value.(ast.StringLiteral); !ok {
				v.addError(arg.Span.Start, "@@index "+arg.Name+" must be a string")
			}
		case "sort", "order":
			v.validateIndexOptionValues(arg, fieldCount, map[string]bool{"ASC": true, "DESC": true}, "@@index "+arg.Name+" values must be Asc or Desc")
		case "nulls":
			v.validateIndexOptionValues(arg, fieldCount, map[string]bool{"FIRST": true, "LAST": true}, "@@index nulls values must be First or Last")
		case "opclass", "opclasses", "ops", "collate", "collation":
			v.validateIndexOptionValues(arg, fieldCount, nil, "")
		default:
			v.addWarning(arg.Span.Start, fmt.Sprintf("unknown @@index argument %q", arg.Name))
		}
	}
}

func (v *validator) validateIndexOptionValues(arg ast.AttributeArg, fieldCount int, allowed map[string]bool, message string) {
	values := indexOptionExprs(arg.Value)
	if len(values) > 1 && len(values) != fieldCount {
		v.addError(arg.Span.Start, fmt.Sprintf("@@index %s option has %d value(s), expected 1 or %d", arg.Name, len(values), fieldCount))
		return
	}
	for _, expr := range values {
		value := strings.ToUpper(strings.TrimSpace(indexOptionValue(expr)))
		if value == "" {
			continue
		}
		if allowed != nil && !allowed[value] {
			v.addError(expr.ExprSpan().Start, message)
		}
	}
}

func indexOptionExprs(expr ast.Expression) []ast.Expression {
	if arr, ok := expr.(ast.ArrayLiteral); ok {
		return arr.Elements
	}
	return []ast.Expression{expr}
}

func indexOptionValue(expr ast.Expression) string {
	switch v := expr.(type) {
	case ast.Identifier:
		return v.Name
	case ast.StringLiteral:
		return v.Value
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Enum validation
// ---------------------------------------------------------------------------

func (v *validator) validateEnumNames(enums []ast.EnumDecl) {
	seen := make(map[string]bool)
	for _, e := range enums {
		if seen[e.Name] {
			v.addError(e.Span.Start, fmt.Sprintf("duplicate enum name %q", e.Name))
		}
		seen[e.Name] = true
		v.validateEnumNaming(e)
	}
}

func (v *validator) validateEnumNaming(e ast.EnumDecl) {
	if len(e.Name) > 0 && !unicode.IsUpper(rune(e.Name[0])) {
		v.addError(e.Span.Start, fmt.Sprintf("enum name %q must start with an uppercase letter", e.Name))
	}
}

func (v *validator) validateEnum(e ast.EnumDecl) {
	seen := make(map[string]bool)
	for _, val := range e.Values {
		if seen[val.Name] {
			v.addError(val.Span.Start, fmt.Sprintf(
				"duplicate enum value %q in enum %q", val.Name, e.Name))
		}
		seen[val.Name] = true

		if val.Name != strings.ToUpper(val.Name) {
			v.addWarning(val.Span.Start, fmt.Sprintf(
				"enum value %q should be all uppercase", val.Name))
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-reference validation (DocumentSet only)
// ---------------------------------------------------------------------------

func (v *validator) validateCrossReferences(ds *ast.DocumentSet) {
	modelFields := make(map[string]map[string]bool)
	for _, doc := range ds.Documents {
		for _, m := range doc.Models {
			fields := make(map[string]bool)
			for _, f := range m.Fields {
				fields[f.Name] = true
			}
			modelFields[m.Name] = fields
		}
	}

	for _, doc := range ds.Documents {
		for _, m := range doc.Models {
			for _, f := range m.Fields {
				for _, a := range f.Attributes {
					if a.Name != "relation" {
						continue
					}
					refModel := f.Type.Name
					refFields, ok := modelFields[refModel]
					if !ok {
						continue
					}
					for _, arg := range a.Args {
						if arg.Name != "references" {
							continue
						}
						arr, ok := arg.Value.(ast.ArrayLiteral)
						if !ok {
							continue
						}
						for _, elem := range arr.Elements {
							id, ok := elem.(ast.Identifier)
							if !ok {
								continue
							}
							if !refFields[id.Name] {
								v.addError(id.Span.Start, fmt.Sprintf(
									"@relation references unknown field %q on model %q",
									id.Name, refModel))
							}
						}
					}
				}
			}
		}
	}
}
