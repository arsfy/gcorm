// planner.go implements the schema diff engine that compares two IR schemas
// and produces a structured changeset describing the migration steps.
package migrate

import (
	"sort"
	"strings"
	"unicode"

	"github.com/arsfy/gco-orm/pkg/schema/ir"
)

// ChangeType categorizes a schema change.
type ChangeType string

const (
	CreateTable  ChangeType = "CreateTable"
	DropTable    ChangeType = "DropTable"
	RenameTable  ChangeType = "RenameTable"
	AddColumn    ChangeType = "AddColumn"
	DropColumn   ChangeType = "DropColumn"
	RenameColumn ChangeType = "RenameColumn"
	AlterType    ChangeType = "AlterType"
	AlterNull    ChangeType = "AlterNullability"
	AlterDefault ChangeType = "AlterDefault"
	AddIndex     ChangeType = "AddIndex"
	DropIndex    ChangeType = "DropIndex"
	AddUnique    ChangeType = "AddUnique"
	DropUnique   ChangeType = "DropUnique"
	ChangePK     ChangeType = "ChangePrimaryKey"
	AddFK        ChangeType = "AddForeignKey"
	DropFK       ChangeType = "DropForeignKey"
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
	Field    string            // Column name (if applicable).
	OldValue string            // Previous value (for renames, type changes).
	NewValue string            // New value.
	Details  map[string]string // Additional context.
	Rollback RollbackSafety
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
	case CreateTable, AddColumn, AddIndex, AddUnique, AddFK:
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
	allNames := mergedSortedKeys(oldModels, newModels)
	var createdTables []string

	for _, name := range allNames {
		oldM, inOld := oldModels[name]
		newM, inNew := newModels[name]

		switch {
		case inNew && !inOld:
			cs.add(Change{Type: CreateTable, Model: name})
			createdTables = append(createdTables, name)
		case inOld && !inNew:
			cs.add(Change{Type: DropTable, Model: name})
		default:
			compareModels(cs, old, new, oldM, newM)
		}
	}

	addForeignKeysForCreatedTables(cs, newModels, createdTables)

	return cs
}

func (cs *Changeset) add(c Change) {
	c.Rollback = classifyRollback(c.Type)
	cs.Changes = append(cs.Changes, c)
}

// ---------------------------------------------------------------------------
// Model comparison
// ---------------------------------------------------------------------------

func compareModels(cs *Changeset, oldSchema, newSchema *ir.Schema, oldM, newM *ir.Model) {
	tableName := newM.TableName()
	compareFields(cs, tableName, oldM.ScalarFields(), newM.ScalarFields())
	compareIndexes(cs, tableName, oldM, newM)
	compareUniques(cs, tableName, oldM, newM)
	comparePrimaryKey(cs, tableName, oldM, newM)
	compareForeignKeys(cs, tableName, oldSchema, newSchema, oldM, newM)
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
			cs.add(Change{Type: AddColumn, Model: model, Field: fn, NewValue: nf.ScalarType})
		case inOld && !inNew:
			cs.add(Change{Type: DropColumn, Model: model, Field: fn, OldValue: of.ScalarType})
		default:
			compareField(cs, model, of, nf)
		}
	}
}

