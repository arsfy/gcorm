// planner.go implements the schema diff engine that compares two IR schemas
// and produces a structured changeset describing the migration steps.
package migrate

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// ChangeType categorizes a schema change.
type ChangeType string

const (
	CreateTable ChangeType = "CreateTable"
	DropTable   ChangeType = "DropTable"
	RenameTable ChangeType = "RenameTable"
	// SetTableSchema moves an existing table to another PostgreSQL schema.
	// OldValue and NewValue hold the source and target schema names.
	SetTableSchema ChangeType = "SetTableSchema"
	AddColumn      ChangeType = "AddColumn"
	DropColumn     ChangeType = "DropColumn"
	RenameColumn   ChangeType = "RenameColumn"
	AlterType      ChangeType = "AlterType"
	AlterNull      ChangeType = "AlterNullability"
	AlterDefault   ChangeType = "AlterDefault"
	AddIndex       ChangeType = "AddIndex"
	DropIndex      ChangeType = "DropIndex"
	AddUnique      ChangeType = "AddUnique"
	DropUnique     ChangeType = "DropUnique"
	ChangePK       ChangeType = "ChangePrimaryKey"
	AddFK          ChangeType = "AddForeignKey"
	DropFK         ChangeType = "DropForeignKey"
	CreateEnum     ChangeType = "CreateEnum"
	AlterEnum      ChangeType = "AlterEnum"
	DropEnum       ChangeType = "DropEnum"
	// RenameEnum renames an enum's database type after an @@map change.
	// OldValue and NewValue hold the previous and new type names.
	RenameEnum  ChangeType = "RenameEnum"
	AddTrigger  ChangeType = "AddTrigger"
	DropTrigger ChangeType = "DropTrigger"
)

// RollbackSafety indicates how safe it is to reverse a change.
type RollbackSafety string

const (
	SafeRollback        RollbackSafety = "safe"
	DestructiveRollback RollbackSafety = "destructive"
	ReviewRequired      RollbackSafety = "review"
)

// Change represents a single schema change.
type Change struct {
	Type     ChangeType
	Model    string            // Table/model name.
	Schema   string            // PostgreSQL schema of the table (@@schema); empty uses search_path.
	Field    string            // Column name (if applicable).
	OldValue string            // Previous value (for renames, type changes).
	NewValue string            // New value.
	Details  map[string]string // Additional context.
	Rollback RollbackSafety
	OldField *ir.Field   // Complete source column definition for dialects such as MySQL.
	NewField *ir.Field   // Complete target column definition for dialects such as MySQL.
	Trigger  *ir.Trigger // Definition for either an add or a drop.
}

// Changeset is an ordered list of changes from old to new schema.
type Changeset struct {
	Changes []Change
	Old     *ir.Schema // Source schema (may be nil for initial migration).
	New     *ir.Schema // Target schema (may be nil for teardown).
}

// classifyRollback returns the safety classification for a change type.
func classifyRollback(ct ChangeType) RollbackSafety {
	switch ct {
	case CreateTable, AddColumn, AddIndex, AddUnique, AddFK, CreateEnum, AddTrigger:
		return SafeRollback
	case DropTable, DropColumn, DropIndex, DropUnique, DropFK:
		return DestructiveRollback
	default:
		return ReviewRequired
	}
}

// Diff compares two schemas and returns the changes needed to go from old to new.
func Diff(old, new *ir.Schema) *Changeset {
	cs := &Changeset{Old: old, New: new}

	if old == nil {
		old = &ir.Schema{}
	}
	if new == nil {
		new = &ir.Schema{}
	}

	oldModels := indexModels(old.Models)
	newModels := indexModels(new.Models)
	moves := matchSchemaMoves(oldModels, newModels)
	movedFrom := make(map[string]bool, len(moves))
	for _, oldKey := range moves {
		movedFrom[oldKey] = true
	}
	allNames := mergedSortedKeys(oldModels, newModels)
	var createdTables []string

	for _, name := range allNames {
		oldM, inOld := oldModels[name]
		newM, inNew := newModels[name]

		switch {
		case inNew && !inOld:
			if oldKey, moved := moves[name]; moved {
				oldM = oldModels[oldKey]
				cs.add(Change{Type: SetTableSchema, Model: newM.TableName(), Schema: newM.Schema, OldValue: oldM.Schema, NewValue: newM.Schema})
				compareModels(cs, old, new, oldM, newM)
				break
			}
			cs.add(Change{Type: CreateTable, Model: newM.TableName(), Schema: newM.Schema})
			createdTables = append(createdTables, name)
		case inOld && !inNew:
			if movedFrom[name] {
				continue
			}
			cs.add(Change{Type: DropTable, Model: oldM.TableName(), Schema: oldM.Schema})
		default:
			compareModels(cs, old, new, oldM, newM)
		}
		compareTriggers(cs, oldM, newM)
	}

	addForeignKeysForCreatedTables(cs, newModels, createdTables)
	compareEnums(cs, old, new)

	return cs
}

// matchSchemaMoves pairs a target-only model with a source-only model that
// has the same table name in a different schema. Each table name must be
// unambiguous on both sides. The result maps target keys to source keys.
func matchSchemaMoves(oldModels, newModels map[string]*ir.Model) map[string]string {
	onlyByTable := func(models, other map[string]*ir.Model) map[string][]string {
		out := make(map[string][]string)
		for key, model := range models {
			if _, ok := other[key]; !ok {
				out[model.TableName()] = append(out[model.TableName()], key)
			}
		}
		return out
	}
	oldOnly := onlyByTable(oldModels, newModels)
	newOnly := onlyByTable(newModels, oldModels)
	moves := make(map[string]string)
	for table, newKeys := range newOnly {
		oldKeys := oldOnly[table]
		if len(newKeys) == 1 && len(oldKeys) == 1 {
			moves[newKeys[0]] = oldKeys[0]
		}
	}
	return moves
}

