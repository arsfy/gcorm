package introspect

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	runtimedialect "github.com/arsfy/gcorm/pkg/runtime/dialect"
	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/formatter"
	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// builtinScalarTypes are reserved type names; generated model and enum names
// must not collide with them.
var builtinScalarTypes = []string{"String", "Int", "SmallInt", "BigInt", "Float", "Decimal", "Boolean", "DateTime", "Bytes", "Json", "UUID"}

// enumNaming maps a database enum type to its schema enum declaration.
type enumNaming struct {
	name   string
	values map[string]string // database label -> schema value name
}

type relationField struct {
	fk        ForeignKeyInfo
	fieldName string
	refModel  string
	local     []string
	refs      []string
}

type backRelationField struct {
	fieldName string
	typ       string
}

// schemaNames holds every schema identifier chosen for the introspected
// tables, keeping model/enum names and per-model field names unique.
type schemaNames struct {
	models        map[string]string            // table -> model name
	fields        map[string]map[string]string // table -> column -> field name
	enums         map[string]*enumNaming       // database enum name -> naming
	relations     map[string][]relationField   // table -> forward relations
	backRelations map[string][]backRelationField
}

func newSchemaNames(tables []TableInfo, enums []EnumInfo) *schemaNames {
	n := &schemaNames{
		models:        make(map[string]string),
		fields:        make(map[string]map[string]string),
		enums:         make(map[string]*enumNaming),
		relations:     make(map[string][]relationField),
		backRelations: make(map[string][]backRelationField),
	}
	usedTypes := make(map[string]bool)
	for _, scalar := range builtinScalarTypes {
		usedTypes[scalar] = true
	}
	usedFields := make(map[string]map[string]bool)
	for _, t := range tables {
		n.models[t.Name] = uniqueName(sanitizeModelName(t.Name), usedTypes)
		used := make(map[string]bool)
		usedFields[t.Name] = used
		n.fields[t.Name] = make(map[string]string)
		for _, col := range t.Columns {
			n.fields[t.Name][col.Name] = uniqueName(sanitizeFieldName(col.Name), used)
		}
	}
	for _, enum := range enums {
		naming := &enumNaming{name: uniqueName(sanitizeModelName(enum.Name), usedTypes), values: make(map[string]string)}
		usedValues := make(map[string]bool)
		for _, label := range enum.Values {
			naming.values[label] = uniqueName(sanitizeEnumValueName(label), usedValues)
		}
		n.enums[enum.Name] = naming
	}

	tablesByName := make(map[string]TableInfo, len(tables))
	for _, t := range tables {
		tablesByName[t.Name] = t
	}
	for _, t := range tables {
		var mappable []relationField
		fkCount := make(map[string]int)
		for _, fk := range t.FKs {
			local := mappedNames(n.fields[t.Name], fk.Columns)
			refs := mappedNames(n.fields[fk.RefTable], fk.RefColumns)
			if len(local) == 0 || len(local) != len(fk.Columns) || len(local) != len(refs) {
				continue
			}
			refModel := n.models[fk.RefTable]
			if refModel == "" {
				refModel = sanitizeModelName(fk.RefTable)
			}
			mappable = append(mappable, relationField{fk: fk, refModel: refModel, local: local, refs: refs})
			fkCount[fk.RefTable]++
		}
		for _, rel := range mappable {
			base := sanitizeFieldName(rel.fk.RefTable)
			if fkCount[rel.fk.RefTable] > 1 || rel.fk.RefTable == t.Name {
				// Several relations to one table, or a self-relation: name
				// each after its columns so names stay distinct and readable.
				base = relationFieldBase(rel.fk)
			}
			rel.fieldName = uniqueName(base, usedFields[t.Name])
			n.relations[t.Name] = append(n.relations[t.Name], rel)
		}
	}
	// Back-relation fields go on the referenced model after all forward
	// relation names are reserved, so they never collide with them.
	for _, t := range tables {
		for _, rel := range n.relations[t.Name] {
			target, ok := tablesByName[rel.fk.RefTable]
			if !ok {
				continue
			}
			base := sanitizeFieldName(t.Name)
			if len(n.relationsBetween(t.Name, target.Name)) > 1 || t.Name == target.Name {
				base = sanitizeFieldName(t.Name + "_" + rel.fieldName)
			}
			typ := n.models[t.Name] + "[]"
			if columnsAreUnique(t, rel.fk.Columns) {
				typ = n.models[t.Name] + "?"
			}
			n.backRelations[target.Name] = append(n.backRelations[target.Name], backRelationField{
				fieldName: uniqueName(base, usedFields[target.Name]),
				typ:       typ,
			})
		}
	}
	return n
}