func compareField(cs *Changeset, model string, of, nf *ir.Field) {
	if of.ScalarType != nf.ScalarType {
		cs.add(Change{
			Type: AlterType, Model: model, Field: columnName(nf),
			OldValue: of.ScalarType, NewValue: nf.ScalarType,
		})
	}

	if of.IsOptional != nf.IsOptional {
		cs.add(Change{
			Type: AlterNull, Model: model, Field: columnName(nf),
			OldValue: nullLabel(of.IsOptional), NewValue: nullLabel(nf.IsOptional),
		})
	}

	od := formatDefault(of.Default)
	nd := formatDefault(nf.Default)
	if od != nd {
		cs.add(Change{
			Type: AlterDefault, Model: model, Field: columnName(nf),
			OldValue: od, NewValue: nd,
		})
	}

	if of.IsUnique != nf.IsUnique {
		changeType := AddUnique
		if !nf.IsUnique {
			changeType = DropUnique
		}
		cs.add(Change{
			Type:  changeType,
			Model: model,
			Details: map[string]string{
				"fields": columnName(coalesceField(nf, of)),
			},
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
	if d.IsFunction {
		if len(d.FuncArgs) > 0 {
			return d.FuncName + "(" + strings.Join(d.FuncArgs, ",") + ")"
		}
		return d.FuncName + "()"
	}
	return d.Value
}

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

func compareIndexes(cs *Changeset, model string, oldM, newM *ir.Model) {
	oldMap := mapIndexes(oldM, oldM.Indexes)
	newMap := mapIndexes(newM, newM.Indexes)
	allKeys := mergedSortedKeys(oldMap, newMap)

	for _, k := range allKeys {
		oi, inOld := oldMap[k]
		ni, inNew := newMap[k]

		switch {
		case inNew && !inOld:
			cs.add(Change{
				Type: AddIndex, Model: model, NewValue: ni.Name,
				Details: map[string]string{
					"fields": strings.Join(ni.Fields, ","),
					"unique": boolStr(ni.IsUnique),
				},
			})
		case inOld && !inNew:
			cs.add(Change{
				Type: DropIndex, Model: model, OldValue: oi.Name,
				Details: map[string]string{
					"fields": strings.Join(oi.Fields, ","),
					"unique": boolStr(oi.IsUnique),
				},
			})
		default:
			if strings.Join(oi.Fields, ",") != strings.Join(ni.Fields, ",") || oi.IsUnique != ni.IsUnique {
				cs.add(Change{
					Type: DropIndex, Model: model, OldValue: oi.Name,
					Details: map[string]string{
						"fields": strings.Join(oi.Fields, ","),
						"unique": boolStr(oi.IsUnique),
					},
				})
				cs.add(Change{
					Type: AddIndex, Model: model, NewValue: ni.Name,
					Details: map[string]string{
						"fields": strings.Join(ni.Fields, ","),
						"unique": boolStr(ni.IsUnique),
					},
				})
			}
		}
	}
}

func mapIndexes(model *ir.Model, idxs []*ir.Index) map[string]*ir.Index {
	m := make(map[string]*ir.Index, len(idxs))
	for _, idx := range idxs {
		m[idxKey(model, idx)] = normalizedIndex(model, idx)
	}
	return m
}

func idxKey(model *ir.Model, idx *ir.Index) string {
	return effectiveIndexName(model, idx)
}

func effectiveIndexName(model *ir.Model, idx *ir.Index) string {
	if idx == nil {
		return ""
	}
	if idx.Name != "" {
		return idx.Name
	}
	return defaultIndexName(model, normalizeFieldNames(model, idx.Fields))
}

func defaultIndexName(model *ir.Model, fields []string) string {
	tableName := ""
	if model != nil {
		tableName = model.TableName()
	}
	return "idx_" + tableName + "_" + strings.Join(fields, "_")
}

// ---------------------------------------------------------------------------
// Unique constraints
// ---------------------------------------------------------------------------

func compareUniques(cs *Changeset, model string, oldM, newM *ir.Model) {
	oldMap := make(map[string]*ir.UniqueConstraint, len(oldM.UniqueConstraints))
	for _, uc := range oldM.UniqueConstraints {
		normalized := normalizedUnique(oldM, uc)
		oldMap[ucKey(normalized)] = normalized
	}
	newMap := make(map[string]*ir.UniqueConstraint, len(newM.UniqueConstraints))
	for _, uc := range newM.UniqueConstraints {
		normalized := normalizedUnique(newM, uc)
		newMap[ucKey(normalized)] = normalized
	}
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
		}
	}
}

func ucKey(uc *ir.UniqueConstraint) string {
	if uc.Name != "" {
		return uc.Name
	}
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

func compareForeignKeys(cs *Changeset, model string, oldSchema, newSchema *ir.Schema, oldM, newM *ir.Model) {
	oldMap := mapFKRelations(oldSchema, oldM, oldM.Relations)
	newMap := mapFKRelations(newSchema, newM, newM.Relations)
	allKeys := mergedSortedKeys(oldMap, newMap)

	for _, k := range allKeys {
		or, inOld := oldMap[k]
		nr, inNew := newMap[k]

		switch {
		case inNew && !inOld:
			cs.add(Change{
				Type: AddFK, Model: model, NewValue: nr.ToModel,
				Details: map[string]string{
					"fields":     strings.Join(nr.Fields, ","),
					"references": strings.Join(nr.References, ","),
					"toModel":    nr.ToModel,
				},
			})
		case inOld && !inNew:
			cs.add(Change{
				Type: DropFK, Model: model, OldValue: or.ToModel,
				Details: map[string]string{
					"fields":     strings.Join(or.Fields, ","),
					"references": strings.Join(or.References, ","),
					"toModel":    or.ToModel,
				},
			})
		}
	}
}

func addForeignKeysForCreatedTables(cs *Changeset, newModels map[string]*ir.Model, createdTables []string) {
	for _, tableName := range createdTables {
		model := newModels[tableName]
		if model == nil {
			continue
		}
		relations := mapFKRelations(cs.New, model, model.Relations)
		keys := make([]string, 0, len(relations))
		for key := range relations {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			rel := relations[key]
			cs.add(Change{
				Type: AddFK, Model: tableName, NewValue: rel.ToModel,
				Details: map[string]string{
					"fields":     strings.Join(rel.Fields, ","),
					"references": strings.Join(rel.References, ","),
					"toModel":    rel.ToModel,
				},
			})
		}
	}
}

func mapFKRelations(schema *ir.Schema, model *ir.Model, rels []*ir.Relation) map[string]*ir.Relation {
	m := make(map[string]*ir.Relation)
	for _, r := range rels {
		if len(r.Fields) > 0 && len(r.References) > 0 {
			normalized := normalizedRelation(schema, model, r)
			m[fkKey(normalized)] = normalized
		}
	}
	return m
}

func fkKey(r *ir.Relation) string {
	return strings.Join(r.Fields, ",") + "->" + r.ToModel + "(" + strings.Join(r.References, ",") + ")"
}

// ---------------------------------------------------------------------------
// Generic helpers
// ---------------------------------------------------------------------------

func indexModels(models []*ir.Model) map[string]*ir.Model {
	m := make(map[string]*ir.Model, len(models))
	for _, model := range models {
		m[model.TableName()] = model
	}
	return m
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

// findModel looks up a model by name in a schema.
func findModel(schema *ir.Schema, name string) *ir.Model {
	if schema == nil {
		return nil
	}
	for _, m := range schema.Models {
		if m.Name == name || m.TableName() == name {
			return m
		}
	}
	return nil
}

// findField looks up a scalar field by model and field name.
func findField(schema *ir.Schema, modelName, fieldName string) *ir.Field {
	m := findModel(schema, modelName)
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

func normalizedIndex(model *ir.Model, idx *ir.Index) *ir.Index {
	if idx == nil {
		return nil
	}
	clone := *idx
	clone.Fields = normalizeFieldNames(model, idx.Fields)
	clone.Name = effectiveIndexName(model, &clone)
	return &clone
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
	return &clone
}