// compareTriggers diffs managed triggers. Drops use the source table location
// and additions the target location, because triggers are dropped before and
// created after any SetTableSchema change.
func compareTriggers(cs *Changeset, oldModel, newModel *ir.Model) {
	oldTriggers := indexTriggers(oldModel)
	newTriggers := indexTriggers(newModel)
	for _, name := range mergedSortedKeys(oldTriggers, newTriggers) {
		old, new := oldTriggers[name], newTriggers[name]
		if old != nil && new != nil && triggersEqual(old, new) {
			continue
		}
		if old != nil {
			cs.add(Change{Type: DropTrigger, Model: oldModel.TableName(), Schema: oldModel.Schema, Field: name, Trigger: old})
		}
		if new != nil {
			cs.add(Change{Type: AddTrigger, Model: newModel.TableName(), Schema: newModel.Schema, Field: name, Trigger: new})
		}
	}
}

func indexTriggers(model *ir.Model) map[string]*ir.Trigger {
	triggers := make(map[string]*ir.Trigger)
	if model != nil {
		for _, trigger := range model.Triggers {
			triggers[trigger.Name] = normalizedTrigger(model, trigger)
		}
	}
	return triggers
}

func compareEnums(cs *Changeset, old, new *ir.Schema) {
	oldMap := map[string]*ir.Enum{}
	newMap := map[string]*ir.Enum{}
	for _, e := range old.Enums {
		oldMap[e.Name] = e
	}
	for _, e := range new.Enums {
		newMap[e.Name] = e
	}
	for _, name := range mergedSortedKeys(oldMap, newMap) {
		oe, okOld := oldMap[name]
		ne, okNew := newMap[name]
		switch {
		case okNew && !okOld:
			cs.add(Change{Type: CreateEnum, Model: name})
		case okOld && !okNew:
			cs.add(Change{Type: DropEnum, Model: name})
		default:
			if oldName, newName := enumTypeName(oe), enumTypeName(ne); oldName != newName {
				cs.add(Change{Type: RenameEnum, Model: name, OldValue: oldName, NewValue: newName})
			}
			if enumSignature(oe) != enumSignature(ne) {
				rollback := ReviewRequired
				if len(removedEnumLabels(oe, ne)) > 0 {
					rollback = DestructiveRollback
				}
				cs.add(Change{Type: AlterEnum, Model: name, OldValue: enumSignature(oe), NewValue: enumSignature(ne), Rollback: rollback})
			}
		}
	}
}

// enumTypeName returns the database type name of an enum (@@map or name).
func enumTypeName(e *ir.Enum) string {
	if e.DBName != "" {
		return e.DBName
	}
	return e.Name
}

// enumValueLabel returns the database label of an enum value (@map or name).
func enumValueLabel(v *ir.EnumValue) string {
	if v.DBName != "" {
		return v.DBName
	}
	return v.Name
}

// enumLabelRenames returns old->new database labels for values whose @map
// changed, in target declaration order.
func enumLabelRenames(old, new *ir.Enum) [][2]string {
	oldByName := make(map[string]*ir.EnumValue, len(old.Values))
	for _, v := range old.Values {
		oldByName[v.Name] = v
	}
	var renames [][2]string
	for _, v := range new.Values {
		if ov := oldByName[v.Name]; ov != nil && enumValueLabel(ov) != enumValueLabel(v) {
			renames = append(renames, [2]string{enumValueLabel(ov), enumValueLabel(v)})
		}
	}
	return renames
}

// enumLabelsAfterRenames returns the source labels with value renames applied.
func enumLabelsAfterRenames(old, new *ir.Enum) []string {
	renamed := make(map[string]string)
	for _, rename := range enumLabelRenames(old, new) {
		renamed[rename[0]] = rename[1]
	}
	labels := make([]string, len(old.Values))
	for i, v := range old.Values {
		labels[i] = coalesce(renamed[enumValueLabel(v)], enumValueLabel(v))
	}
	return labels
}

// removedEnumLabels returns the labels a source enum loses in the target.
func removedEnumLabels(old, new *ir.Enum) []string {
	target := make(map[string]bool, len(new.Values))
	for _, v := range new.Values {
		target[enumValueLabel(v)] = true
	}
	var removed []string
	for _, label := range enumLabelsAfterRenames(old, new) {
		if !target[label] {
			removed = append(removed, label)
		}
	}
	return removed
}

// enumSignature lists an enum's database labels in declaration order.
// Renaming a value while keeping its @map label changes nothing in the
// database.
func enumSignature(e *ir.Enum) string {
	if e == nil {
		return ""
	}
	values := make([]string, len(e.Values))
	for i, v := range e.Values {
		values[i] = strconv.Quote(enumValueLabel(v))
	}
	return strings.Join(values, ",")
}

func (cs *Changeset) add(c Change) {
	if c.Rollback == "" {
		c.Rollback = classifyRollback(c.Type)
	}
	cs.Changes = append(cs.Changes, c)
}

// ---------------------------------------------------------------------------
// Model comparison
// ---------------------------------------------------------------------------

func compareModels(cs *Changeset, oldSchema, newSchema *ir.Schema, oldM, newM *ir.Model) {
	start := len(cs.Changes)
	tableName := newM.TableName()
	compareFields(cs, tableName, oldM.ScalarFields(), newM.ScalarFields())
	compareIndexes(cs, tableName, oldSchema, newSchema, oldM, newM)
	compareUniques(cs, tableName, oldM, newM)
	comparePrimaryKey(cs, tableName, oldM, newM)
	compareForeignKeys(cs, tableName, oldSchema, newSchema, oldM, newM)
	for i := start; i < len(cs.Changes); i++ {
		cs.Changes[i].Schema = newM.Schema
	}
}