func (n *schemaNames) relationsBetween(from, to string) []relationField {
	var out []relationField
	for _, rel := range n.relations[from] {
		if rel.fk.RefTable == to {
			out = append(out, rel)
		}
	}
	return out
}

// relationFieldBase names one of several relations to the same table after
// its foreign-key columns: author_id -> author, editorId -> editor.
func relationFieldBase(fk ForeignKeyInfo) string {
	if len(fk.Columns) == 1 {
		col := fk.Columns[0]
		lower := strings.ToLower(col)
		switch {
		case len(col) > 3 && strings.HasSuffix(lower, "_id"):
			return sanitizeFieldName(col[:len(col)-3])
		case len(col) > 2 && (strings.HasSuffix(col, "Id") || strings.HasSuffix(col, "ID")):
			return sanitizeFieldName(col[:len(col)-2])
		}
	}
	return sanitizeFieldName(fk.RefTable + "_" + strings.Join(fk.Columns, "_"))
}

// columnsAreUnique reports whether columns exactly match the table's
// single-column primary key, a unique column, or a non-partial unique index.
func columnsAreUnique(t TableInfo, columns []string) bool {
	want := append([]string(nil), columns...)
	sort.Strings(want)
	key := strings.Join(want, "\x00")
	var pk []string
	for _, col := range t.Columns {
		if col.IsPrimaryKey {
			pk = append(pk, col.Name)
		}
		if col.IsUnique && len(columns) == 1 && col.Name == columns[0] {
			return true
		}
	}
	sort.Strings(pk)
	if len(pk) > 0 && strings.Join(pk, "\x00") == key {
		return true
	}
	for _, idx := range t.Indexes {
		if !idx.IsUnique || idx.Where != "" || hasIndexExpressions(idx.Expressions) {
			continue
		}
		cols := append([]string(nil), idx.Columns...)
		sort.Strings(cols)
		if strings.Join(cols, "\x00") == key {
			return true
		}
	}
	return false
}

// sanitizeEnumValueName returns label when it is already a valid schema
// identifier; otherwise a valid identifier (the label is kept via @map).
func sanitizeEnumValueName(label string) string {
	if isSchemaIdentifier(label) {
		return label
	}
	var b strings.Builder
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	value := strings.Trim(b.String(), "_")
	if value == "" {
		return "VALUE"
	}
	if first, _ := utf8.DecodeRuneInString(value); unicode.IsDigit(first) {
		value = "V_" + value
	}
	if value == "true" || value == "false" {
		value += "_value"
	}
	return value
}

// isSchemaIdentifier mirrors the schema lexer's identifier rule.
func isSchemaIdentifier(s string) bool {
	if s == "" || s == "true" || s == "false" {
		return false
	}
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}

func writeEnum(b *strings.Builder, enum EnumInfo, naming *enumNaming) {
	if naming == nil {
		return
	}
	fmt.Fprintf(b, "\nenum %s {\n", naming.name)
	for _, label := range enum.Values {
		name := naming.values[label]
		if name != label {
			fmt.Fprintf(b, "  %s @map(%s)\n", name, formatter.FormatExpression(ast.StringLiteral{Value: label}))
		} else {
			fmt.Fprintf(b, "  %s\n", name)
		}
	}
	if naming.name != enum.Name {
		fmt.Fprintf(b, "\n  @@map(%s)\n", formatter.FormatExpression(ast.StringLiteral{Value: enum.Name}))
	}
	b.WriteString("}\n")
}

