package migrate

import (
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testField(name, scalarType string) *ir.Field {
	return &ir.Field{
		Name:       name,
		Type:       ir.FieldKindScalar,
		ScalarType: scalarType,
	}
}

func testFieldOptional(name, scalarType string) *ir.Field {
	f := testField(name, scalarType)
	f.IsOptional = true
	return f
}

func testFieldWithDefault(name, scalarType string, def *ir.DefaultValue) *ir.Field {
	f := testField(name, scalarType)
	f.Default = def
	return f
}

func testModel(name string, fields ...*ir.Field) *ir.Model {
	pk := &ir.PrimaryKey{Fields: []string{"id"}}
	hasID := false
	for _, f := range fields {
		if f.Name == "id" {
			hasID = true
			break
		}
	}
	if !hasID {
		pk = nil
	}
	return &ir.Model{
		Name:       name,
		Fields:     fields,
		PrimaryKey: pk,
	}
}

func testSchema(models ...*ir.Model) *ir.Schema {
	return &ir.Schema{Models: models}
}

func testRelation(fromModel, toModel string, fields, references []string) *ir.Relation {
	return &ir.Relation{
		FromModel:  fromModel,
		ToModel:    toModel,
		Fields:     fields,
		References: references,
	}
}

// hasChange returns true if the changeset contains a change matching the given
// type and model (and optionally field, if non-empty).
func hasChange(cs *Changeset, ct ChangeType, model, field string) bool {
	for _, c := range cs.Changes {
		if c.Type == ct && c.Model == model {
			if field == "" || c.Field == field {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Planner tests
// ---------------------------------------------------------------------------

func TestDiff_EmptyDiff(t *testing.T) {
	schema := testSchema(
		testModel("User", testField("id", "Int"), testField("name", "String")),
	)
	cs := Diff(schema, schema)
	if len(cs.Changes) != 0 {
		t.Errorf("expected 0 changes, got %d: %+v", len(cs.Changes), cs.Changes)
	}
}

func TestDiff_AddModel(t *testing.T) {
	old := testSchema()
	new := testSchema(testModel("User", testField("id", "Int"), testField("name", "String")))

	cs := Diff(old, new)
	if !hasChange(cs, CreateTable, "User", "") {
		t.Fatal("expected CreateTable for User")
	}
}

func TestDiff_DropModel(t *testing.T) {
	old := testSchema(testModel("User", testField("id", "Int")))
	new := testSchema()

	cs := Diff(old, new)
	if !hasChange(cs, DropTable, "User", "") {
		t.Fatal("expected DropTable for User")
	}
}

func TestDiff_AddColumn(t *testing.T) {
	old := testSchema(testModel("User", testField("id", "Int")))
	new := testSchema(testModel("User", testField("id", "Int"), testField("email", "String")))

	cs := Diff(old, new)
	if !hasChange(cs, AddColumn, "User", "email") {
		t.Fatal("expected AddColumn for email")
	}
}

func TestDiff_DropColumn(t *testing.T) {
	old := testSchema(testModel("User", testField("id", "Int"), testField("email", "String")))
	new := testSchema(testModel("User", testField("id", "Int")))

	cs := Diff(old, new)
	if !hasChange(cs, DropColumn, "User", "email") {
		t.Fatal("expected DropColumn for email")
	}
}

func TestDiff_TypeChange(t *testing.T) {
	old := testSchema(testModel("User", testField("id", "Int"), testField("age", "String")))
	new := testSchema(testModel("User", testField("id", "Int"), testField("age", "Int")))

	cs := Diff(old, new)
	if !hasChange(cs, AlterType, "User", "age") {
		t.Fatal("expected AlterType for age")
	}
}

func TestDiff_NullabilityChange(t *testing.T) {
	old := testSchema(testModel("User", testField("id", "Int"), testField("bio", "String")))
	new := testSchema(testModel("User", testField("id", "Int"), testFieldOptional("bio", "String")))

	cs := Diff(old, new)
	if !hasChange(cs, AlterNull, "User", "bio") {
		t.Fatal("expected AlterNullability for bio")
	}
}

func TestDiff_DefaultChange(t *testing.T) {
	old := testSchema(testModel("User",
		testField("id", "Int"),
		testField("uid", "UUID"),
	))
	new := testSchema(testModel("User",
		testField("id", "Int"),
		testFieldWithDefault("uid", "UUID", &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}),
	))

	cs := Diff(old, new)
	if !hasChange(cs, AlterDefault, "User", "uid") {
		t.Fatal("expected AlterDefault for uid")
	}
}

func TestDiff_AddIndex(t *testing.T) {
	userOld := testModel("User", testField("id", "Int"), testField("email", "String"))
	userNew := testModel("User", testField("id", "Int"), testField("email", "String"))
	userNew.Indexes = []*ir.Index{{Name: "idx_email", Fields: []string{"email"}}}

	cs := Diff(testSchema(userOld), testSchema(userNew))
	if !hasChange(cs, AddIndex, "User", "") {
		t.Fatal("expected AddIndex for email index")
	}
}

func TestDiff_IndexOptionsTriggerRecreate(t *testing.T) {
	userOld := testModel("User", testField("id", "Int"), testField("email", "String"))
	userOld.Indexes = []*ir.Index{{
		Name:   "idx_email",
		Fields: []string{"email"},
		Columns: []ir.IndexColumn{{
			Field: "email",
			Sort:  "ASC",
		}},
	}}

	userNew := testModel("User", testField("id", "Int"), testField("email", "String"))
	userNew.Indexes = []*ir.Index{{
		Name:   "idx_email",
		Fields: []string{"email"},
		Where:  "email IS NOT NULL",
		Columns: []ir.IndexColumn{{
			Field:   "email",
			Sort:    "DESC",
			Nulls:   "LAST",
			OpClass: "text_ops",
		}},
	}}

	cs := Diff(testSchema(userOld), testSchema(userNew))
	if len(cs.Changes) != 2 || cs.Changes[0].Type != DropIndex || cs.Changes[1].Type != AddIndex {
		t.Fatalf("expected drop/add index for option change, got: %+v", cs.Changes)
	}
	add := cs.Changes[1]
	if add.Details["where"] != "email IS NOT NULL" {
		t.Fatalf("where detail = %q", add.Details["where"])
	}
	if add.Details["sorts"] != "DESC" || add.Details["nulls"] != "LAST" || add.Details["opclasses"] != "text_ops" {
		t.Fatalf("details = %+v", add.Details)
	}
}

func TestDiff_DefaultIndexNameMatchesIntrospectedIndex(t *testing.T) {
	oldPost := testModel("Post", testField("id", "Int"), testField("change", "String"))
	oldPost.DBName = "posts"
	oldPost.Indexes = []*ir.Index{{Name: "idx_posts_change", Fields: []string{"change"}}}

	newPost := testModel("Post", testField("id", "Int"), testField("change", "String"))
	newPost.DBName = "posts"
	newPost.Indexes = []*ir.Index{{Fields: []string{"change"}}}

	cs := Diff(testSchema(oldPost), testSchema(newPost))
	if hasChange(cs, AddIndex, "posts", "") || hasChange(cs, DropIndex, "posts", "") {
		t.Fatalf("expected default-named existing index to be reused, got changes: %+v", cs.Changes)
	}
}

func TestDiff_ComplexScenario(t *testing.T) {
	old := testSchema(
		testModel("User", testField("id", "Int"), testField("name", "String"), testField("age", "String")),
		testModel("Post", testField("id", "Int"), testField("title", "String")),
	)
	new := testSchema(
		testModel("User",
			testField("id", "Int"),
			testField("name", "String"),
			testField("age", "Int"),      // type change
			testField("email", "String"), // new column
		),
		// Post removed, Comment added
		testModel("Comment", testField("id", "Int"), testField("body", "String")),
	)

	cs := Diff(old, new)

	// Expect: AlterType(age), AddColumn(email), DropTable(Post), CreateTable(Comment) = 4 minimum
	if len(cs.Changes) < 4 {
		t.Errorf("expected at least 4 changes, got %d: %+v", len(cs.Changes), cs.Changes)
	}

	if !hasChange(cs, AlterType, "User", "age") {
		t.Error("missing AlterType for User.age")
	}
	if !hasChange(cs, AddColumn, "User", "email") {
		t.Error("missing AddColumn for User.email")
	}
	if !hasChange(cs, DropTable, "Post", "") {
		t.Error("missing DropTable for Post")
	}
	if !hasChange(cs, CreateTable, "Comment", "") {
		t.Error("missing CreateTable for Comment")
	}
}

func TestDiff_RollbackSafety(t *testing.T) {
	tests := []struct {
		ct   ChangeType
		want RollbackSafety
	}{
		{CreateTable, SafeRollback},
		{AddColumn, SafeRollback},
		{AddIndex, SafeRollback},
		{DropTable, DestructiveRollback},
		{DropColumn, DestructiveRollback},
		{DropIndex, DestructiveRollback},
		{AlterType, ReviewRequired},
		{AlterNull, ReviewRequired},
		{AlterDefault, ReviewRequired},
		{ChangePK, ReviewRequired},
	}
	for _, tt := range tests {
		got := classifyRollback(tt.ct)
		if got != tt.want {
			t.Errorf("classifyRollback(%s) = %s, want %s", tt.ct, got, tt.want)
		}
	}
}

func TestDiff_NilSchemas(t *testing.T) {
	user := testSchema(testModel("User", testField("id", "Int")))

	// nil old → treat as empty → should produce CreateTable
	cs1 := Diff(nil, user)
	if !hasChange(cs1, CreateTable, "User", "") {
		t.Error("Diff(nil, schema) should produce CreateTable")
	}

	// nil new → treat as empty → should produce DropTable
	cs2 := Diff(user, nil)
	if !hasChange(cs2, DropTable, "User", "") {
		t.Error("Diff(schema, nil) should produce DropTable")
	}
}

// ---------------------------------------------------------------------------
// DDL generator tests
// ---------------------------------------------------------------------------

func buildCreateTableChangeset() *Changeset {
	user := testModel("User",
		testField("id", "Int"),
		testField("email", "String"),
	)
	schema := testSchema(user)
	return &Changeset{
		Changes: []Change{
			{Type: CreateTable, Model: "User", Rollback: SafeRollback},
			{Type: AddColumn, Model: "User", Field: "name", NewValue: "String", Rollback: SafeRollback},
		},
		New: schema,
	}
}

func TestDDLGenerateUp_PostgreSQL(t *testing.T) {
	cs := buildCreateTableChangeset()
	gen := DDLGenerator{Dialect: "postgresql", Schema: cs.New}

	sql := gen.GenerateUp(cs)

	if !strings.Contains(sql, "CREATE TABLE") {
		t.Error("expected CREATE TABLE in output")
	}
	if !strings.Contains(sql, `"User"`) {
		t.Errorf("expected quoted table name, got:\n%s", sql)
	}
	if !strings.Contains(sql, `"id"`) {
		t.Error("expected quoted column id")
	}
	if !strings.Contains(sql, "INTEGER") {
		t.Error("expected INTEGER type for id")
	}
	if !strings.Contains(sql, "TEXT") {
		t.Error("expected TEXT type for email/name")
	}
	if !strings.Contains(sql, "ADD COLUMN") {
		t.Error("expected ADD COLUMN for name")
	}
}

func TestDDLGenerateUp_PostgreSQLDefaultFunctions(t *testing.T) {
	user := testModel("users",
		testFieldWithDefault("id", "String", &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}),
		testFieldWithDefault("createdAt", "DateTime", &ir.DefaultValue{IsFunction: true, FuncName: "now"}),
	)
	cs := &Changeset{
		Changes: []Change{{Type: CreateTable, Model: "users", Rollback: SafeRollback}},
		New:     testSchema(user),
	}
	gen := DDLGenerator{Dialect: "postgresql", Schema: cs.New}

	sql := gen.GenerateUp(cs)

	if !strings.Contains(sql, "DEFAULT gen_random_uuid()") {
		t.Fatalf("expected PostgreSQL UUID default mapping, got:\n%s", sql)
	}
	if !strings.Contains(sql, "DEFAULT NOW()") {
		t.Fatalf("expected PostgreSQL now() default mapping, got:\n%s", sql)
	}
	if !strings.Contains(sql, `"created_at"`) {
		t.Fatalf("expected camelCase field to use snake_case column, got:\n%s", sql)
	}
	if strings.Contains(sql, `"createdAt"`) {
		t.Fatalf("unexpected camelCase column in DDL:\n%s", sql)
	}
	if strings.Contains(sql, "DEFAULT uuid()") {
		t.Fatalf("unexpected raw uuid() default in PostgreSQL DDL:\n%s", sql)
	}
	if strings.Contains(sql, "DEFAULT now()") {
		t.Fatalf("unexpected raw now() default in PostgreSQL DDL:\n%s", sql)
	}
}

func TestColumnNameDefaultsToSnakeCase(t *testing.T) {
	field := testField("abcAbc", "String")
	if got := columnName(field); got != "abc_abc" {
		t.Fatalf("columnName() = %q, want %q", got, "abc_abc")
	}

	field.DBName = "custom_column"
	if got := columnName(field); got != "custom_column" {
		t.Fatalf("columnName() with DBName = %q, want %q", got, "custom_column")
	}
}

func TestDDLAlterDefaultUsesDialectMapping(t *testing.T) {
	gen := DDLGenerator{Dialect: "postgresql"}
	sql := gen.alterDefaultSQL(Change{Type: AlterDefault, Model: "users", Field: "id", NewValue: "uuid()"})

	if !strings.Contains(sql, "SET DEFAULT gen_random_uuid()") {
		t.Fatalf("expected PostgreSQL UUID default mapping in ALTER DEFAULT, got:\n%s", sql)
	}
}

func TestDiffAddsForeignKeysForNewTables(t *testing.T) {
	user := testModel("User", testField("id", "String"))
	user.DBName = "users"
	post := testModel(
		"Post",
		testField("id", "String"),
		testField("authorId", "String"),
	)
	post.DBName = "posts"
	post.Relations = []*ir.Relation{testRelation("Post", "User", []string{"authorId"}, []string{"id"})}

	cs := Diff(&ir.Schema{}, testSchema(post, user))

	if !hasChange(cs, CreateTable, "posts", "") {
		t.Fatal("expected CreateTable for posts")
	}
	if !hasChange(cs, CreateTable, "users", "") {
		t.Fatal("expected CreateTable for users")
	}
	if !hasChange(cs, AddFK, "posts", "") {
		t.Fatal("expected AddFK for posts in initial schema")
	}
}

func TestDDLGenerateUpOrdersForeignKeysAfterTables(t *testing.T) {
	user := testModel("User", testField("id", "String"))
	user.DBName = "users"
	post := testModel(
		"Post",
		testFieldWithDefault("id", "String", &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}),
		testField("authorId", "String"),
	)
	post.DBName = "posts"
	post.Relations = []*ir.Relation{testRelation("Post", "User", []string{"authorId"}, []string{"id"})}

	newSchema := testSchema(post, user)
	cs := Diff(&ir.Schema{}, newSchema)
	gen := DDLGenerator{Dialect: "postgresql", Schema: newSchema}

	sql := gen.GenerateUp(cs)

	usersIdx := strings.Index(sql, `CREATE TABLE "users"`)
	postsIdx := strings.Index(sql, `CREATE TABLE "posts"`)
	fkIdx := strings.Index(sql, `ALTER TABLE "posts" ADD CONSTRAINT "fk_posts_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id")`)
	if usersIdx == -1 || postsIdx == -1 || fkIdx == -1 {
		t.Fatalf("expected users table, posts table, and FK statement in SQL:\n%s", sql)
	}
	if fkIdx < usersIdx || fkIdx < postsIdx {
		t.Fatalf("expected FK statement after both CREATE TABLE statements:\n%s", sql)
	}
	postsCreateEnd := strings.Index(sql[postsIdx:], `);`)
	if postsCreateEnd == -1 {
		t.Fatalf("could not locate end of posts CREATE TABLE statement:\n%s", sql)
	}
	postsCreateSQL := sql[postsIdx : postsIdx+postsCreateEnd+2]
	if strings.Contains(postsCreateSQL, `FOREIGN KEY`) {
		t.Fatalf("expected CREATE TABLE posts without inline foreign key:\n%s", postsCreateSQL)
	}
}

func TestDDLForeignKeyReferentialActions(t *testing.T) {
	tag := testModel("Tag", testField("id", "BigInt"))
	tag.DBName = "tags"
	post := testModel("Post", testField("id", "BigInt"))
	post.DBName = "posts"
	postTag := testModel(
		"PostTag",
		testField("tagId", "BigInt"),
		testField("postId", "BigInt"),
	)
	postTag.DBName = "post_tags"
	postTag.PrimaryKey = &ir.PrimaryKey{Fields: []string{"tagId", "postId"}, IsComposite: true}
	tagRel := testRelation("PostTag", "Tag", []string{"tagId"}, []string{"id"})
	tagRel.OnDelete = "Cascade"
	postRel := testRelation("PostTag", "Post", []string{"postId"}, []string{"id"})
	postRel.OnDelete = "Cascade"
	postTag.Relations = []*ir.Relation{tagRel, postRel}

	newSchema := testSchema(post, postTag, tag)
	cs := Diff(&ir.Schema{}, newSchema)
	gen := DDLGenerator{Dialect: "postgresql", Schema: newSchema}

	sql := gen.GenerateUp(cs)

	expected := []string{
		`ALTER TABLE "post_tags" ADD CONSTRAINT "fk_post_tags_post_id" FOREIGN KEY ("post_id") REFERENCES "posts" ("id") ON DELETE CASCADE;`,
		`ALTER TABLE "post_tags" ADD CONSTRAINT "fk_post_tags_tag_id" FOREIGN KEY ("tag_id") REFERENCES "tags" ("id") ON DELETE CASCADE;`,
	}
	for _, want := range expected {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected FK SQL %q, got:\n%s", want, sql)
		}
	}
}

func TestDiffDetectsForeignKeyReferentialActionChange(t *testing.T) {
	user := testModel("User", testField("id", "String"))
	user.DBName = "users"
	oldPost := testModel("Post", testField("id", "String"), testField("authorId", "String"))
	oldPost.DBName = "posts"
	oldPost.Relations = []*ir.Relation{testRelation("Post", "User", []string{"authorId"}, []string{"id"})}

	newPost := testModel("Post", testField("id", "String"), testField("authorId", "String"))
	newPost.DBName = "posts"
	newRel := testRelation("Post", "User", []string{"authorId"}, []string{"id"})
	newRel.OnDelete = "Cascade"
	newPost.Relations = []*ir.Relation{newRel}

	cs := Diff(testSchema(oldPost, user), testSchema(newPost, user))
	if !hasChange(cs, DropFK, "posts", "") || !hasChange(cs, AddFK, "posts", "") {
		t.Fatalf("expected FK action change to drop and re-add FK, got: %+v", cs.Changes)
	}
}

func TestDDLGenerateUpDropsTablesByDependency(t *testing.T) {
	user := testModel("User", testField("id", "String"))
	user.DBName = "users"
	post := testModel(
		"Post",
		testField("id", "String"),
		testField("authorId", "String"),
	)
	post.DBName = "posts"
	post.Relations = []*ir.Relation{testRelation("Post", "User", []string{"authorId"}, []string{"id"})}

	oldSchema := testSchema(user, post)
	cs := Diff(oldSchema, &ir.Schema{})
	gen := DDLGenerator{Dialect: "postgresql", Schema: &ir.Schema{}}

	sql := gen.GenerateUp(cs)

	postsIdx := strings.Index(sql, `DROP TABLE "posts";`)
	usersIdx := strings.Index(sql, `DROP TABLE "users";`)
	if postsIdx == -1 || usersIdx == -1 {
		t.Fatalf("expected drop statements for posts and users:\n%s", sql)
	}
	if postsIdx > usersIdx {
		t.Fatalf("expected dependent table to drop before referenced table:\n%s", sql)
	}
}

func TestDDLGenerateUp_MySQL(t *testing.T) {
	cs := buildCreateTableChangeset()
	gen := DDLGenerator{Dialect: "mysql", Schema: cs.New}

	sql := gen.GenerateUp(cs)

	if !strings.Contains(sql, "CREATE TABLE") {
		t.Error("expected CREATE TABLE")
	}
	if !strings.Contains(sql, "`User`") {
		t.Errorf("expected backtick quoting, got:\n%s", sql)
	}
	if !strings.Contains(sql, "`id`") {
		t.Error("expected backtick column quoting")
	}
	if !strings.Contains(sql, "INT") {
		t.Error("expected INT type for id")
	}
	if !strings.Contains(sql, "VARCHAR(255)") {
		t.Error("expected VARCHAR(255) for email")
	}
}

func TestDDLGenerateUp_SQLite(t *testing.T) {
	cs := buildCreateTableChangeset()
	gen := DDLGenerator{Dialect: "sqlite", Schema: cs.New}

	sql := gen.GenerateUp(cs)

	if !strings.Contains(sql, "CREATE TABLE") {
		t.Error("expected CREATE TABLE")
	}
	if !strings.Contains(sql, `"User"`) {
		t.Error("expected double-quote quoting for sqlite")
	}
	if !strings.Contains(sql, "INTEGER") {
		t.Error("expected INTEGER type for id")
	}
	if !strings.Contains(sql, "TEXT") {
		t.Error("expected TEXT type for email")
	}
}

func TestDDLGenerateDown(t *testing.T) {
	cs := buildCreateTableChangeset()
	gen := DDLGenerator{Dialect: "postgresql", Schema: cs.New}

	sql := gen.GenerateDown(cs)

	// Reverse of CreateTable → DROP TABLE
	if !strings.Contains(sql, "DROP TABLE") {
		t.Errorf("expected DROP TABLE in down SQL, got:\n%s", sql)
	}
	// Reverse of AddColumn → DROP COLUMN
	if !strings.Contains(sql, "DROP COLUMN") {
		t.Errorf("expected DROP COLUMN in down SQL, got:\n%s", sql)
	}
}

func TestDDLDropIndexUsesIfExists(t *testing.T) {
	change := Change{
		Type:     DropIndex,
		Model:    "post_tags",
		OldValue: "idx_post_tags_post_id_tag_id",
		Details:  map[string]string{"fields": "post_id,tag_id"},
	}

	pg := DDLGenerator{Dialect: "postgresql"}.dropIndexSQL(change)
	if pg != `DROP INDEX IF EXISTS "idx_post_tags_post_id_tag_id";` {
		t.Fatalf("postgresql drop index SQL = %q", pg)
	}

	sqlite := DDLGenerator{Dialect: "sqlite"}.dropIndexSQL(change)
	if sqlite != `DROP INDEX IF EXISTS "idx_post_tags_post_id_tag_id";` {
		t.Fatalf("sqlite drop index SQL = %q", sqlite)
	}

	mysql := DDLGenerator{Dialect: "mysql"}.dropIndexSQL(change)
	if mysql != "DROP INDEX `idx_post_tags_post_id_tag_id` ON `post_tags`;" {
		t.Fatalf("mysql drop index SQL = %q", mysql)
	}
}

func TestDDLCreateIndexWithPredicateAndColumnOptions(t *testing.T) {
	announcement := testModel(
		"Announcement",
		testField("id", "Int"),
		testField("status", "Int"),
		testFieldOptional("publishedAt", "DateTime"),
	)
	announcement.DBName = "announcements"
	announcement.Fields[2].DBName = "published_at"
	announcement.Indexes = []*ir.Index{{
		Name:   "IDX_ANNOUNCEMENTS_USER",
		Fields: []string{"status", "publishedAt"},
		Where:  "status = 1 AND published_at IS NOT NULL",
		Columns: []ir.IndexColumn{
			{
				Field:     "status",
				Sort:      "DESC",
				Nulls:     "LAST",
				OpClass:   "int8_ops",
				Collation: "pg_catalog.default",
			},
			{
				Field:     "publishedAt",
				Sort:      "ASC",
				Nulls:     "LAST",
				OpClass:   "timestamptz_ops",
				Collation: "pg_catalog.default",
			},
		},
	}}
	cs := Diff(nil, testSchema(announcement))
	sql := DDLGenerator{Dialect: "postgresql", Schema: cs.New}.GenerateUp(cs)

	want := `CREATE INDEX "IDX_ANNOUNCEMENTS_USER" ON "announcements" ("status" COLLATE "pg_catalog"."default" int8_ops DESC NULLS LAST, "published_at" COLLATE "pg_catalog"."default" timestamptz_ops ASC NULLS LAST) WHERE status = 1 AND published_at IS NOT NULL;`
	if !strings.Contains(sql, want) {
		t.Fatalf("missing index SQL:\nwant: %s\n got:\n%s", want, sql)
	}
}

func TestDDLAddIndexWithPredicateAndColumnOptions(t *testing.T) {
	change := Change{
		Type:     AddIndex,
		Model:    "interaction_click_events",
		NewValue: "idx_ihce_retry",
		Details: map[string]string{
			"fields":    "clickhouse_recorded_at",
			"sorts":     "ASC",
			"nulls":     "LAST",
			"opclasses": "timestamptz_ops",
			"where":     "clickhouse_recorded_at IS NULL",
		},
	}

	sql := DDLGenerator{Dialect: "postgresql"}.addIndexSQL(change)
	want := `CREATE INDEX "idx_ihce_retry" ON "interaction_click_events" ("clickhouse_recorded_at" timestamptz_ops ASC NULLS LAST) WHERE clickhouse_recorded_at IS NULL;`
	if sql != want {
		t.Fatalf("add index SQL = %q\nwant %q", sql, want)
	}
}

func TestDDLDropUniqueUsesIfExists(t *testing.T) {
	change := Change{
		Type:     DropUnique,
		Model:    "users",
		OldValue: "uq_users_email",
		Details:  map[string]string{"fields": "email"},
	}

	pg := DDLGenerator{Dialect: "postgresql"}.dropUniqueSQL(change)
	if pg != `DROP INDEX IF EXISTS "uq_users_email";` {
		t.Fatalf("postgresql drop unique SQL = %q", pg)
	}
}

func TestDDLChangePKDropUsesIfExists(t *testing.T) {
	change := Change{
		Type:     ChangePK,
		Model:    "post_tags",
		OldValue: "post_id,tag_id",
		NewValue: "post_id",
	}

	pg := DDLGenerator{Dialect: "postgresql"}.changePKSQL(change)
	if !strings.Contains(pg, `ALTER TABLE "post_tags" DROP CONSTRAINT IF EXISTS "post_tags_pkey";`) {
		t.Fatalf("postgresql change PK SQL missing IF EXISTS:\n%s", pg)
	}
}

func TestDDLDropFKUsesIfExists(t *testing.T) {
	change := Change{
		Type:  DropFK,
		Model: "post_tags",
		Details: map[string]string{
			"fields": "post_id",
		},
	}

	pg := DDLGenerator{Dialect: "postgresql"}.dropFKSQL(change)
	if pg != `ALTER TABLE "post_tags" DROP CONSTRAINT IF EXISTS "fk_post_tags_post_id";` {
		t.Fatalf("postgresql drop FK SQL = %q", pg)
	}
}

func TestDDLDestructiveWarnings(t *testing.T) {
	cs := &Changeset{
		Changes: []Change{
			{Type: DropTable, Model: "OldTable", Rollback: DestructiveRollback},
			{Type: DropColumn, Model: "User", Field: "legacy", OldValue: "String", Rollback: DestructiveRollback},
		},
		Old: testSchema(
			testModel("OldTable", testField("id", "Int")),
			testModel("User", testField("id", "Int"), testField("legacy", "String")),
		),
	}
	gen := DDLGenerator{Dialect: "postgresql"}

	down := gen.GenerateDown(cs)

	if !strings.Contains(down, "WARNING") {
		t.Errorf("expected WARNING comment for destructive ops in down SQL, got:\n%s", down)
	}
	// Should have warnings for both drop table and drop column.
	if strings.Count(down, "WARNING") < 2 {
		t.Errorf("expected at least 2 WARNING comments, got:\n%s", down)
	}
}
