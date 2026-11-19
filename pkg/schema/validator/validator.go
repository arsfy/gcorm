// Package validator provides semantic validation for GCORM schema ASTs.
package validator

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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
	ds := &ast.DocumentSet{Documents: []*ast.Document{doc}}
	v.validateExpressionIndexProvider(ds)
	v.validateTriggers(ds)
	v.validateCrossReferences(ds)
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
	v.validateExpressionIndexProvider(ds)
	v.validateTriggers(ds)
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
	"schema":            true,
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
	typeKinds  map[string]string
}

func newValidator() *validator {
	return &validator{
		result:     &ValidationResult{},
		modelNames: make(map[string]ast.Position),
		enumNames:  make(map[string]ast.Position),
		typeKinds:  make(map[string]string),
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
		v.registerTypeName(m.Name, "model", m.Span.Start)
		v.modelNames[m.Name] = m.Span.Start
	}
	for _, e := range doc.Enums {
		v.registerTypeName(e.Name, "enum", e.Span.Start)
		v.enumNames[e.Name] = e.Span.Start
	}
}

func (v *validator) registerTypeName(name, kind string, pos ast.Position) {
	if scalarTypes[name] {
		v.addError(pos, fmt.Sprintf("%s name %q conflicts with built-in scalar type", kind, name))
	}
	if previousKind, ok := v.typeKinds[name]; ok && previousKind != kind {
		v.addError(pos, fmt.Sprintf("type name %q is used by both a model and an enum", name))
		return
	}
	v.typeKinds[name] = kind
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
	hasProvider := false
	provider := ""
	seen := make(map[string]bool)
	hasSchema := false
	hasSchemas := false
	for _, e := range d.Entries {
		if seen[e.Key] {
			v.addError(e.Span.Start, fmt.Sprintf("duplicate datasource config key %q", e.Key))
		}
		seen[e.Key] = true
		switch e.Key {
		case "provider":
			hasProvider = true
			if sl, ok := e.Value.(ast.StringLiteral); ok {
				provider = sl.Value
				if !validProviders[sl.Value] {
					v.addError(e.Span.Start, fmt.Sprintf(
						"invalid provider %q; must be one of: postgresql, mysql, sqlite", sl.Value))
				}
			} else {
				v.addError(e.Span.Start, "datasource provider must be a string")
			}
		case "url":
			hasURL = true
			v.validateDatasourceURL(e)
		case "schema":
			hasSchema = true
			if sl, ok := e.Value.(ast.StringLiteral); !ok || strings.TrimSpace(sl.Value) == "" {
				v.addError(e.Span.Start, "datasource schema must be a non-empty string")
			}
		case "schemas":
			hasSchemas = true
			v.validateDatasourceSchemas(e)
		default:
			if !knownDatasourceKeys[e.Key] {
				v.addError(e.Span.Start, fmt.Sprintf("unknown datasource config key %q", e.Key))
			}
		}
	}
	if !hasURL {
		v.addError(d.Span.Start, "datasource block requires a \"url\" entry")
	}
	if !hasProvider {
		v.addError(d.Span.Start, "datasource block requires a \"provider\" entry")
	}
	if hasSchema && hasSchemas {
		v.addError(d.Span.Start, "datasource schema and schemas are mutually exclusive")
	}
	if (hasSchema || hasSchemas) && provider != "" && provider != "postgresql" {
		v.addError(d.Span.Start, "datasource schema/schemas are only supported by postgresql")
	}
}

func (v *validator) validateDatasourceURL(e ast.ConfigEntry) {
	switch value := e.Value.(type) {
	case ast.StringLiteral:
		if strings.TrimSpace(value.Value) == "" {
			v.addError(e.Span.Start, "datasource url must not be empty")
		}
	case ast.FunctionCall:
		if value.Name != "env" || len(value.Args) != 1 {
			v.addError(e.Span.Start, "datasource url must be a string or env(\"NAME\")")
			return
		}
		if name, ok := value.Args[0].(ast.StringLiteral); !ok || strings.TrimSpace(name.Value) == "" {
			v.addError(e.Span.Start, "env() requires exactly one non-empty string argument")
		}
	default:
		v.addError(e.Span.Start, "datasource url must be a string or env(\"NAME\")")
	}
}