// ---------------------------------------------------------------------------
// Fields
// ---------------------------------------------------------------------------

func compareFields(cs *Changeset, model string, oldFields, newFields []*ir.Field) {
	oldMap := indexFields(oldFields)
	newMap := indexFields(newFields)
	allNames := mergedSortedKeys(oldMap, newMap)

	for _, fn := range allNames {
		of, inOld := oldMap[fn]
		nf, inNew := newMap[fn]

		switch {
		case inNew && !inOld:
			cs.add(Change{Type: AddColumn, Model: model, Field: fn, NewValue: fieldTypeSignature(nf), NewField: nf})
		case inOld && !inNew:
			cs.add(Change{Type: DropColumn, Model: model, Field: fn, OldValue: fieldTypeSignature(of), OldField: of})
		default:
			compareField(cs, model, of, nf)
		}
	}
}

// compareField diffs a column. A generated column is read-only: its type
// and expression are never altered, only its nullability.
func compareField(cs *Changeset, model string, of, nf *ir.Field) {
	generated := of.IsGenerated || nf.IsGenerated
	oldType := fieldTypeSignature(of)
	newType := fieldTypeSignature(nf)
	if oldType != newType && !generated {
		cs.add(Change{
			Type: AlterType, Model: model, Field: columnName(nf),
			OldValue: oldType, NewValue: newType, OldField: of, NewField: nf,
		})
	}

	if of.IsOptional != nf.IsOptional {
		cs.add(Change{
			Type: AlterNull, Model: model, Field: columnName(nf),
			OldValue: nullLabel(of.IsOptional), NewValue: nullLabel(nf.IsOptional), OldField: of, NewField: nf,
		})
	}

	if generated {
		return
	}
	od := databaseDefaultSignature(of.Default)
	nd := databaseDefaultSignature(nf.Default)
	if od != nd && !equivalentJSONDefaults(of, nf) {
		cs.add(Change{
			Type: AlterDefault, Model: model, Field: columnName(nf),
			OldValue: od, NewValue: nd, OldField: of, NewField: nf,
		})
	}
}

func nullLabel(optional bool) string {
	if optional {
		return "optional"
	}
	return "required"
}

func formatDefault(d *ir.DefaultValue) string {
	if d == nil {
		return ""
	}
	if d.IsArray {
		return "[" + strings.Join(d.ArrayValue, ",") + "]"
	}
	if d.IsFunction {
		if len(d.FuncArgs) > 0 {
			return d.FuncName + "(" + strings.Join(d.FuncArgs, ",") + ")"
		}
		return d.FuncName + "()"
	}
	return d.Value
}

// databaseDefaultSignature is the default as the database stores it. cuid()
// values are generated by the client, so the column has no default.
func databaseDefaultSignature(d *ir.DefaultValue) string {
	if isCUIDDefault(d) {
		return ""
	}
	return formatDefault(d)
}

func isCUIDDefault(d *ir.DefaultValue) bool {
	return d != nil && d.IsFunction && d.FuncName == "cuid"
}

func fieldTypeSignature(f *ir.Field) string {
	if f == nil {
		return ""
	}
	base := f.ScalarType
	if f.Type == ir.FieldKindEnum {
		base = f.EnumType
	}
	if f.NativeType != nil {
		base += "@db." + f.NativeType.Name
		if len(f.NativeType.Args) > 0 {
			base += "(" + strings.Join(f.NativeType.Args, ",") + ")"
		}
	}
	if f.IsList {
		return base + "[]"
	}
	return base
}

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

func compareIndexes(cs *Changeset, model string, oldSchema, newSchema *ir.Schema, oldM, newM *ir.Model) {
	provider := schemaProvider(newSchema, oldSchema)
	oldMap := mapIndexes(provider, oldM, oldM.Indexes)
	newMap := mapIndexes(provider, newM, newM.Indexes)
	allKeys := mergedSortedKeys(oldMap, newMap)

	for _, k := range allKeys {
		oi, inOld := oldMap[k]
		ni, inNew := newMap[k]

		switch {
		case inNew && !inOld:
			cs.add(Change{
				Type: AddIndex, Model: model, NewValue: ni.Name,
				Details: indexDetails(ni),
			})
		case inOld && !inNew:
			cs.add(Change{
				Type: DropIndex, Model: model, OldValue: oi.Name,
				Details: indexDetails(oi),
			})
		default:
			if indexSignature(oldSchema, oi) != indexSignature(newSchema, ni) {
				cs.add(Change{
					Type: DropIndex, Model: model, OldValue: oi.Name,
					Details: indexDetails(oi),
				})
				cs.add(Change{
					Type: AddIndex, Model: model, NewValue: ni.Name,
					Details: indexDetails(ni),
				})
			}
		}
	}
}

func indexDetails(idx *ir.Index) map[string]string {
	details := map[string]string{
		"fields": strings.Join(idx.Fields, ","),
		"unique": boolStr(idx.IsUnique),
	}
	if idx.Where != "" {
		details["where"] = idx.Where
	}
	cols := effectiveIndexColumns(idx)
	details["expressions"] = encodeIndexExpressions(indexColumnValues(cols, func(c ir.IndexColumn) string { return c.Expression }))
	details["sorts"] = strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return c.Sort }), ",")
	details["nulls"] = strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return c.Nulls }), ",")
	details["opclasses"] = strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return c.OpClass }), ",")
	details["collations"] = strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return c.Collation }), ",")
	return details
}

