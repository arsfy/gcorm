// Package ir defines the resolved intermediate representation (IR) of a GCORM
// schema. The IR is produced by the resolve stage and consumed by code generators.
package ir

import "github.com/arsfy/gcorm/pkg/schema/ast"

// Schema is the fully resolved schema graph, the output of the compiler pipeline.
type Schema struct {
	Datasource *Datasource
	Generators []*Generator
	Models     []*Model
	Enums      []*Enum
}

// Datasource holds the resolved database connection configuration.
type Datasource struct {
	Name     string
	Provider string // "postgresql", "mysql", "sqlite"
	URL      string // raw URL or env() reference
	URLIsEnv bool   // true if URL comes from env()
	EnvVar   string // environment variable name if URLIsEnv
	Schema   string // database schema/namespace
	Span     ast.Span
}

// Generator holds the resolved code generation configuration.
type Generator struct {
	Name        string
	Provider    string
	Output      string
	Package     string
	EmitRuntime bool
	Span        ast.Span
}

// Model holds the resolved definition of a data model.
type Model struct {
	Name              string
	DBName            string // from @@map, defaults to model name
	Schema            string // from @@schema
	Fields            []*Field
	Relations         []*Relation
	Indexes           []*Index
	PrimaryKey        *PrimaryKey
	UniqueConstraints []*UniqueConstraint
	Span              ast.Span
}

// TableName returns the database table name (DBName if set, otherwise Name).
func (m *Model) TableName() string {
	if m.DBName != "" {
		return m.DBName
	}
	return m.Name
}

// ScalarFields returns only non-relation fields.
func (m *Model) ScalarFields() []*Field {
	var out []*Field
	for _, f := range m.Fields {
		if f.Type != FieldKindRelation {
			out = append(out, f)
		}
	}
	return out
}

// Field holds the resolved definition of a single model field.
type Field struct {
	Name        string
	DBName      string // from @map, defaults to field name
	Type        FieldKind
	ScalarType  string // "String", "Int", etc. for scalar fields
	EnumType    string // enum name for enum fields
	ModelType   string // model name for relation fields
	IsOptional  bool
	IsList      bool
	IsID        bool // has @id
	IsUnique    bool // has @unique
	IsUpdatedAt bool // has @updatedAt
	Default     *DefaultValue
	NativeType  *NativeType // from @db.*
	Span        ast.Span
}

// FieldKind classifies a field's type category.
type FieldKind int

const (
	FieldKindScalar   FieldKind = iota // built-in scalar type
	FieldKindEnum                      // references an enum
	FieldKindRelation                  // references another model
)

// DefaultValue holds the resolved @default value for a field.
type DefaultValue struct {
	IsFunction bool
	FuncName   string // e.g. "uuid", "now", "autoincrement"
	FuncArgs   []string
	Value      string // literal value for non-function defaults
	IsString   bool
	IsNumber   bool
	IsBool     bool
}

// NativeType holds a provider-specific type annotation from @db.*.
type NativeType struct {
	Name string   // e.g. "VarChar"
	Args []string // e.g. ["255"]
}

// Relation holds the resolved relationship between two models.
type Relation struct {
	Name       string // relation name (optional)
	Field      *Field // the field that declared @relation
	Type       RelationType
	FromModel  string
	ToModel    string
	Fields     []string // local fields (from `fields:`)
	References []string // remote fields (from `references:`)
	OnDelete   string
	OnUpdate   string
}

// RelationType classifies the cardinality of a relation.
type RelationType int

const (
	RelationOneToOne RelationType = iota
	RelationOneToMany
	RelationManyToOne
	RelationManyToMany
)

// Index holds a resolved model-level index (@@index or @@unique).
type Index struct {
	Name     string
	Fields   []string
	Columns  []IndexColumn
	Where    string
	IsUnique bool
	Span     ast.Span
}

// IndexColumn holds per-column options for a model-level index.
type IndexColumn struct {
	Field     string
	Sort      string // "ASC" or "DESC"
	Nulls     string // "FIRST" or "LAST"
	OpClass   string // PostgreSQL operator class, e.g. int8_ops
	Collation string // Collation name, e.g. pg_catalog.default
}

// PrimaryKey holds the resolved primary key for a model.
type PrimaryKey struct {
	Fields      []string
	IsComposite bool
}

// UniqueConstraint holds a resolved model-level unique constraint (@@unique).
type UniqueConstraint struct {
	Name   string
	Fields []string
}

// Enum holds the resolved definition of an enumeration type.
type Enum struct {
	Name   string
	DBName string // from @@map
	Values []*EnumValue
	Span   ast.Span
}

// EnumValue holds a single resolved enum member.
type EnumValue struct {
	Name   string
	DBName string // from @map
}

// DialectType holds dialect-specific type information.
type DialectType struct {
	SQLType    string
	GoType     string
	NullGoType string
	IsNullable bool
}

// GenerationManifest records what was generated and from what.
type GenerationManifest struct {
	SchemaFiles []string
	Models      []string
	Enums       []string
	Generator   string
	Output      string
	Package     string
}
