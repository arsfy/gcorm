package golang

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func TestGeneratedClientBulkCreateAndRawRuntime(t *testing.T) {
	testGeneratedClient(t, goldenSchema(), generatedRuntimeTestSource)
}

func testGeneratedClient(t *testing.T, schema *ir.Schema, testSource string) {
	t.Helper()
	dir := t.TempDir()

	gen := NewGenerator(schema, WithModulePath("example.com/gentest/gen"))
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/gentest\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	for _, file := range files {
		path := filepath.Join(dir, "gen", file.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, file.Content, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	testPath := filepath.Join(dir, "gen", "client", "client_runtime_test.go")
	if err := os.WriteFile(testPath, []byte(testSource), 0o644); err != nil {
		t.Fatalf("write generated runtime test: %v", err)
	}

	args := []string{"test", "./gen/..."}
	if os.Getenv("GCO_RUN_GENERATED_BENCH") == "1" {
		args = []string{"test", "-run", "^$", "-bench", "BenchmarkGenerated", "-benchmem", "./gen/..."}
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated client tests failed: %v\n%s", err, out)
	}
	if os.Getenv("GCO_RUN_GENERATED_BENCH") == "1" {
		t.Logf("generated client benchmarks:\n%s", out)
	}
}

func TestGeneratedClientPackageScopedNames(t *testing.T) {
	schema := goldenSchema()
	for _, name := range []string{"UserActions", "UserQueryBuilder", "UserCreateBuilder", "UserCreateManyBuilder", "UserUpdateBuilder", "UserDeleteBuilder"} {
		schema.Models = append(schema.Models, &ir.Model{
			Name:       name,
			Fields:     []*ir.Field{{Name: "id", Type: ir.FieldKindScalar, ScalarType: "String", IsID: true}},
			PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
		})
	}
	schema.Models[0].Fields = append(schema.Models[0].Fields, &ir.Field{Name: "nameX", Type: ir.FieldKindScalar, ScalarType: "String"})
	schema.Models = append(schema.Models, &ir.Model{
		Name: "UserName",
		Fields: []*ir.Field{
			{Name: "id", Type: ir.FieldKindScalar, ScalarType: "String", IsID: true},
			{Name: "x", Type: ir.FieldKindRelation, ModelType: "User"},
		},
		PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
	})
	testGeneratedClient(t, schema, "package client_test\n")
}

func TestGeneratedClientMappedTags(t *testing.T) {
	schema := goldenSchema()
	schema.Models[0].Fields[1].DBName = "email`\"\\column"
	testGeneratedClient(t, schema, `package client_test

import (
	"reflect"
	"testing"
	"example.com/gentest/gen/model"
)

func TestMappedTag(t *testing.T) {
	f, ok := reflect.TypeFor[model.User]().FieldByName("Email")
	if !ok || f.Tag.Get("db") != "email\x60\"\\column" {
		t.Fatalf("Email tag = %q", f.Tag)
	}
}
`)
}

const generatedRuntimeTestSource = `package client_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"example.com/gentest/gen/client"
	"example.com/gentest/gen/model"
	"example.com/gentest/gen/query"
)

var captureState struct {
	sync.Mutex
	query string
	args  []driver.Value
}

type captureDriver struct{}

func (d captureDriver) Open(name string) (driver.Conn, error) {
	return captureConn{}, nil
}

type captureConn struct{}

func (c captureConn) Prepare(query string) (driver.Stmt, error) { return nil, nil }
func (c captureConn) Close() error { return nil }
func (c captureConn) Begin() (driver.Tx, error) { return nil, nil }

func (c captureConn) QueryContext(ctx context.Context, queryText string, args []driver.NamedValue) (driver.Rows, error) {
	captureState.Lock()
	captureState.query = queryText
	captureState.args = namedValues(args)
	captureState.Unlock()

	switch {
	case strings.Contains(queryText, "GROUP BY"):
		return &captureRows{
			cols: []string{"published", "count", "agg"},
			rows: [][]driver.Value{{true, int64(2), 3.0}},
		}, nil
	case strings.HasPrefix(queryText, "SELECT COUNT(*),"):
		return &captureRows{
			cols: []string{"count", "agg"},
			rows: [][]driver.Value{{int64(2), 41.5}},
		}, nil
	case strings.Contains(queryText, "RETURNING \"id\""):
		return &captureRows{cols: []string{"id"}, rows: [][]driver.Value{{"p1"}, {"p2"}}}, nil
	default:
		return &captureRows{
			cols: []string{"id", "title", "published", "author_id"},
			rows: [][]driver.Value{{"p1", "First", true, "u1"}},
		}, nil
	}
}

func (c captureConn) ExecContext(ctx context.Context, queryText string, args []driver.NamedValue) (driver.Result, error) {
	captureState.Lock()
	captureState.query = queryText
	captureState.args = namedValues(args)
	captureState.Unlock()
	return captureResult(2), nil
}

type captureRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *captureRows) Columns() []string { return r.cols }
func (r *captureRows) Close() error { return nil }
func (r *captureRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

type captureResult int64

func (r captureResult) LastInsertId() (int64, error) { return 0, nil }
func (r captureResult) RowsAffected() (int64, error) { return int64(r), nil }

func namedValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

func init() {
	sql.Register("capture_runtime", captureDriver{})
}

func openCaptureClient(t *testing.T, opts ...client.Option) *client.Client {
	t.Helper()
	db, err := sql.Open("capture_runtime", "")
	if err != nil {
		t.Fatal(err)
	}
	return client.New(db, opts...)
}

func TestBulkCreateDoReturningValues(t *testing.T) {
	c := openCaptureClient(t)
	defer c.Close()

	rows, err := c.Post.BulkCreate([]query.PostCreateInput{
		{Id: "p1", Title: "First", Published: true, AuthorId: "u1"},
		{Id: "p2", Title: "Second", Published: false, AuthorId: "u1"},
	}).
		OnConflictDoNothing(query.PostIdColumn).
		Returning(query.PostIdColumn).
		BatchSize(10).
		DoReturningValues(context.Background())
	if err != nil {
		t.Fatalf("DoReturningValues() error: %v", err)
	}
	if len(rows) != 2 || rows[0]["id"] != "p1" || rows[1]["id"] != "p2" {
		t.Fatalf("returned rows = %#v", rows)
	}

	captureState.Lock()
	gotQuery := captureState.query
	gotArgs := append([]driver.Value(nil), captureState.args...)
	captureState.Unlock()

	wantQuery := "INSERT INTO \"Post\" (\"id\", \"title\", \"published\", \"author_id\") VALUES ($1, $2, $3, $4), ($5, $6, $7, $8) ON CONFLICT (\"id\") DO NOTHING RETURNING \"id\""
	if gotQuery != wantQuery {
		t.Fatalf("query = %q, want %q", gotQuery, wantQuery)
	}
	wantArgs := []driver.Value{"p1", "First", true, "u1", "p2", "Second", false, "u1"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestLikeWildcardsAreEscaped(t *testing.T) {
	c := openCaptureClient(t)
	defer c.Close()

	_, err := c.Post.Query().
		Where(query.Post.Title.Contains("50%_off\\sale")).
		Do(context.Background())
	if err != nil {
		t.Fatalf("FindMany() error: %v", err)
	}

	captureState.Lock()
	gotQuery := captureState.query
	gotArgs := append([]driver.Value(nil), captureState.args...)
	captureState.Unlock()

	wantQuery := "SELECT \"id\", \"title\", \"published\", \"author_id\" FROM \"Post\" WHERE \"title\" LIKE $1 ESCAPE '\\'"
	if gotQuery != wantQuery {
		t.Fatalf("query = %q, want %q", gotQuery, wantQuery)
	}
	wantArgs := []driver.Value{"%50\\%\\_off\\\\sale%"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestSkipOnlyUsesDialectCompatibleLimit(t *testing.T) {
	tests := []struct {
		dialect string
		want    string
	}{
		{"postgresql", "SELECT \"id\", \"title\", \"published\", \"author_id\" FROM \"Post\" OFFSET 5"},
		{"mysql", "SELECT \x60id\x60, \x60title\x60, \x60published\x60, \x60author_id\x60 FROM \x60Post\x60 LIMIT 18446744073709551615 OFFSET 5"},
		{"sqlite", "SELECT \"id\", \"title\", \"published\", \"author_id\" FROM \"Post\" LIMIT -1 OFFSET 5"},
	}

	for _, tt := range tests {
		t.Run(tt.dialect, func(t *testing.T) {
			c := openCaptureClient(t, client.WithDialect(tt.dialect))
			defer c.Close()

			if _, err := c.Post.Query().Skip(5).Do(context.Background()); err != nil {
				t.Fatalf("FindMany() error: %v", err)
			}

			captureState.Lock()
			got := captureState.query
			captureState.Unlock()
			if got != tt.want {
				t.Fatalf("query = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRawScansStructsByDBTag(t *testing.T) {
	c := openCaptureClient(t)
	defer c.Close()

	posts, err := client.Raw[model.Post](context.Background(), c, "SELECT id, title, published, author_id FROM posts WHERE id = $1", "p1")
	if err != nil {
		t.Fatalf("Raw() error: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("len(posts) = %d", len(posts))
	}
	if posts[0].Id != "p1" || posts[0].Title != "First" || !posts[0].Published || posts[0].AuthorId != "u1" {
		t.Fatalf("post = %#v", posts[0])
	}
}

func TestRawRejectsNilInterfaceDestination(t *testing.T) {
	c := openCaptureClient(t)
	defer c.Close()

	_, err := client.Raw[any](context.Background(), c, "SELECT id FROM posts")
	if err == nil {
		t.Fatal("Raw[any]() should reject a non-struct destination")
	}
	if !strings.Contains(err.Error(), "destination type must be a struct") {
		t.Fatalf("Raw[any]() error = %q", err)
	}
}

func TestAggregateMapsUppercaseFn(t *testing.T) {
	c := openCaptureClient(t)
	defer c.Close()

	res, err := c.Post.Aggregate(context.Background(), query.PostAggregateOption{Fn: "AVG", Field: query.PostPublishedColumn})
	if err != nil {
		t.Fatalf("Aggregate() error: %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("Count = %d, want 2", res.Count)
	}
	if got := res.Avg["published"]; got == nil || *got != 41.5 {
		t.Fatalf("Avg[\"published\"] = %v, want 41.5", got)
	}
}

func TestGroupByMapsMixedCaseFn(t *testing.T) {
	c := openCaptureClient(t)
	defer c.Close()

	rows, err := c.Post.GroupBy(context.Background(), []string{query.PostPublishedColumn}, query.PostAggregateOption{Fn: "Sum", Field: query.PostPublishedColumn})
	if err != nil {
		t.Fatalf("GroupBy() error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Group["published"] != true {
		t.Fatalf("Group[\"published\"] = %#v, want true", rows[0].Group["published"])
	}
	if got := rows[0].Sum["published"]; got == nil || *got != 3.0 {
		t.Fatalf("Sum[\"published\"] = %v, want 3.0", got)
	}
}

func BenchmarkGeneratedFindManyEquals(b *testing.B) {
	db, err := sql.Open("capture_runtime", "")
	if err != nil {
		b.Fatal(err)
	}
	c := client.New(db)
	defer c.Close()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		posts, err := c.Post.Query().
			Where(
				query.Post.AuthorId.Equals("u1"),
				query.Post.Published.Equals(true),
			).
			Take(10).
			Do(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(posts) != 1 {
			b.Fatalf("len(posts) = %d", len(posts))
		}
	}
}

func BenchmarkGeneratedFindManyContainsEscaped(b *testing.B) {
	db, err := sql.Open("capture_runtime", "")
	if err != nil {
		b.Fatal(err)
	}
	c := client.New(db)
	defer c.Close()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		posts, err := c.Post.Query().
			Where(query.Post.Title.Contains("50%_off\\sale")).
			Take(10).
			Do(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(posts) != 1 {
			b.Fatalf("len(posts) = %d", len(posts))
		}
	}
}
`