func encodeIndexExpressions(expressions []string) string {
	encoded, err := json.Marshal(expressions)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func indexSignature(schema *ir.Schema, idx *ir.Index) string {
	if idx == nil {
		return ""
	}
	enumTypes := postgresEnumTypeNames(schema)
	cols := effectiveIndexColumns(idx)
	predicate := strings.TrimSpace(idx.Where)
	if schema == nil || schema.Datasource == nil || schema.Datasource.Provider != "sqlite" {
		predicate = normalizeIndexPredicate(predicate)
	}
	return strings.Join([]string{
		strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string {
			if strings.TrimSpace(c.Expression) != "" {
				return ""
			}
			return c.Field
		}), ","),
		strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string {
			return normalizeIndexExpressionWithEnums(c.Expression, enumTypes)
		}), ","),
		boolStr(idx.IsUnique),
		predicate,
		strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return normalizeIndexSort(c.Sort) }), ","),
		strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return normalizeIndexNulls(c.Sort, c.Nulls) }), ","),
		strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return normalizeIndexOpClass(c.OpClass) }), ","),
		strings.Join(indexColumnValues(cols, func(c ir.IndexColumn) string { return normalizeIndexCollation(c.Collation) }), ","),
	}, "\x00")
}

func normalizeIndexExpression(expression string) string {
	return normalizeIndexExpressionWithEnums(expression, nil)
}

func normalizeIndexExpressionWithEnums(expression string, enumTypes map[string]struct{}) string {
	expression = strings.TrimSpace(expression)
	for hasWrappingParens(expression) {
		expression = strings.TrimSpace(expression[1 : len(expression)-1])
	}
	expression = uppercaseSQLKeywords(expression)
	expression = stripPostgresImplicitCaseElseNull(expression)
	expression = stripPostgresTextCasts(expression)
	expression = stripPostgresEnumLiteralCasts(expression, enumTypes)
	expression = stripPostgresLiteralCasts(expression, false)
	expression = rewritePostgresArrayMembership(expression)
	for {
		next := stripPostgresCaseWhenParens(expression)
		if next == expression {
			break
		}
		expression = next
	}
	for {
		next := stripRedundantIdentifierParens(expression)
		if next == expression {
			break
		}
		expression = next
	}
	expression = normalizeSQLCommas(expression)
	return strings.Join(strings.Fields(expression), " ")
}

func postgresEnumTypeNames(schema *ir.Schema) map[string]struct{} {
	if schema == nil || len(schema.Enums) == 0 {
		return nil
	}
	types := make(map[string]struct{}, len(schema.Enums))
	for _, enum := range schema.Enums {
		if enum == nil {
			continue
		}
		name := enum.DBName
		if name == "" {
			name = enum.Name
		}
		if name != "" {
			types[name] = struct{}{}
		}
	}
	return types
}

func stripPostgresEnumLiteralCasts(expression string, enumTypes map[string]struct{}) string {
	if len(enumTypes) == 0 {
		return expression
	}
	var b strings.Builder
	for i := 0; i < len(expression); {
		if expression[i] != '\'' {
			b.WriteByte(expression[i])
			i++
			continue
		}
		end := postgresStringLiteralEnd(expression, i)
		b.WriteString(expression[i:end])
		i = end
		if !strings.HasPrefix(expression[i:], "::") {
			continue
		}
		castStart := i + 2
		castEnd := postgresCastTypeEnd(expression, castStart)
		if castEnd == castStart || !isPostgresEnumCast(expression[castStart:castEnd], enumTypes) {
			continue
		}
		i = castEnd
	}
	return b.String()
}

func isPostgresEnumCast(castType string, enumTypes map[string]struct{}) bool {
	name := strings.TrimSpace(castType)
	name = strings.ReplaceAll(name, `"`, "")
	if _, ok := enumTypes[name]; ok {
		return true
	}
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		_, ok := enumTypes[name[dot+1:]]
		return ok
	}
	return false
}

func stripPostgresImplicitCaseElseNull(expression string) string {
	var b strings.Builder
	for i := 0; i < len(expression); {
		switch expression[i] {
		case '\'':
			end := postgresStringLiteralEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end
			continue
		}
		if sqlKeywordAt(expression, i, "ELSE") {
			if endStart, ok := implicitCaseElseNullEnd(expression, i); ok {
				i = endStart
				continue
			}
		}
		b.WriteByte(expression[i])
		i++
	}
	return b.String()
}

func implicitCaseElseNullEnd(expression string, elseStart int) (int, bool) {
	i := skipSQLSpace(expression, elseStart+len("ELSE"))
	if !sqlKeywordAt(expression, i, "NULL") {
		return 0, false
	}
	i = skipSQLSpace(expression, i+len("NULL"))
	if strings.HasPrefix(expression[i:], "::") {
		castEnd := postgresCastTypeEnd(expression, i+2)
		if castEnd == i+2 {
			return 0, false
		}
		i = skipSQLSpace(expression, castEnd)
	}
	if !sqlKeywordAt(expression, i, "END") {
		return 0, false
	}
	return i, true
}

func stripPostgresCaseWhenParens(expression string) string {
	var b strings.Builder
	for i := 0; i < len(expression); {
		switch expression[i] {
		case '\'':
			end := postgresStringLiteralEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end
			continue
		case '"':
			end := postgresQuotedIdentifierEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end
			continue
		}
		if !sqlKeywordAt(expression, i, "WHEN") {
			b.WriteByte(expression[i])
			i++
			continue
		}
		keywordEnd := i + len("WHEN")
		conditionStart := skipSQLSpace(expression, keywordEnd)
		if conditionStart >= len(expression) || expression[conditionStart] != '(' {
			b.WriteString(expression[i:keywordEnd])
			i = keywordEnd
			continue
		}
		conditionEnd := matchingParenIndex(expression, conditionStart)
		if conditionEnd < 0 || !sqlKeywordAt(expression, skipSQLSpace(expression, conditionEnd+1), "THEN") {
			b.WriteString(expression[i:keywordEnd])
			i = keywordEnd
			continue
		}
		b.WriteString(expression[i:conditionStart])
		b.WriteString(strings.TrimSpace(expression[conditionStart+1 : conditionEnd]))
		i = conditionEnd + 1
	}
	return b.String()
}

