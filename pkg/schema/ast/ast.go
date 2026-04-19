// Package ast defines the abstract syntax tree types for the GCO ORM schema DSL.
//
// The DSL is Prisma-like and supports four top-level block types:
//   - datasource: database connection configuration
//   - generator: code generation configuration
//   - model: data model definitions with fields, types, and attributes
//   - enum: enumeration type definitions
//
// Every AST node carries a [Span] for precise source location tracking.
package ast

import "fmt"

// ---------------------------------------------------------------------------
// Position & Source Info
// ---------------------------------------------------------------------------

// Position represents a single point in a source file.
type Position struct {
	File   string // Source file path (may be empty for in-memory sources).
	Line   int    // 1-based line number.
	Column int    // 1-based column number (in bytes).
	Offset int    // 0-based byte offset from the start of the file.
}

// Span represents a contiguous range of text in a source file.
type Span struct {
	Start Position // Inclusive start position.
	End   Position // Exclusive end position.
}

// ---------------------------------------------------------------------------
// Comment
// ---------------------------------------------------------------------------

// Comment represents a single-line comment in the schema source.
type Comment struct {
	Text  string // The comment text, excluding the leading // or ///.
	IsDoc bool   // True for doc-comments (///) that attach to declarations.
	Span  Span
}

// ---------------------------------------------------------------------------
// Top-level containers
// ---------------------------------------------------------------------------

// Document is the root AST node produced by parsing a single schema file.
type Document struct {
	Datasources []DatasourceDecl // datasource blocks.
	Generators  []GeneratorDecl  // generator blocks.
	Models      []ModelDecl      // model blocks.
	Enums       []EnumDecl       // enum blocks.
	Comments    []Comment        // Free-floating comments not attached to any declaration.
}

// DocumentSet aggregates multiple parsed schema files into a single unit,
// enabling multi-file schema definitions.
type DocumentSet struct {
	Documents []*Document // Parsed documents in the order they were added.
	Files     []string    // Corresponding file paths for each document.
}

// ---------------------------------------------------------------------------
// Datasource
// ---------------------------------------------------------------------------

// DatasourceDecl represents a `datasource` block that configures the database
// connection (provider, url, schema, etc.).
type DatasourceDecl struct {
	Name     string        // Block name, e.g. "db".
	Entries  []ConfigEntry // Key-value configuration entries.
	Span     Span
	Comments []Comment // Comments inside the block.
}

// ConfigEntry is a key = value pair used inside datasource and generator blocks.
type ConfigEntry struct {
	Key   string     // Entry key, e.g. "provider", "url".
	Value Expression // Entry value expression.
	Span  Span
}

// ---------------------------------------------------------------------------
// Generator
// ---------------------------------------------------------------------------