func (v *validator) validateDatasourceSchemas(e ast.ConfigEntry) {
	arr, ok := e.Value.(ast.ArrayLiteral)
	if !ok || len(arr.Elements) == 0 {
		v.addError(e.Span.Start, "datasource schemas must be a non-empty string array")
		return
	}
	seen := make(map[string]bool)
	for _, elem := range arr.Elements {
		sl, ok := elem.(ast.StringLiteral)
		if !ok || strings.TrimSpace(sl.Value) == "" {
			v.addError(elem.ExprSpan().Start, "datasource schemas values must be non-empty strings")
			continue
		}
		if seen[sl.Value] {
			v.addError(elem.ExprSpan().Start, fmt.Sprintf("duplicate datasource schema %q", sl.Value))
		}
		seen[sl.Value] = true
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
	if len(m.Name) > 0 {
		first, _ := utf8.DecodeRuneInString(m.Name)
		if unicode.IsUpper(first) {
			return
		}
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

		if len(f.Name) > 0 {
			first, _ := utf8.DecodeRuneInString(f.Name)
			if !unicode.IsLower(first) {
				v.addError(f.Span.Start, fmt.Sprintf("field name %q must start with a lowercase letter", f.Name))
			}
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
	if strings.HasPrefix(a.Name, "db.") {
		v.validateNativeType(a)
		return
	}
	switch a.Name {
	case "id", "unique":
		if len(a.Args) != 0 {
			v.addError(a.Span.Start, "@"+a.Name+" does not accept arguments")
		}
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
		if len(a.Args) != 0 {
			v.addError(a.Span.Start, "@updatedAt does not accept arguments")
		}
		if f.Type.Name != "DateTime" {
			v.addError(a.Span.Start, "@updatedAt is only valid on DateTime fields")
		}
	default:
		v.addError(a.Span.Start, fmt.Sprintf("unknown field attribute @%s", a.Name))
	}
}

func (v *validator) validateNativeType(a ast.Attribute) {
	name := strings.TrimPrefix(a.Name, "db.")
	known := map[string]bool{"VarChar": true, "Char": true, "Text": true, "Decimal": true, "Json": true, "JsonB": true, "Uuid": true, "Timestamp": true, "Timestamptz": true, "Date": true, "Time": true, "Bytes": true, "Blob": true, "TinyInt": true, "SmallInt": true, "Int": true, "BigInt": true, "UnsignedInt": true, "UnsignedBigInt": true}
	if !known[name] {
		v.addError(a.Span.Start, fmt.Sprintf("unknown native type @db.%s", name))
		return
	}
	for _, arg := range a.Args {
		n, ok := arg.Value.(ast.NumberLiteral)
		if !ok || n.IsFloat {
			v.addError(arg.Span.Start, "native type arguments must be integers")
		}
	}
}

func (v *validator) validateDefaultArg(arg ast.AttributeArg, f ast.FieldDecl) {
	if fc, ok := arg.Value.(ast.FunctionCall); ok {
		v.validateDefaultFunction(fc, f)
		return
	}
	if f.Type.IsList {
		v.validateListDefaultArg(arg, f)
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

func (v *validator) validateDefaultFunction(fc ast.FunctionCall, f ast.FieldDecl) {
	requireArgs := func(n int) {
		if len(fc.Args) != n {
			v.addError(fc.Span.Start, fmt.Sprintf("%s() requires %d argument(s)", fc.Name, n))
		}
	}
	switch fc.Name {
	case "now":
		requireArgs(0)
		if f.Type.Name != "DateTime" {
			v.addError(fc.Span.Start, "now() is only valid for DateTime fields")
		}
	case "autoincrement":
		requireArgs(0)
		if f.Type.Name != "Int" && f.Type.Name != "SmallInt" && f.Type.Name != "BigInt" {
			v.addError(fc.Span.Start, "autoincrement() is only valid for integer fields")
		}
	case "uuid":
		requireArgs(0)
		if f.Type.Name != "UUID" && f.Type.Name != "String" {
			v.addError(fc.Span.Start, "uuid() is only valid for UUID or String fields")
		}
	case "cuid":
		requireArgs(0)
		if f.Type.Name != "String" {
			v.addError(fc.Span.Start, "cuid() is only valid for String fields")
		}
	case "dbgenerated":
		requireArgs(1)
		if len(fc.Args) == 1 {
			if _, ok := fc.Args[0].(ast.StringLiteral); !ok {
				v.addError(fc.Args[0].ExprSpan().Start, "dbgenerated() argument must be a string")
			}
		}
	default:
		v.addError(fc.Span.Start, fmt.Sprintf("unknown default function %q", fc.Name))
	}
}

func (v *validator) validateListDefaultArg(arg ast.AttributeArg, f ast.FieldDecl) {
	arr, ok := arg.Value.(ast.ArrayLiteral)
	if !ok {
		v.addError(arg.Span.Start, fmt.Sprintf(
			"@default value for %s[] field must be an array literal or function call", f.Type.Name))
		return
	}
	for _, elem := range arr.Elements {
		switch f.Type.Name {
		case "String", "UUID", "Bytes", "Json":
			if _, ok := elem.(ast.StringLiteral); !ok {
				v.addError(elem.ExprSpan().Start, fmt.Sprintf(
					"@default array values for %s[] field must be strings", f.Type.Name))
			}
		case "Int", "SmallInt", "BigInt", "Float", "Decimal":
			if _, ok := elem.(ast.NumberLiteral); !ok {
				v.addError(elem.ExprSpan().Start, fmt.Sprintf(
					"@default array values for %s[] field must be numbers", f.Type.Name))
			}
		case "Boolean":
			if _, ok := elem.(ast.BooleanLiteral); !ok {
				v.addError(elem.ExprSpan().Start,
					"@default array values for Boolean[] field must be booleans")
			}
		case "DateTime":
			if _, ok := elem.(ast.StringLiteral); !ok {
				v.addError(elem.ExprSpan().Start,
					"@default array values for DateTime[] field must be strings or use a function default")
			}
		}
	}
}

func (v *validator) validateRelation(a ast.Attribute, f ast.FieldDecl, m ast.ModelDecl) {
	if scalarTypes[f.Type.Name] || v.typeKinds[f.Type.Name] != "model" {
		v.addError(a.Span.Start, "@relation is only valid on fields with a model type")
	}

	hasFields := false
	hasReferences := false

	seenArgs := make(map[string]bool)
	for _, arg := range a.Args {
		key := arg.Name
		if key == "" {
			key = "name"
		}
		if seenArgs[key] {
			v.addError(arg.Span.Start, fmt.Sprintf("duplicate @relation argument %q", key))
		}
		seenArgs[key] = true
		switch arg.Name {
		case "fields":
			hasFields = true
			if arr, ok := arg.Value.(ast.ArrayLiteral); ok {
				fmap := make(map[string]bool)
				for _, ff := range m.Fields {
					fmap[ff.Name] = true
				}
				if len(arr.Elements) == 0 {
					v.addError(arg.Span.Start, "@relation fields must not be empty")
				}
				seen := make(map[string]bool)
				for _, elem := range arr.Elements {
					if id, ok := elem.(ast.Identifier); ok {
						if seen[id.Name] {
							v.addError(id.Span.Start, fmt.Sprintf("duplicate @relation field %q", id.Name))
						}
						seen[id.Name] = true
						if !fmap[id.Name] {
							v.addError(id.Span.Start, fmt.Sprintf(
								"@relation field %q does not exist in model %q", id.Name, m.Name))
						}
					} else {
						v.addError(elem.ExprSpan().Start, "@relation fields must be identifiers")
					}
				}
			} else {
				v.addError(arg.Span.Start, "@relation \"fields\" argument must be an array")
			}
		case "references":
			hasReferences = true
			if arr, ok := arg.Value.(ast.ArrayLiteral); !ok {
				v.addError(arg.Span.Start, "@relation \"references\" argument must be an array")
			} else {
				if len(arr.Elements) == 0 {
					v.addError(arg.Span.Start, "@relation references must not be empty")
				}
				seen := make(map[string]bool)
				for _, elem := range arr.Elements {
					id, ok := elem.(ast.Identifier)
					if !ok {
						v.addError(elem.ExprSpan().Start, "@relation references must be identifiers")
						continue
					}
					if seen[id.Name] {
						v.addError(id.Span.Start, fmt.Sprintf("duplicate @relation reference %q", id.Name))
					}
					seen[id.Name] = true
				}
			}
		case "onDelete", "onUpdate":
			value := indexOptionValue(arg.Value)
			valid := map[string]bool{"CASCADE": true, "RESTRICT": true, "NOACTION": true, "SETNULL": true, "SETDEFAULT": true}
			if !valid[strings.ToUpper(strings.ReplaceAll(value, "_", ""))] {
				v.addError(arg.Span.Start, fmt.Sprintf("invalid referential action %q", value))
			}
		case "", "name":
			if _, ok := arg.Value.(ast.StringLiteral); !ok {
				v.addError(arg.Span.Start, "@relation name must be a string")
			}
		default:
			v.addError(arg.Span.Start, fmt.Sprintf("unknown @relation argument %q", arg.Name))
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
	case "trigger":
		// Validated across the document set because name scope depends on the provider.
	case "id":
		v.validateConstraintFields(a, m, fieldNames, "@@id")
	case "unique":
		v.validateConstraintFields(a, m, fieldNames, "@@unique")
		if fieldArg, ok := indexFieldsArg(a); ok {
			if arr, ok := fieldArg.Value.(ast.ArrayLiteral); ok {
				v.validateIndexExpressions(a, len(arr.Elements), "@@unique")
			}
		}
		v.validateExpressionIndexName(a, "@@unique")
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
			v.validateExpressionIndexName(a, "@@index")
		} else {
			v.addError(fieldArg.Span.Start, "@@index fields argument must be an array")
		}
	case "map":
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@@map requires exactly one argument")
		} else if _, ok := a.Args[0].Value.(ast.StringLiteral); !ok {
			v.addError(a.Span.Start, "@@map argument must be a string")
		}
	case "schema":
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@@schema requires exactly one argument")
		} else if sl, ok := a.Args[0].Value.(ast.StringLiteral); !ok || strings.TrimSpace(sl.Value) == "" {
			v.addError(a.Span.Start, "@@schema argument must be a non-empty string")
		}
	default:
		v.addError(a.Span.Start, fmt.Sprintf("unknown model attribute @@%s", a.Name))
	}
}

func (v *validator) validateConstraintFields(a ast.Attribute, m ast.ModelDecl, fieldNames map[string]bool, label string) {
	fieldArg, ok := indexFieldsArg(a)
	if !ok {
		v.addError(a.Span.Start, label+" requires a fields array")
		return
	}
	arr, ok := fieldArg.Value.(ast.ArrayLiteral)
	if !ok {
		v.addError(fieldArg.Span.Start, label+" fields argument must be an array")
		return
	}
	if len(arr.Elements) == 0 {
		v.addError(fieldArg.Span.Start, label+" fields must not be empty")
	}
	seenFields := make(map[string]bool)
	seenArgs := make(map[string]bool)
	for _, arg := range a.Args {
		key := arg.Name
		if key == "" {
			key = "fields"
		}
		if seenArgs[key] {
			v.addError(arg.Span.Start, fmt.Sprintf("duplicate %s argument %q", label, key))
		}
		seenArgs[key] = true
		isUniqueExpression := label == "@@unique" && (key == "expression" || key == "expressions")
		if key != "fields" && key != "name" && !isUniqueExpression {
			v.addError(arg.Span.Start, fmt.Sprintf("unknown %s argument %q", label, key))
		}
		if key == "name" {
			if _, ok := arg.Value.(ast.StringLiteral); !ok {
				v.addError(arg.Span.Start, label+" name must be a string")
			}
		}
	}
	expressions := attributeIndexExpressions(a, len(arr.Elements))
	for i, elem := range arr.Elements {
		id, ok := elem.(ast.Identifier)
		if !ok {
			v.addError(elem.ExprSpan().Start, label+" fields must be identifiers")
			continue
		}
		if !fieldNames[id.Name] {
			v.addError(id.Span.Start, fmt.Sprintf("%s references unknown field %q in model %q", label, id.Name, m.Name))
		}
		isExpressionItem := i < len(expressions) && strings.TrimSpace(expressions[i]) != ""
		if seenFields[id.Name] && !isExpressionItem {
			v.addError(id.Span.Start, fmt.Sprintf("%s contains duplicate field %q", label, id.Name))
		}
		seenFields[id.Name] = true
	}
}

func (v *validator) validateExpressionIndexName(a ast.Attribute, label string) {
	if !attributeHasIndexExpression(a) {
		return
	}
	for _, arg := range a.Args {
		if arg.Name != "name" {
			continue
		}
		if value, ok := arg.Value.(ast.StringLiteral); ok && strings.TrimSpace(value.Value) != "" {
			return
		}
	}
	v.addError(a.Span.Start, label+" with expressions requires an explicit non-empty name")
}

func attributeHasIndexExpression(a ast.Attribute) bool {
	for _, expression := range attributeIndexExpressions(a, 0) {
		if strings.TrimSpace(expression) != "" {
			return true
		}
	}
	return false
}

func attributeIndexExpressions(a ast.Attribute, fieldCount int) []string {
	for _, arg := range a.Args {
		if arg.Name != "expression" && arg.Name != "expressions" {
			continue
		}
		values := indexOptionExprs(arg.Value)
		out := make([]string, len(values))
		for i, value := range values {
			if literal, ok := value.(ast.StringLiteral); ok {
				out[i] = literal.Value
			}
		}
		if len(out) == 1 && fieldCount > 1 {
			repeated := make([]string, fieldCount)
			for i := range repeated {
				repeated[i] = out[0]
			}
			return repeated
		}
		return out
	}
	return nil
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
		case "expression", "expressions":
			v.validateIndexExpressionArg(arg, fieldCount, "@@index")
		default:
			v.addWarning(arg.Span.Start, fmt.Sprintf("unknown @@index argument %q", arg.Name))
		}
	}
}

func (v *validator) validateIndexExpressions(a ast.Attribute, fieldCount int, label string) {
	for _, arg := range a.Args {
		if arg.Name == "expression" || arg.Name == "expressions" {
			v.validateIndexExpressionArg(arg, fieldCount, label)
		}
	}
}

func (v *validator) validateIndexExpressionArg(arg ast.AttributeArg, fieldCount int, label string) {
	values := indexOptionExprs(arg.Value)
	if len(values) != 1 && len(values) != fieldCount {
		v.addError(arg.Span.Start, fmt.Sprintf("%s %s option has %d value(s), expected 1 or %d", label, arg.Name, len(values), fieldCount))
		return
	}
	hasExpression := false
	for _, expr := range values {
		literal, ok := expr.(ast.StringLiteral)
		if !ok {
			v.addError(expr.ExprSpan().Start, label+" "+arg.Name+" values must be strings")
			continue
		}
		if strings.TrimSpace(literal.Value) != "" {
			hasExpression = true
		}
	}
	if !hasExpression {
		v.addError(arg.Span.Start, label+" "+arg.Name+" must contain at least one non-empty expression")
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
	if len(e.Name) > 0 {
		first, _ := utf8.DecodeRuneInString(e.Name)
		if unicode.IsUpper(first) {
			return
		}
		v.addError(e.Span.Start, fmt.Sprintf("enum name %q must start with an uppercase letter", e.Name))
	}
}

func (v *validator) validateEnum(e ast.EnumDecl) {
	seen := make(map[string]bool)
	for _, a := range e.Attributes {
		if a.Name != "map" {
			v.addError(a.Span.Start, fmt.Sprintf("unknown enum attribute @@%s", a.Name))
			continue
		}
		if len(a.Args) != 1 {
			v.addError(a.Span.Start, "@@map requires exactly one argument")
		} else if _, ok := a.Args[0].Value.(ast.StringLiteral); !ok {
			v.addError(a.Span.Start, "@@map argument must be a string")
		}
	}
	for _, val := range e.Values {
		if seen[val.Name] {
			v.addError(val.Span.Start, fmt.Sprintf(
				"duplicate enum value %q in enum %q", val.Name, e.Name))
		}
		seen[val.Name] = true
		for _, a := range val.Attributes {
			if a.Name != "map" {
				v.addError(a.Span.Start, fmt.Sprintf("unknown enum value attribute @%s", a.Name))
			} else if len(a.Args) != 1 {
				v.addError(a.Span.Start, "@map requires exactly one argument")
			} else if _, ok := a.Args[0].Value.(ast.StringLiteral); !ok {
				v.addError(a.Span.Start, "@map argument must be a string")
			}
		}

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
	modelFields := make(map[string]map[string]ast.FieldDecl)
	modelUnique := make(map[string]map[string]bool)
	for _, doc := range ds.Documents {
		for _, m := range doc.Models {
			fields := make(map[string]ast.FieldDecl)
			for _, f := range m.Fields {
				fields[f.Name] = f
			}
			modelFields[m.Name] = fields
			unique := make(map[string]bool)
			for _, f := range m.Fields {
				for _, a := range f.Attributes {
					if a.Name == "id" || a.Name == "unique" {
						unique[f.Name] = true
					}
				}
			}
			for _, a := range m.Attributes {
				if a.Name != "id" && a.Name != "unique" {
					continue
				}
				if a.Name == "unique" && attributeHasIndexExpression(a) {
					continue
				}
				if arg, ok := indexFieldsArg(a); ok {
					if arr, ok := arg.Value.(ast.ArrayLiteral); ok {
						var names []string
						for _, e := range arr.Elements {
							if id, ok := e.(ast.Identifier); ok {
								names = append(names, id.Name)
							}
						}
						unique[strings.Join(names, ",")] = true
					}
				}
			}
			modelUnique[m.Name] = unique
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
					var localNames, refNames []string
					var onDelete string
					for _, arg := range a.Args {
						if arg.Name == "onDelete" {
							onDelete = indexOptionValue(arg.Value)
						}
						if arg.Name != "references" && arg.Name != "fields" {
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
							if arg.Name == "fields" {
								localNames = append(localNames, id.Name)
								continue
							}
							refNames = append(refNames, id.Name)
							if _, exists := refFields[id.Name]; !exists {
								v.addError(id.Span.Start, fmt.Sprintf(
									"@relation references unknown field %q on model %q",
									id.Name, refModel))
							}
						}
					}
					if len(localNames) != len(refNames) {
						v.addError(a.Span.Start, fmt.Sprintf("@relation fields and references must have the same length (got %d and %d)", len(localNames), len(refNames)))
					}
					for i := 0; i < len(localNames) && i < len(refNames); i++ {
						lf, lok := modelFields[m.Name][localNames[i]]
						rf, rok := refFields[refNames[i]]
						if lok && rok && (lf.Type.Name != rf.Type.Name || lf.Type.IsList || rf.Type.IsList) {
							v.addError(a.Span.Start, fmt.Sprintf("@relation field %q type %s does not match referenced field %q type %s", localNames[i], lf.Type.String(), refNames[i], rf.Type.String()))
						}
						if strings.EqualFold(strings.ReplaceAll(onDelete, "_", ""), "SetNull") && lok && !lf.Type.IsOptional {
							v.addError(a.Span.Start, "onDelete: SetNull requires optional relation fields")
						}
					}
					if len(refNames) > 0 && !modelUnique[refModel][strings.Join(refNames, ",")] {
						v.addError(a.Span.Start, fmt.Sprintf("@relation references on model %q must match an id or unique constraint", refModel))
					}
				}
			}
		}
	}
}

func datasourceProvider(ds *ast.DocumentSet) string {
	for _, doc := range ds.Documents {
		for _, datasource := range doc.Datasources {
			for _, entry := range datasource.Entries {
				if entry.Key == "provider" {
					if value, ok := entry.Value.(ast.StringLiteral); ok {
						return value.Value
					}
				}
			}
		}
	}
	return "postgresql"
}

func (v *validator) validateExpressionIndexProvider(ds *ast.DocumentSet) {
	if provider := datasourceProvider(ds); provider == "" || provider == "postgresql" {
		return
	}
	for _, doc := range ds.Documents {
		for _, model := range doc.Models {
			for _, attr := range model.Attributes {
				if (attr.Name == "index" || attr.Name == "unique") && attributeHasIndexExpression(attr) {
					v.addError(attr.Span.Start, fmt.Sprintf("@@%s expressions are only supported by postgresql", attr.Name))
				}
			}
		}
	}
}