func skipSQLSpace(s string, start int) int {
	for start < len(s) {
		switch s[start] {
		case ' ', '\t', '\n', '\r':
			start++
		default:
			return start
		}
	}
	return start
}

func sqlKeywordAt(s string, start int, keyword string) bool {
	end := start + len(keyword)
	if start < 0 || end > len(s) || !strings.EqualFold(s[start:end], keyword) {
		return false
	}
	if start > 0 && isSQLIdentifierByte(s[start-1]) {
		return false
	}
	return end == len(s) || !isSQLIdentifierByte(s[end])
}

func isSQLIdentifierByte(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func stripPostgresTextCasts(expression string) string {
	var b strings.Builder
	for i := 0; i < len(expression); {
		if expression[i] == '\'' {
			end := postgresStringLiteralEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end
			continue
		}
		if !strings.HasPrefix(expression[i:], "::") {
			b.WriteByte(expression[i])
			i++
			continue
		}
		castStart := i + 2
		castEnd := postgresCastTypeEnd(expression, castStart)
		if castEnd > castStart && isPostgresTextLikeCast(expression[castStart:castEnd]) {
			i = castEnd
			continue
		}
		b.WriteString("::")
		i += 2
	}
	return b.String()
}

func postgresStringLiteralEnd(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		if s[i] != '\'' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			i++
			continue
		}
		return i + 1
	}
	return len(s)
}

func stripRedundantIdentifierParens(expression string) string {
	var b strings.Builder
	changed := false
	for i := 0; i < len(expression); i++ {
		if expression[i] == '\'' {
			end := postgresStringLiteralEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end - 1
			continue
		}
		if expression[i] == '"' {
			end := postgresQuotedIdentifierEnd(expression, i)
			b.WriteString(expression[i:end])
			i = end - 1
			continue
		}
		if expression[i] != '(' {
			b.WriteByte(expression[i])
			continue
		}
		end := matchingParenIndex(expression, i)
		if end == -1 {
			b.WriteByte(expression[i])
			continue
		}
		inner := strings.TrimSpace(expression[i+1 : end])
		if !isSimplePostgresIdentifier(inner) {
			b.WriteByte(expression[i])
			continue
		}
		b.WriteString(inner)
		i = end
		changed = true
	}
	if !changed {
		return expression
	}
	return b.String()
}

func postgresQuotedIdentifierEnd(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		if s[i] != '"' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '"' {
			i++
			continue
		}
		return i + 1
	}
	return len(s)
}

func isSimplePostgresIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); {
		end := postgresIdentifierEnd(value, i)
		if end == i {
			return false
		}
		i = end
		if i == len(value) {
			return true
		}
		if value[i] != '.' {
			return false
		}
		i++
	}
	return true
}

func normalizeIndexPredicate(where string) string {
	where = strings.TrimSpace(where)
	for hasWrappingParens(where) {
		where = strings.TrimSpace(where[1 : len(where)-1])
	}
	where = uppercaseSQLKeywords(where)
	where = stripPostgresTextLiteralCasts(where)
	where = stripPostgresTextCasts(where)
	where = stripPostgresLiteralCasts(where, true)
	where = rewritePostgresArrayMembership(where)
	for {
		next := stripSimplePredicateParens(where)
		if next == where {
			break
		}
		where = next
	}
	where = normalizeSQLCommas(where)
	return strings.Join(strings.Fields(where), " ")
}

func stripPostgresTextLiteralCasts(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			i++
			continue
		}

		for {
			b.WriteByte(s[i])
			i++
			if i >= len(s) {
				return b.String()
			}
			if s[i-1] != '\'' {
				continue
			}
			if i < len(s) && s[i] == '\'' {
				b.WriteByte(s[i])
				i++
				continue
			}
			break
		}

		if !strings.HasPrefix(s[i:], "::") {
			continue
		}
		castStart := i + 2
		castEnd := postgresCastTypeEnd(s, castStart)
		if castEnd == castStart {
			continue
		}
		if isPostgresTextLikeCast(s[castStart:castEnd]) {
			i = castEnd
		}
	}
	return b.String()
}

func postgresCastTypeEnd(s string, start int) int {
	if hasCaseInsensitivePrefix(s[start:], "character varying") && isCastBoundary(s, start+len("character varying")) {
		i := start + len("character varying")
		if i < len(s) && s[i] == '(' {
			if end := matchingParenIndex(s, i); end != -1 {
				i = end + 1
			}
		}
		if strings.HasPrefix(s[i:], "[]") {
			i += 2
		}
		return i
	}

	i := start
	for {
		next := postgresIdentifierEnd(s, i)
		if next == i {
			break
		}
		i = next
		if i >= len(s) || s[i] != '.' {
			break
		}
		i++
	}
	if i < len(s) && s[i] == '(' {
		if end := matchingParenIndex(s, i); end != -1 {
			i = end + 1
		}
	}
	if strings.HasPrefix(s[i:], "[]") {
		i += 2
	}
	return i
}

func postgresIdentifierEnd(s string, start int) int {
	if start >= len(s) {
		return start
	}
	if s[start] == '"' {
		for i := start + 1; i < len(s); i++ {
			if s[i] != '"' {
				continue
			}
			if i+1 < len(s) && s[i+1] == '"' {
				i++
				continue
			}
			return i + 1
		}
		return start
	}
	i := start
	for i < len(s) {
		r := rune(s[i])
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			break
		}
		i++
	}
	return i
}

func isPostgresTextLikeCast(castType string) bool {
	castType = strings.TrimSpace(castType)
	castType = strings.ReplaceAll(castType, `"`, "")
	castType = strings.Join(strings.Fields(castType), " ")
	// Ignore length/precision arguments: character varying(10) is still text-like.
	if open := strings.IndexByte(castType, '('); open >= 0 {
		if end := matchingParenIndex(castType, open); end == len(castType)-1 {
			castType = strings.TrimSpace(castType[:open])
		}
	}
	castType = strings.ToLower(castType)
	castType = strings.TrimPrefix(castType, "pg_catalog.")
	switch castType {
	case "text", "varchar", "character varying", "bpchar", "char", "character", "name":
		return true
	default:
		return false
	}
}

func hasCaseInsensitivePrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

func isCastBoundary(s string, idx int) bool {
	if idx >= len(s) {
		return true
	}
	r := rune(s[idx])
	return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
}

func stripSimplePredicateParens(s string) string {
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); i++ {
		if s[i] != '(' || (i > 0 && !isPredicateBoundaryByte(s[i-1])) {
			b.WriteByte(s[i])
			continue
		}
		end := matchingParenIndex(s, i)
		if end == -1 || (end+1 < len(s) && !isPredicateBoundaryByte(s[end+1])) {
			b.WriteByte(s[i])
			continue
		}
		inner := strings.TrimSpace(s[i+1 : end])
		upper := strings.ToUpper(inner)
		if strings.Contains(upper, " AND ") || strings.Contains(upper, " OR ") || hasTopLevelComma(inner) {
			b.WriteByte(s[i])
			continue
		}
		b.WriteString(inner)
		i = end
		changed = true
	}
	if !changed {
		return s
	}
	return b.String()
}

func matchingParenIndex(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = postgresStringLiteralEnd(s, i) - 1
		case '"':
			for i++; i < len(s); i++ {
				if s[i] != '"' {
					continue
				}
				if i+1 < len(s) && s[i+1] == '"' {
					i++
					continue
				}
				break
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isPredicateBoundaryByte(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '(', ')':
		return true
	default:
		return false
	}
}

func hasWrappingParens(s string) bool {
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return false
	}
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(s)-1 {
				return false
			}
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0
}

func normalizeIndexSort(sort string) string {
	sort = strings.ToUpper(strings.TrimSpace(sort))
	if sort == "" {
		return "ASC"
	}
	return sort
}

func normalizeIndexNulls(sort, nulls string) string {
	nulls = strings.ToUpper(strings.TrimSpace(nulls))
	if nulls != "" {
		return nulls
	}
	if normalizeIndexSort(sort) == "DESC" {
		return "FIRST"
	}
	return "LAST"
}

func normalizeIndexOpClass(opclass string) string {
	opclass = strings.TrimSpace(opclass)
	switch strings.ToLower(opclass) {
	case "bool_ops", "enum_ops", "int2_ops", "int4_ops", "int8_ops", "text_ops", "timestamp_ops", "timestamptz_ops", "uuid_ops":
		return ""
	default:
		return opclass
	}
}

func normalizeIndexCollation(collation string) string {
	collation = strings.Trim(strings.TrimSpace(collation), `"`)
	collation = strings.ReplaceAll(collation, `"."`, ".")
	collation = strings.ReplaceAll(collation, `"`, "")
	// Built-in collations live in pg_catalog, which introspection reports.
	if len(collation) > len("pg_catalog.") && strings.EqualFold(collation[:len("pg_catalog.")], "pg_catalog.") {
		collation = collation[len("pg_catalog."):]
	}
	switch strings.ToLower(collation) {
	case "default":
		return ""
	default:
		return collation
	}
}

func indexColumnValues(cols []ir.IndexColumn, pick func(ir.IndexColumn) string) []string {
	values := make([]string, len(cols))
	for i, col := range cols {
		values[i] = pick(col)
	}
	return values
}

func effectiveIndexColumns(idx *ir.Index) []ir.IndexColumn {
	if idx == nil {
		return nil
	}
	if len(idx.Columns) > 0 {
		return idx.Columns
	}
	cols := make([]ir.IndexColumn, len(idx.Fields))
	for i, field := range idx.Fields {
		cols[i] = ir.IndexColumn{Field: field}
	}
	return cols
}

func mapIndexes(provider string, model *ir.Model, idxs []*ir.Index) map[string]*ir.Index {
	m := make(map[string]*ir.Index, len(idxs))
	for _, idx := range idxs {
		m[idxKey(provider, model, idx)] = normalizedIndex(provider, model, idx)
	}
	return m
}

func idxKey(provider string, model *ir.Model, idx *ir.Index) string {
	return effectiveIndexName(provider, model, idx)
}

// schemaProvider returns the datasource provider of the first schema that
// declares one.
func schemaProvider(schemas ...*ir.Schema) string {
	for _, schema := range schemas {
		if schema != nil && schema.Datasource != nil && schema.Datasource.Provider != "" {
			return schema.Datasource.Provider
		}
	}
	return ""
}

// effectiveIndexName returns the explicit index name or the generated
// default name for provider (see DefaultIndexNameFor).
func effectiveIndexName(provider string, model *ir.Model, idx *ir.Index) string {
	if idx == nil {
		return ""
	}
	if idx.Name != "" {
		return idx.Name
	}
	tableName := ""
	if model != nil {
		tableName = model.TableName()
	}
	return DefaultIndexNameFor(provider, tableName, normalizeFieldNames(model, idx.Fields), idx.IsUnique)
}

// ---------------------------------------------------------------------------
// Unique constraints
// ---------------------------------------------------------------------------