// writeIRColumn renders a column carried as an ir.Field.
func writeIRColumn(b *strings.Builder, provider string, col ColumnInfo, fieldName string, singleID bool, names *schemaNames) {
	f := col.Field
	var enum *enumNaming
	fieldType := f.ScalarType
	if f.Type == ir.FieldKindEnum {
		enum = names.enums[col.EnumType]
		if enum != nil {
			fieldType = enum.name
		} else {
			fieldType = "String"
		}
	}
	if fieldType == "" {
		fieldType = "String"
	}
	switch {
	case f.IsList:
		// Schema types take either [] or ?, so nullable arrays become lists.
		fieldType += "[]"
	case col.IsNullable:
		fieldType += "?"
	}
	if f.IsList && col.IsNullable {
		fmt.Fprintf(b, "  // WARNING: %s is a nullable array column; schema lists are NOT NULL, so db push would add NOT NULL (failing if rows hold NULL)\n", col.Name)
	}

	line := fmt.Sprintf("  %-20s %s", fieldName, fieldType)
	if singleID {
		line += " @id"
	}
	if col.IsUnique && !col.IsPrimaryKey {
		line += " @unique"
	}
	expr, note := schemaDefaultExpression(provider, f, enum)
	if expr != "" {
		line += " @default(" + expr + ")"
	}
	if f.NativeType != nil && f.Type == ir.FieldKindScalar {
		line += " " + nativeTypeAttribute(f.NativeType)
	}
	if fieldName != col.Name {
		line += fmt.Sprintf(" @map(%q)", col.Name)
	}
	if f.IsGenerated {
		generated := generatedExpression(f)
		b.WriteString("  // NOTE: schemas cannot declare generated columns. db push leaves this column alone, but\n")
		b.WriteString("  // migrate init-sql/diff would create it as a plain DEFAULT; create it with custom SQL there.\n")
		fmt.Fprintf(b, "  // %s is a read-only generated column: GENERATED ALWAYS AS (%s) STORED\n", fieldName, generated)
	}
	if note != "" {
		fmt.Fprintf(b, "  // %s\n", note)
	}
	b.WriteString(line)
	b.WriteString("\n")
}

func nativeTypeAttribute(nt *ir.NativeType) string {
	attr := "@db." + nt.Name
	if len(nt.Args) > 0 {
		attr += "(" + strings.Join(nt.Args, ", ") + ")"
	}
	return attr
}

func generatedExpression(f *ir.Field) string {
	if f.Default != nil && f.Default.IsFunction && f.Default.FuncName == "dbgenerated" && len(f.Default.FuncArgs) > 0 {
		return f.Default.FuncArgs[0]
	}
	return "unknown expression"
}

// schemaDefaultExpression renders an introspected default as a schema
// @default argument. note explains a default that had to be dropped or
// rewritten.
func schemaDefaultExpression(provider string, f *ir.Field, enum *enumNaming) (expr, note string) {
	d := f.Default
	if d == nil {
		return "", ""
	}
	if d.IsFunction {
		return functionDefaultExpression(provider, f)
	}
	if d.IsArray {
		elems := make([]string, len(d.ArrayValue))
		for i, value := range d.ArrayValue {
			elems[i] = literalDefaultExpression(f, enum, value)
		}
		return "[" + strings.Join(elems, ", ") + "]", ""
	}
	return literalDefaultExpression(f, enum, d.Value), ""
}

func functionDefaultExpression(provider string, f *ir.Field) (string, string) {
	d := f.Default
	switch d.FuncName {
	case "autoincrement":
		switch f.ScalarType {
		case "Int", "SmallInt", "BigInt":
			return "autoincrement()", ""
		}
		return "", fmt.Sprintf("autoincrement default omitted: not valid for %s", f.ScalarType)
	case "uuid":
		if f.ScalarType == "String" || f.ScalarType == "UUID" {
			return "uuid()", ""
		}
	case "now":
		if f.ScalarType == "DateTime" {
			return "now()", ""
		}
	case "cuid":
		if f.ScalarType == "String" {
			return "cuid()", ""
		}
	case "dbgenerated":
		if len(d.FuncArgs) > 0 && strings.TrimSpace(d.FuncArgs[0]) != "" {
			return dbGeneratedCall(d.FuncArgs[0]), ""
		}
		return "", "empty dbgenerated() default omitted"
	}
	// The function is not valid for this field type in the schema language;
	// keep the database behaviour as a raw expression.
	if sqlExpr := sqlDefaultFunction(provider, d.FuncName, d.FuncArgs); sqlExpr != "" {
		return dbGeneratedCall(sqlExpr), ""
	}
	return "", fmt.Sprintf("default %s() omitted: not representable for %s", d.FuncName, f.ScalarType)
}

