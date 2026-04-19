// planner.go implements the schema diff engine that compares two IR schemas
// and produces a structured changeset describing the migration steps.
package migrate

import (
	"sort"
	"strings"

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

	for _, name := range allNames {
		oldM, inOld := oldModels[name]
		newM, inNew := newModels[name]

		switch {
		case inNew && !inOld:
			cs.add(Change{Type: CreateTable, Model: name})
		case inOld && !inNew:
			cs.add(Change{Type: DropTable, Model: name})
		default:
			compareModels(cs, oldM, newM)
		}
	}

	return cs
}

func (cs *Changeset) add(c Change) {
	c.Rollback = classifyRollback(c.Type)
	cs.Changes = append(cs.Changes, c)
}

// ---------------------------------------------------------------------------
// Model comparison
// ---------------------------------------------------------------------------

func compareModels(cs *Changeset, oldM, newM *ir.Model) {
	name := newM.Name
	compareFields(cs, name, oldM.ScalarFields(), newM.ScalarFields())
	compareIndexes(cs, name, oldM.Indexes, newM.Indexes)
	compareUniques(cs, name, oldM.UniqueConstraints, newM.UniqueConstraints)
	comparePrimaryKey(cs, name, oldM.PrimaryKey, newM.PrimaryKey)
	compareForeignKeys(cs, name, oldM.Relations, newM.Relations)
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
			Type: AlterType, Model: model, Field: nf.Name,
			OldValue: of.ScalarType, NewValue: nf.ScalarType,
		})
	}

	if of.IsOptional != nf.IsOptional {
		cs.add(Change{
			Type: AlterNull, Model: model, Field: nf.Name,
			OldValue: nullLabel(of.IsOptional), NewValue: nullLabel(nf.IsOptional),
		})
	}

	od := formatDefault(of.Default)
	nd := formatDefault(nf.Default)
	if od != nd {
		cs.add(Change{
			Type: AlterDefault, Model: model, Field: nf.Name,
			OldValue: od, NewValue: nd,
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

func compareIndexes(cs *Changeset, model string, oldIdxs, newIdxs []*ir.Index) {
	oldMap := mapIndexes(oldIdxs)
	newMap := mapIndexes(newIdxs)
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

func mapIndexes(idxs []*ir.Index) map[string]*ir.Index {
	m := make(map[string]*ir.Index, len(idxs))
	for _, idx := range idxs {
		m[idxKey(idx)] = idx
	}
	return m
}

func idxKey(idx *ir.Index) string {
	if idx.Name != "" {
		return idx.Name
	}
	return strings.Join(idx.Fields, ",")
}

// ---------------------------------------------------------------------------
// Unique constraints
// ---------------------------------------------------------------------------

func compareUniques(cs *Changeset, model string, oldUCs, newUCs []*ir.UniqueConstraint) {
	oldMap := make(map[string]*ir.UniqueConstraint, len(oldUCs))
	for _, uc := range oldUCs {
		oldMap[ucKey(uc)] = uc
	}
	newMap := make(map[string]*ir.UniqueConstraint, len(newUCs))
	for _, uc := range newUCs {
		newMap[ucKey(uc)] = uc
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

func comparePrimaryKey(cs *Changeset, model string, oldPK, newPK *ir.PrimaryKey) {
	oldCols := pkString(oldPK)
	newCols := pkString(newPK)
	if oldCols != newCols {
		cs.add(Change{Type: ChangePK, Model: model, OldValue: oldCols, NewValue: newCols})
	}
}

func pkString(pk *ir.PrimaryKey) string {
	if pk == nil {
		return ""
	}
	return strings.Join(pk.Fields, ",")
}

// ---------------------------------------------------------------------------
// Foreign keys (derived from relations with fields/references)
// ---------------------------------------------------------------------------

func compareForeignKeys(cs *Changeset, model string, oldRels, newRels []*ir.Relation) {
	oldMap := mapFKRelations(oldRels)
	newMap := mapFKRelations(newRels)
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

func mapFKRelations(rels []*ir.Relation) map[string]*ir.Relation {
	m := make(map[string]*ir.Relation)
	for _, r := range rels {
		if len(r.Fields) > 0 && len(r.References) > 0 {
			m[fkKey(r)] = r
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
		m[model.Name] = model
	}
	return m
}

func indexFields(fields []*ir.Field) map[string]*ir.Field {
	m := make(map[string]*ir.Field, len(fields))
	for _, f := range fields {
		m[f.Name] = f
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
		if m.Name == name {
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
		if f.Name == fieldName {
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
	return f.Name
}