func compareUniques(cs *Changeset, model string, oldM, newM *ir.Model) {
	oldMap := mapUniqueConstraints(oldM)
	newMap := mapUniqueConstraints(newM)
	allKeys := mergedSortedKeys(oldMap, newMap)

	for _, k := range allKeys {
		ou, inOld := oldMap[k]
		nu, inNew := newMap[k]

		switch {
		case inNew && !inOld:
			cs.add(Change{
				Type: AddUnique, Model: model, NewValue: nu.Name,
				Details: map[string]string{"fields": strings.Join(nu.Fields, ",")},
			})
		case inOld && !inNew:
			cs.add(Change{
				Type: DropUnique, Model: model, OldValue: ou.Name,
				Details: map[string]string{"fields": strings.Join(ou.Fields, ",")},
			})
		case nu.Name != "" && ou.Name != nu.Name:
			cs.add(Change{
				Type: DropUnique, Model: model, OldValue: ou.Name,
				Details: map[string]string{"fields": strings.Join(ou.Fields, ",")},
			})
			cs.add(Change{
				Type: AddUnique, Model: model, NewValue: nu.Name,
				Details: map[string]string{"fields": strings.Join(nu.Fields, ",")},
			})
		}
	}
}

func mapUniqueConstraints(model *ir.Model) map[string]*ir.UniqueConstraint {
	constraints := make([]*ir.UniqueConstraint, 0, len(model.UniqueConstraints)+len(model.Fields))
	for _, uc := range model.UniqueConstraints {
		constraints = append(constraints, uc)
	}
	for _, field := range model.ScalarFields() {
		if !field.IsUnique {
			continue
		}
		constraints = append(constraints, &ir.UniqueConstraint{Fields: []string{field.Name}})
	}

	out := make(map[string]*ir.UniqueConstraint, len(constraints))
	for _, uc := range constraints {
		normalized := normalizedUnique(model, uc)
		key := ucKey(normalized)
		if existing := out[key]; existing != nil && existing.Name != "" {
			continue
		}
		out[key] = normalized
	}
	return out
}

func ucKey(uc *ir.UniqueConstraint) string {
	return strings.Join(uc.Fields, ",")
}

// ---------------------------------------------------------------------------
// Primary key
// ---------------------------------------------------------------------------

func comparePrimaryKey(cs *Changeset, model string, oldM, newM *ir.Model) {
	oldCols := pkString(oldM, oldM.PrimaryKey)
	newCols := pkString(newM, newM.PrimaryKey)
	if oldCols != newCols {
		cs.add(Change{Type: ChangePK, Model: model, OldValue: oldCols, NewValue: newCols})
	}
}

func pkString(model *ir.Model, pk *ir.PrimaryKey) string {
	if pk == nil {
		return ""
	}
	return strings.Join(normalizeFieldNames(model, pk.Fields), ",")
}

// ---------------------------------------------------------------------------
// Foreign keys (derived from relations with fields/references)
// ---------------------------------------------------------------------------

// foreignKey is a relation normalized to database names: Fields and
// References hold column names and ToModel the target table, which lives in
// ToSchema.
type foreignKey struct {
	*ir.Relation
	ToSchema string
}

func compareForeignKeys(cs *Changeset, model string, oldSchema, newSchema *ir.Schema, oldM, newM *ir.Model) {
	oldMap := mapFKRelations(oldSchema, oldM, oldM.Relations)
	newMap := mapFKRelations(newSchema, newM, newM.Relations)
	newByIdentity := mapFKRelationsByIdentity(newMap)
	allKeys := mergedSortedKeys(oldMap, newMap)

	for _, k := range allKeys {
		or, inOld := oldMap[k]
		nr, inNew := newMap[k]

		switch {
		case inNew && !inOld:
			cs.add(Change{
				Type: AddFK, Model: model, NewValue: nr.ToModel,
				Details: fkDetails(nr),
			})
		case inOld && !inNew:
			rollback := DestructiveRollback
			if _, ok := newByIdentity[fkIdentityKey(or)]; ok {
				rollback = SafeRollback
			}
			cs.add(Change{
				Type: DropFK, Model: model, OldValue: or.ToModel,
				Details:  fkDetails(or),
				Rollback: rollback,
			})
		}
	}
}

func addForeignKeysForCreatedTables(cs *Changeset, newModels map[string]*ir.Model, createdTables []string) {
	for _, key := range createdTables {
		model := newModels[key]
		if model == nil {
			continue
		}
		tableName := model.TableName()
		relations := mapFKRelations(cs.New, model, model.Relations)
		keys := make([]string, 0, len(relations))
		for key := range relations {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			rel := relations[key]
			cs.add(Change{
				Type: AddFK, Model: tableName, Schema: model.Schema, NewValue: rel.ToModel,
				Details: fkDetails(rel),
			})
		}
	}
}

func mapFKRelations(schema *ir.Schema, model *ir.Model, rels []*ir.Relation) map[string]foreignKey {
	m := make(map[string]foreignKey)
	for _, r := range rels {
		if len(r.Fields) > 0 && len(r.References) > 0 {
			fk := foreignKey{Relation: normalizedRelation(schema, model, r)}
			if target := findModel(schema, r.ToModel); target != nil {
				fk.ToSchema = target.Schema
			}
			m[fkKey(fk)] = fk
		}
	}
	return m
}

func mapFKRelationsByIdentity(rels map[string]foreignKey) map[string]foreignKey {
	m := make(map[string]foreignKey, len(rels))
	for _, rel := range rels {
		m[fkIdentityKey(rel)] = rel
	}
	return m
}

func fkIdentityKey(r foreignKey) string {
	return strings.Join(r.Fields, ",") + "->" + tableKey(r.ToSchema, r.ToModel) + "(" + strings.Join(r.References, ",") + ")"
}

func fkKey(r foreignKey) string {
	return fkIdentityKey(r) +
		"|delete=" + effectiveReferentialAction(r.OnDelete) +
		"|update=" + effectiveReferentialAction(r.OnUpdate)
}

func fkDetails(r foreignKey) map[string]string {
	details := map[string]string{
		"fields":     strings.Join(r.Fields, ","),
		"references": strings.Join(r.References, ","),
		"toModel":    r.ToModel,
	}
	if r.ToSchema != "" {
		details["toSchema"] = r.ToSchema
	}
	if r.ConstraintName != "" {
		details["name"] = r.ConstraintName
	}
	if r.OnDelete != "" {
		details["onDelete"] = r.OnDelete
	}
	if r.OnUpdate != "" {
		details["onUpdate"] = r.OnUpdate
	}
	return details
}

