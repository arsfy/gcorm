// Package resolve transforms an AST DocumentSet into a fully resolved Schema IR.
// It resolves type references, extracts attributes, and builds relation metadata.
package resolve

import (
	"fmt"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// Known scalar type names.
var knownScalars = map[string]bool{
	"String":   true,
	"Int":      true,
	"BigInt":   true,
	"Float":    true,
	"Decimal":  true,
	"Boolean":  true,
	"DateTime": true,
	"Bytes":    true,
	"Json":     true,
	"UUID":     true,
}

// ResolveError represents a resolution error.
type ResolveError struct {
	Message string
	Pos     ast.Position
}

func (e *ResolveError) Error() string {
	if e.Pos.File != "" {
		return fmt.Sprintf("%s:%d:%d: %s", e.Pos.File, e.Pos.Line, e.Pos.Column, e.Message)
	}
	return e.Message
}

// Resolve takes a DocumentSet and resolves all references to produce a Schema IR.
func Resolve(ds *ast.DocumentSet) (*ir.Schema, []error) {
	r := &resolver{
		enumNames:  map[string]bool{},
		modelNames: map[string]bool{},
	}
	return r.resolve(ds)
}

type resolver struct {
	enumNames  map[string]bool
	modelNames map[string]bool
	errors     []error
}

func (r *resolver) addError(msg string, pos ast.Position) {
	r.errors = append(r.errors, &ResolveError{Message: msg, Pos: pos})
}

func (r *resolver) resolve(ds *ast.DocumentSet) (*ir.Schema, []error) {
	schema := &ir.Schema{}

	// Phase 1: collect all type names into symbol table.
	for _, doc := range ds.Documents {
		for _, m := range doc.Models {
			r.modelNames[m.Name] = true
		}
		for _, e := range doc.Enums {
			r.enumNames[e.Name] = true
		}
	}

	// Phase 2: resolve each top-level block.
	for _, doc := range ds.Documents {
		for i := range doc.Datasources {
			schema.Datasource = r.resolveDatasource(&doc.Datasources[i])
		}
		for i := range doc.Generators {
			schema.Generators = append(schema.Generators, r.resolveGenerator(&doc.Generators[i]))
		}
		for i := range doc.Models {
			schema.Models = append(schema.Models, r.resolveModel(&doc.Models[i]))
		}
		for i := range doc.Enums {
			schema.Enums = append(schema.Enums, r.resolveEnum(&doc.Enums[i]))
		}
	}

	return schema, r.errors
}

// ---------------------------------------------------------------------------
// Datasource
// ---------------------------------------------------------------------------

func (r *resolver) resolveDatasource(d *ast.DatasourceDecl) *ir.Datasource {
	ds := &ir.Datasource{
		Name: d.Name,
		Span: d.Span,
	}
	for _, entry := range d.Entries {
		switch entry.Key {
		case "provider":
			ds.Provider = exprToString(entry.Value)
		case "url":
			if fc, ok := entry.Value.(ast.FunctionCall); ok && fc.Name == "env" {
				ds.URLIsEnv = true
				if len(fc.Args) > 0 {
					ds.EnvVar = exprToString(fc.Args[0])
					ds.URL = "env(" + ds.EnvVar + ")"
				}
			} else {
				ds.URL = exprToString(entry.Value)
			}
		case "schema", "schemas":
			ds.Schema = exprToString(entry.Value)
		}
	}
	return ds
}

// ---------------------------------------------------------------------------
// Generator
// ---------------------------------------------------------------------------

func (r *resolver) resolveGenerator(g *ast.GeneratorDecl) *ir.Generator {
	gen := &ir.Generator{
		Name: g.Name,
		Span: g.Span,
	}
	for _, entry := range g.Entries {
		switch entry.Key {
		case "provider":
			gen.Provider = exprToString(entry.Value)
		case "output":
			gen.Output = exprToString(entry.Value)
		case "package":
			gen.Package = exprToString(entry.Value)
		case "emitRuntime":
			gen.EmitRuntime = exprToBool(entry.Value)
		}
	}
	return gen
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

func (r *resolver) resolveModel(m *ast.ModelDecl) *ir.Model {
	model := &ir.Model{
		Name: m.Name,
		Span: m.Span,
	}

	// Resolve model-level attributes first.
	for _, attr := range m.Attributes {
		switch attr.Name {
		case "map":
			if len(attr.Args) > 0 {
				model.DBName = exprToString(attr.Args[0].Value)
			}
		case "schema":
			if len(attr.Args) > 0 {
				model.Schema = exprToString(attr.Args[0].Value)
			}
		case "id":
			model.PrimaryKey = r.resolveCompositePK(attr)
		case "index":
			model.Indexes = append(model.Indexes, r.resolveIndex(attr, false))
		case "unique":
			idx := r.resolveIndex(attr, true)
			model.UniqueConstraints = append(model.UniqueConstraints, &ir.UniqueConstraint{
				Name:   idx.Name,
				Fields: idx.Fields,
			})
		}
	}

	// Resolve fields.
	for i := range m.Fields {
		field := r.resolveField(&m.Fields[i])
		model.Fields = append(model.Fields, field)

		// Track single-field @id.
		if field.IsID && model.PrimaryKey == nil {
			model.PrimaryKey = &ir.PrimaryKey{
				Fields:      []string{field.Name},
				IsComposite: false,
			}
		}

		// Build relation entry if the field references a model.
		if field.Type == ir.FieldKindRelation {
			rel := r.buildRelation(field, m.Name, &m.Fields[i])
			model.Relations = append(model.Relations, rel)
		}
	}

	return model
}

func (r *resolver) resolveField(f *ast.FieldDecl) *ir.Field {
	field := &ir.Field{
		Name:       f.Name,
		IsOptional: f.Type.IsOptional,
		IsList:     f.Type.IsList,
		Span:       f.Span,
	}

	typeName := f.Type.Name
	switch {
	case knownScalars[typeName]:
		field.Type = ir.FieldKindScalar
		field.ScalarType = typeName
	case r.enumNames[typeName]:
		field.Type = ir.FieldKindEnum
		field.EnumType = typeName
	case r.modelNames[typeName]:
		field.Type = ir.FieldKindRelation
		field.ModelType = typeName
	default:
		r.addError(fmt.Sprintf("unknown type %q for field %q", typeName, f.Name), f.Span.Start)
		field.Type = ir.FieldKindScalar
		field.ScalarType = typeName
	}

	// Process field-level attributes.
	for _, attr := range f.Attributes {
		r.applyFieldAttribute(field, attr)
	}

	return field
}

func (r *resolver) applyFieldAttribute(field *ir.Field, attr ast.Attribute) {
	// Handle @db.* native type attributes.
	if strings.HasPrefix(attr.Name, "db.") {
		nativeName := strings.TrimPrefix(attr.Name, "db.")
		nt := &ir.NativeType{Name: nativeName}
		for _, arg := range attr.Args {
			nt.Args = append(nt.Args, exprToString(arg.Value))
		}
		field.NativeType = nt
		return
	}

	switch attr.Name {
	case "id":
		field.IsID = true
	case "unique":
		field.IsUnique = true
	case "updatedAt":
		field.IsUpdatedAt = true
	case "map":
		if len(attr.Args) > 0 {
			field.DBName = exprToString(attr.Args[0].Value)
		}
	case "default":
		if len(attr.Args) > 0 {
			field.Default = r.resolveDefault(attr.Args[0].Value)
		}
	case "relation":
		// handled at model level via buildRelation
	}
}

func (r *resolver) resolveDefault(expr ast.Expression) *ir.DefaultValue {
	dv := &ir.DefaultValue{}
	switch v := expr.(type) {
	case ast.FunctionCall:
		dv.IsFunction = true
		dv.FuncName = v.Name
		for _, arg := range v.Args {
			dv.FuncArgs = append(dv.FuncArgs, exprToString(arg))
		}
	case ast.StringLiteral:
		dv.Value = v.Value
		dv.IsString = true
	case ast.NumberLiteral:
		dv.Value = v.Value
		dv.IsNumber = true
	case ast.BooleanLiteral:
		dv.Value = fmt.Sprintf("%t", v.Value)
		dv.IsBool = true
	case ast.Identifier:
		// Enum value reference or bare identifier (e.g. autoincrement).
		dv.Value = v.Name
	}
	return dv
}

// ---------------------------------------------------------------------------
// Relations
// ---------------------------------------------------------------------------

func (r *resolver) buildRelation(field *ir.Field, fromModel string, fd *ast.FieldDecl) *ir.Relation {
	rel := &ir.Relation{
		Field:     field,
		FromModel: fromModel,
		ToModel:   field.ModelType,
	}

	// Determine relation type from modifiers.
	switch {
	case field.IsList:
		rel.Type = ir.RelationOneToMany
	case field.IsOptional:
		rel.Type = ir.RelationOneToOne
	default:
		rel.Type = ir.RelationManyToOne
	}

	// Extract @relation arguments.
	for _, attr := range fd.Attributes {
		if attr.Name != "relation" {
			continue
		}
		for _, arg := range attr.Args {
			switch arg.Name {
			case "":
				// Positional argument is the relation name.
				rel.Name = exprToString(arg.Value)
			case "name":
				rel.Name = exprToString(arg.Value)
			case "fields":
				rel.Fields = exprToStringSlice(arg.Value)
			case "references":
				rel.References = exprToStringSlice(arg.Value)
			case "onDelete":
				rel.OnDelete = exprToString(arg.Value)
			case "onUpdate":
				rel.OnUpdate = exprToString(arg.Value)
			}
		}
	}

	return rel
}

// ---------------------------------------------------------------------------
// Indexes / PrimaryKey
// ---------------------------------------------------------------------------

func (r *resolver) resolveCompositePK(attr ast.Attribute) *ir.PrimaryKey {
	pk := &ir.PrimaryKey{IsComposite: true}
	for _, arg := range attr.Args {
		if arg.Name == "" || arg.Name == "fields" {
			pk.Fields = exprToStringSlice(arg.Value)
		}
	}
	return pk
}

func (r *resolver) resolveIndex(attr ast.Attribute, isUnique bool) *ir.Index {
	idx := &ir.Index{IsUnique: isUnique, Span: attr.Span}
	for _, arg := range attr.Args {
		switch arg.Name {
		case "", "fields":
			idx.Fields = exprToStringSlice(arg.Value)
		case "name":
			idx.Name = exprToString(arg.Value)
		}
	}
	return idx
}

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

func (r *resolver) resolveEnum(e *ast.EnumDecl) *ir.Enum {
	enum := &ir.Enum{
		Name: e.Name,
		Span: e.Span,
	}
	for _, attr := range e.Attributes {
		if attr.Name == "map" && len(attr.Args) > 0 {
			enum.DBName = exprToString(attr.Args[0].Value)
		}
	}
	for _, v := range e.Values {
		ev := &ir.EnumValue{Name: v.Name}
		for _, attr := range v.Attributes {
			if attr.Name == "map" && len(attr.Args) > 0 {
				ev.DBName = exprToString(attr.Args[0].Value)
			}
		}
		enum.Values = append(enum.Values, ev)
	}
	return enum
}

// ---------------------------------------------------------------------------
// Expression helpers
// ---------------------------------------------------------------------------

func exprToString(expr ast.Expression) string {
	switch v := expr.(type) {
	case ast.StringLiteral:
		return v.Value
	case ast.NumberLiteral:
		return v.Value
	case ast.BooleanLiteral:
		return fmt.Sprintf("%t", v.Value)
	case ast.Identifier:
		return v.Name
	case ast.FunctionCall:
		return v.Name + "(...)"
	default:
		return ""
	}
}

func exprToBool(expr ast.Expression) bool {
	if b, ok := expr.(ast.BooleanLiteral); ok {
		return b.Value
	}
	return exprToString(expr) == "true"
}

func exprToStringSlice(expr ast.Expression) []string {
	switch v := expr.(type) {
	case ast.ArrayLiteral:
		var out []string
		for _, el := range v.Elements {
			out = append(out, exprToString(el))
		}
		return out
	case ast.Identifier:
		return []string{v.Name}
	default:
		s := exprToString(expr)
		if s != "" {
			return []string{s}
		}
		return nil
	}
}