func sqlDefaultFunction(provider, funcName string, args []string) string {
	switch provider {
	case "postgresql":
		return runtimedialect.PostgreSQL{}.DefaultValueExpression(funcName, args)
	case "mysql":
		return runtimedialect.MySQL{}.DefaultValueExpression(funcName, args)
	case "sqlite":
		return runtimedialect.SQLite{}.DefaultValueExpression(funcName, args)
	}
	return ""
}

func dbGeneratedCall(expr string) string {
	return "dbgenerated(" + formatter.FormatExpression(ast.StringLiteral{Value: expr}) + ")"
}

// literalDefaultExpression renders one literal default value according to
// the field type, so string values are always quoted (even "42" or "now()")
// and enum labels become enum value identifiers.
func literalDefaultExpression(f *ir.Field, enum *enumNaming, value string) string {
	if f.Type == ir.FieldKindEnum {
		if enum != nil {
			if name, ok := enum.values[value]; ok {
				return name
			}
			for _, name := range enum.values {
				if name == value {
					return name
				}
			}
		}
		return dbGeneratedCall(quoteSQLLiteral(value))
	}
	switch f.ScalarType {
	case "Int", "SmallInt", "BigInt", "Float", "Decimal":
		if isNumericDefault(value) {
			return value
		}
		return dbGeneratedCall(quoteSQLLiteral(value))
	case "Boolean":
		switch strings.ToLower(value) {
		case "true", "1":
			return "true"
		case "false", "0":
			return "false"
		}
		return dbGeneratedCall(quoteSQLLiteral(value))
	default:
		return formatter.FormatExpression(ast.StringLiteral{Value: value})
	}
}

func quoteSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// formatIndexColumnOptions renders per-column @@index options that differ
// from the database defaults.
func formatIndexColumnOptions(idx IndexInfo, fieldCount int) []string {
	var args []string
	add := func(key string, values []string, keyword bool) {
		if formatted := formatIndexOption(values, fieldCount, keyword); formatted != "" {
			args = append(args, key+": "+formatted)
		}
	}
	add("sort", idx.Sorts, true)
	add("nulls", idx.Nulls, true)
	add("opclass", idx.OpClasses, false)
	add("collate", idx.Collations, false)
	return args
}

func formatIndexOption(values []string, fieldCount int, keyword bool) string {
	if len(values) != fieldCount || fieldCount == 0 {
		return ""
	}
	allSet, anySet := true, false
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			allSet = false
		} else {
			anySet = true
		}
	}
	if !anySet {
		return ""
	}
	parts := make([]string, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if keyword && allSet {
			parts[i] = pascalKeyword(value)
		} else {
			parts[i] = formatter.FormatExpression(ast.StringLiteral{Value: value})
		}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pascalKeyword(value string) string {
	lower := strings.ToLower(value)
	if lower == "" {
		return value
	}
	return strings.ToUpper(lower[:1]) + lower[1:]
}

func indexHasColumnOptions(idx IndexInfo) bool {
	for _, values := range [][]string{idx.Sorts, idx.Nulls, idx.OpClasses, idx.Collations} {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

// enumsFromIR lists the schema's enums by database names.
func enumsFromIR(schema *ir.Schema) []EnumInfo {
	if schema == nil {
		return nil
	}
	out := make([]EnumInfo, 0, len(schema.Enums))
	for _, enum := range schema.Enums {
		info := EnumInfo{Name: enumDBName(enum)}
		for _, value := range enum.Values {
			label := value.DBName
			if label == "" {
				label = value.Name
			}
			info.Values = append(info.Values, label)
		}
		out = append(out, info)
	}
	return out
}

func enumDBName(enum *ir.Enum) string {
	if enum.DBName != "" {
		return enum.DBName
	}
	return enum.Name
}

// enumDatabaseName resolves an IR enum name to its database type name.
func enumDatabaseName(schema *ir.Schema, name string) string {
	for _, enum := range schema.Enums {
		if enum.Name == name {
			return enumDBName(enum)
		}
	}
	return name
}