// ---------------------------------------------------------------------------
// Generic helpers
// ---------------------------------------------------------------------------

// indexModels keys models by table identity: schema and table name.
func indexModels(models []*ir.Model) map[string]*ir.Model {
	m := make(map[string]*ir.Model, len(models))
	for _, model := range models {
		m[modelKey(model)] = model
	}
	return m
}

// tableKey identifies a table by schema and name. Tables without a schema
// are keyed by their bare name.
func tableKey(schemaName, table string) string {
	if schemaName == "" {
		return table
	}
	return schemaName + "\x00" + table
}

func modelKey(m *ir.Model) string {
	return tableKey(m.Schema, m.TableName())
}

// findTableModel looks up the model for a table in the given schema. When no
// model matches exactly, a single model with that table name in another
// schema is returned, which resolves tables moved by SetTableSchema.
func findTableModel(schema *ir.Schema, schemaName, table string) *ir.Model {
	if schema == nil {
		return nil
	}
	var byTable []*ir.Model
	for _, m := range schema.Models {
		if m.TableName() != table && m.Name != table {
			continue
		}
		if m.Schema == schemaName {
			return m
		}
		byTable = append(byTable, m)
	}
	if len(byTable) == 1 {
		return byTable[0]
	}
	return nil
}

// findTableField looks up a field by column or field name in a table.
func findTableField(schema *ir.Schema, schemaName, table, fieldName string) *ir.Field {
	return fieldByName(findTableModel(schema, schemaName, table), fieldName)
}

func fieldByName(m *ir.Model, fieldName string) *ir.Field {
	if m == nil {
		return nil
	}
	for _, f := range m.Fields {
		if f.Name == fieldName || columnName(f) == fieldName {
			return f
		}
	}
	return nil
}

func indexFields(fields []*ir.Field) map[string]*ir.Field {
	m := make(map[string]*ir.Field, len(fields))
	for _, f := range fields {
		m[columnName(f)] = f
	}
	return m
}

func mergedSortedKeys[V any](a, b map[string]V) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// findModel looks up a model by name in a schema, falling back to a table
// name. A model name wins over another model's table name.
func findModel(schema *ir.Schema, name string) *ir.Model {
	if schema == nil {
		return nil
	}
	for _, m := range schema.Models {
		if m.Name == name {
			return m
		}
	}
	for _, m := range schema.Models {
		if m.TableName() == name {
			return m
		}
	}
	return nil
}

// findField looks up a scalar field by model and field name.
func findField(schema *ir.Schema, modelName, fieldName string) *ir.Field {
	return fieldByName(findModel(schema, modelName), fieldName)
}

// ColumnName returns the database column name of a field: its @map name or
// its name in snake_case.
func ColumnName(f *ir.Field) string {
	return columnName(f)
}

// columnName returns the database column name for a field.
func columnName(f *ir.Field) string {
	if f.DBName != "" {
		return f.DBName
	}
	return toSnakeCase(f.Name)
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

func coalesceField(values ...*ir.Field) *ir.Field {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func normalizeFieldNames(model *ir.Model, names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = resolveColumnName(model, name)
	}
	return out
}

func resolveColumnName(model *ir.Model, name string) string {
	if model == nil {
		return name
	}
	for _, f := range model.Fields {
		if f.Name == name || columnName(f) == name {
			return columnName(f)
		}
	}
	return name
}

func normalizedIndex(provider string, model *ir.Model, idx *ir.Index) *ir.Index {
	if idx == nil {
		return nil
	}
	clone := *idx
	clone.Fields = normalizeFieldNames(model, idx.Fields)
	clone.Columns = normalizeIndexColumns(model, idx)
	clone.Name = effectiveIndexName(provider, model, &clone)
	return &clone
}

func normalizeIndexColumns(model *ir.Model, idx *ir.Index) []ir.IndexColumn {
	cols := effectiveIndexColumns(idx)
	out := make([]ir.IndexColumn, len(cols))
	for i, col := range cols {
		out[i] = col
		if strings.TrimSpace(col.Expression) == "" {
			out[i].Field = resolveColumnName(model, col.Field)
		}
	}
	return out
}

func normalizedUnique(model *ir.Model, uc *ir.UniqueConstraint) *ir.UniqueConstraint {
	if uc == nil {
		return nil
	}
	clone := *uc
	clone.Fields = normalizeFieldNames(model, uc.Fields)
	return &clone
}

func normalizedRelation(schema *ir.Schema, model *ir.Model, rel *ir.Relation) *ir.Relation {
	if rel == nil {
		return nil
	}
	clone := *rel
	clone.Fields = normalizeFieldNames(model, rel.Fields)
	targetModel := findModel(schema, rel.ToModel)
	clone.References = normalizeFieldNames(targetModel, rel.References)
	if targetModel != nil {
		clone.ToModel = targetModel.TableName()
	}
	clone.OnDelete = normalizeReferentialAction(rel.OnDelete)
	clone.OnUpdate = normalizeReferentialAction(rel.OnUpdate)
	return &clone
}

func normalizeReferentialAction(action string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(action), "_", " ")) {
	case "":
		return ""
	case "cascade":
		return "CASCADE"
	case "restrict":
		return "RESTRICT"
	case "noaction", "no action":
		return "NO ACTION"
	case "setnull", "set null":
		return "SET NULL"
	case "setdefault", "set default":
		return "SET DEFAULT"
	default:
		return strings.ToUpper(action)
	}
}

func effectiveReferentialAction(action string) string {
	normalized := normalizeReferentialAction(action)
	if normalized == "" {
		return "NO ACTION"
	}
	return normalized
}
