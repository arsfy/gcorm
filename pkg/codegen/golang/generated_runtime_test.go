package golang

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGeneratedClientBulkCreateAndRawRuntime(t *testing.T) {
	dir := t.TempDir()

	schema := goldenSchema()
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
	if err := os.WriteFile(testPath, []byte(generatedRuntimeTestSource), 0o644); err != nil {
		t.Fatalf("write generated runtime test: %v", err)
	}

	cmd := exec.Command("go", "test", "./gen/...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated client tests failed: %v\n%s", err, out)
	}
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
	case strings.Contains(queryText, "RETURNING id"):
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

func openCaptureClient(t *testing.T) *client.Client {
	t.Helper()
	db, err := sql.Open("capture_runtime", "")
	if err != nil {
		t.Fatal(err)
	}
	return client.New(db)
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

	wantQuery := "INSERT INTO Post (id, title, published, author_id) VALUES ($1, $2, $3, $4), ($5, $6, $7, $8) ON CONFLICT (id) DO NOTHING RETURNING id"
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

	wantQuery := "SELECT id, title, published, author_id FROM Post WHERE title LIKE $1 ESCAPE '\\'"
	if gotQuery != wantQuery {
		t.Fatalf("query = %q, want %q", gotQuery, wantQuery)
	}
	wantArgs := []driver.Value{"%50\\%\\_off\\\\sale%"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
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
`