// GeneratorDecl represents a `generator` block that configures code generation
// (provider, output, package, emitRuntime, etc.).
type GeneratorDecl struct {
	Name     string        // Block name, e.g. "client".
	Entries  []ConfigEntry // Key-value configuration entries.
	Span     Span
	Comments []Comment // Comments inside the block.
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// ModelDecl represents a `model` block defining a data model with typed fields
// and optional model-level attributes (@@-prefixed).
type ModelDecl struct {
	Name       string      // Model name, e.g. "User".
	Fields     []FieldDecl // Field declarations in source order.
	Attributes []Attribute // Model-level attributes, e.g. @@id, @@unique.
	Span       Span
	Comments   []Comment // Comments inside the block.
}

// FieldDecl represents a single field inside a model block.
type FieldDecl struct {
	Name       string      // Field name, e.g. "email".
	Type       FieldType   // The field's type information.
	Attributes []Attribute // Field-level attributes, e.g. @id, @default.
	Span       Span
	Comments   []Comment // Trailing or preceding comments attached to this field.
}

// FieldType describes the type of a model field, including optional/list modifiers.
type FieldType struct {
	Name       string // Base type name, e.g. "String", "Int", "Post".
	IsOptional bool   // True when the field type ends with "?".
	IsList     bool   // True when the field type ends with "[]".
	Span       Span
}

// String returns the type as it would appear in the schema source,
// e.g. "String", "String?", "Post[]".
func (ft FieldType) String() string {
	switch {
	case ft.IsList:
		return fmt.Sprintf("%s[]", ft.Name)
	case ft.IsOptional:
		return fmt.Sprintf("%s?", ft.Name)
	default:
		return ft.Name
	}
}

// ---------------------------------------------------------------------------
// Enum
// ---------------------------------------------------------------------------

// EnumDecl represents an `enum` block defining a set of named constants.
type EnumDecl struct {
	Name       string      // Enum name, e.g. "Role".
	Values     []EnumValue // Enum value entries in source order.
	Attributes []Attribute // Enum-level attributes.
	Span       Span
	Comments   []Comment // Comments inside the block.
}

// EnumValue represents a single value inside an enum block.
type EnumValue struct {
	Name       string      // Value name, e.g. "ADMIN".
	Attributes []Attribute // Value-level attributes, e.g. @map.
	Span       Span
	Comments   []Comment // Trailing or preceding comments attached to this value.
}

// ---------------------------------------------------------------------------
// Attributes
// ---------------------------------------------------------------------------

// Attribute represents a field-level (@) or model-level (@@) attribute.
type Attribute struct {
	Name         string         // Attribute name without the @ prefix, e.g. "id", "unique".
	IsModelLevel bool           // True for @@-prefixed (model/enum-level) attributes.
	Args         []AttributeArg // Positional and named arguments.
	Span         Span
}

// AttributeArg represents a single argument passed to an attribute.
type AttributeArg struct {
	Name  string     // Argument name for named args (empty for positional args).
	Value Expression // Argument value expression.
	Span  Span
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// Expression is the interface implemented by all expression AST nodes.
// Expressions appear as attribute arguments and config entry values.
type Expression interface {
	expressionNode()  // Marker method to restrict the interface to this package.
	ExprSpan() Span   // Returns the source span of the expression.
}

// StringLiteral represents a quoted string value, e.g. "hello".
type StringLiteral struct {
	Value string // The string content without surrounding quotes.
	Span  Span
}

func (StringLiteral) expressionNode()     {}
func (s StringLiteral) ExprSpan() Span    { return s.Span }

// NumberLiteral represents a numeric value (integer or floating-point).
type NumberLiteral struct {
	Value   string // Raw numeric text, preserving the original representation.
	IsFloat bool   // True when the literal contains a decimal point or exponent.
	Span    Span
}

func (NumberLiteral) expressionNode()     {}
func (n NumberLiteral) ExprSpan() Span    { return n.Span }

// BooleanLiteral represents a boolean value (true or false).
type BooleanLiteral struct {
	Value bool
	Span  Span
}

func (BooleanLiteral) expressionNode()    {}
func (b BooleanLiteral) ExprSpan() Span   { return b.Span }

// Identifier represents a name reference, possibly with dotted path segments,
// e.g. "autoincrement" or "db.uuid".
type Identifier struct {
	Name  string   // The full identifier text, e.g. "db.uuid".
	Parts []string // Individual segments split on ".", e.g. ["db", "uuid"].
	Span  Span
}

func (Identifier) expressionNode()        {}
func (i Identifier) ExprSpan() Span       { return i.Span }

// FunctionCall represents a function invocation, e.g. env("DATABASE_URL")
// or autoincrement().
type FunctionCall struct {
	Name string       // Function name, e.g. "env", "autoincrement".
	Args []Expression // Positional arguments.
	Span Span
}

func (FunctionCall) expressionNode()      {}
func (f FunctionCall) ExprSpan() Span     { return f.Span }

// ArrayLiteral represents a bracketed list of expressions, e.g. [1, 2, 3].
type ArrayLiteral struct {
	Elements []Expression // Elements of the array in source order.
	Span     Span
}

func (ArrayLiteral) expressionNode()      {}
func (a ArrayLiteral) ExprSpan() Span     { return a.Span }
