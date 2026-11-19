package golang

import (
	"os"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// TestGeneratedClientFeatureSQLShapes checks the SQL the generated client emits
// for @@schema, defaults/cuid, bulk batching, single-row writes, upserts,
// where validation, list encoding, aggregates, savepoints and error hooks.
func TestGeneratedClientFeatureSQLShapes(t *testing.T) {
	testGeneratedClientFiles(t, featureSchema("postgresql"), map[string]string{
		"capture_driver_test.go":      featureCaptureDriverSource,
		"feature_shapes_test.go":      featureShapesTestSource,
		"list_codec_internal_test.go": listCodecInternalTestSource,
	})
}

// TestGeneratedClientSQLiteRuntime runs the generated client against a real
// SQLite database (modernc.org/sqlite).
func TestGeneratedClientSQLiteRuntime(t *testing.T) {
	testGeneratedClientFiles(t, featureSchema("sqlite"), map[string]string{
		"sqlite_runtime_test.go": sqliteRuntimeTestSource,
	})
}

// TestGeneratedClientEmptySchemaCompiles checks the model-less client (which
// still carries Tx/savepoint support) builds.
func TestGeneratedClientEmptySchemaCompiles(t *testing.T) {
	testGeneratedClientFiles(t, &ir.Schema{}, map[string]string{"empty_test.go": "package client_test\n"})
}

// TestGeneratedClientPostgresRuntime runs the generated client against the
// PostgreSQL database in GCO_TEST_POSTGRES_URL (skipped when unset). It drops
// and recreates the audit schema, the Role type and the public accounts/events
// tables in that database.
func TestGeneratedClientPostgresRuntime(t *testing.T) {
	if os.Getenv("GCO_TEST_POSTGRES_URL") == "" {
		t.Skip("GCO_TEST_POSTGRES_URL not set")
	}
	testGeneratedClientFiles(t, featureSchema("postgresql"), map[string]string{
		"postgres_runtime_test.go": postgresRuntimeTestSource,
	})
}

const featureCaptureDriverSource = `package client_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"strings"
)

type capturedStmt struct {
	query string
	args  []driver.Value
}

var capture struct {
	sync.Mutex
	stmts []capturedStmt
}

var errCaptureFail = errors.New("capture: forced failure")

func resetCapture() {
	capture.Lock()
	capture.stmts = nil
	capture.Unlock()
}

func captured() []capturedStmt {
	capture.Lock()
	defer capture.Unlock()
	return append([]capturedStmt(nil), capture.stmts...)
}

func record(queryText string, args []driver.NamedValue) bool {
	values := make([]driver.Value, len(args))
	fail := strings.Contains(queryText, "FAIL")
	for i, arg := range args {
		values[i] = arg.Value
		if arg.Value == "FAIL" {
			fail = true
		}
	}
	capture.Lock()
	capture.stmts = append(capture.stmts, capturedStmt{query: queryText, args: values})
	capture.Unlock()
	return fail
}

type featureDriver struct{}

func (featureDriver) Open(string) (driver.Conn, error) { return featureConn{}, nil }

type featureConn struct{}

func (featureConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare not supported") }
func (featureConn) Close() error                        { return nil }
func (featureConn) Begin() (driver.Tx, error)           { return featureTx{}, nil }

func (featureConn) QueryContext(ctx context.Context, queryText string, args []driver.NamedValue) (driver.Rows, error) {
	if record(queryText, args) {
		return nil, errCaptureFail
	}
	return &emptyRows{}, nil
}

func (featureConn) ExecContext(ctx context.Context, queryText string, args []driver.NamedValue) (driver.Result, error) {
	if record(queryText, args) {
		return nil, errCaptureFail
	}
	return driver.RowsAffected(1), nil
}

type featureTx struct{}

func (featureTx) Commit() error   { return nil }
func (featureTx) Rollback() error { return nil }

type emptyRows struct{}

func (*emptyRows) Columns() []string              { return nil }
func (*emptyRows) Close() error                   { return nil }
func (*emptyRows) Next(dest []driver.Value) error { return io.EOF }

func init() {
	sql.Register("capture_features", featureDriver{})
}
`

const featureShapesTestSource = `package client_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"example.com/gentest/gen/client"
	"example.com/gentest/gen/model"
	"example.com/gentest/gen/query"
)

func openFeatureClient(t *testing.T, dialect string, opts ...client.Option) *client.Client {
	t.Helper()
	db, err := sql.Open("capture_features", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	resetCapture()
	return client.New(db, append([]client.Option{client.WithDialect(dialect)}, opts...)...)
}

func assertCUID(t *testing.T, v driver.Value) string {
	t.Helper()
	s, ok := v.(string)
	if !ok || len(s) != 25 || s[0] != 'c' {
		t.Fatalf("expected a 25-char cuid, got %#v", v)
	}
	return s
}

func TestEnumConstantsUseDBValues(t *testing.T) {
	if model.RoleADMIN != "admin" || model.RoleUSER != "USER" {
		t.Fatalf("enum constants = %q, %q; want @map value admin and USER", model.RoleADMIN, model.RoleUSER)
	}
}

func TestCreateInputShape(t *testing.T) {
	typ := reflect.TypeFor[query.AccountCreateInput]()
	if _, ok := typ.FieldByName("Slug"); ok {
		t.Fatal("generated column Slug must not be part of AccountCreateInput")
	}
	for name, want := range map[string]string{
		"Id": "*int", "Code": "string", "Uid": "*string", "Role": "*model.Role",
		"Tags": "[]string", "Balance": "*string", "CreatedAt": "*time.Time", "Nickname": "*string", "Note": "*string",
	} {
		f, ok := typ.FieldByName(name)
		if !ok || f.Type.String() != want {
			t.Errorf("AccountCreateInput.%s type = %v, want %s", name, f.Type, want)
		}
	}
	if _, ok := reflect.TypeFor[model.Account]().FieldByName("Slug"); !ok {
		t.Fatal("model.Account should still expose the generated Slug column")
	}
}

func TestSchemaQualificationIsPostgresOnly(t *testing.T) {
	for dialect, want := range map[string]string{
		"postgresql": "SELECT COUNT(*) FROM \"audit\".\"accounts\"",
		"sqlite":     "SELECT COUNT(*) FROM \"accounts\"",
		"mysql":      "SELECT COUNT(*) FROM \x60accounts\x60",
	} {
		c := openFeatureClient(t, dialect)
		if _, err := c.Account.Count(context.Background()); err == nil {
			t.Fatalf("%s: Count on empty capture rows should fail with no rows", dialect)
		}
		if got := captured()[0].query; got != want {
			t.Errorf("%s: query = %q, want %q", dialect, got, want)
		}
	}
}

func TestBulkCreateUsesDefaultForOmittedFields(t *testing.T) {
	user := model.RoleUSER
	data := []query.AccountCreateInput{
		{Tenant: "t1", Email: "a@x"},
		{Tenant: "t1", Email: "b@x", Role: &user, Tags: []string{"x"}},
	}

	t.Run("postgresql", func(t *testing.T) {
		c := openFeatureClient(t, "postgresql")
		if _, err := c.Account.BulkCreate(data).Do(context.Background()); err != nil {
			t.Fatal(err)
		}
		stmts := captured()
		if len(stmts) != 1 {
			t.Fatalf("statements = %d, want 1", len(stmts))
		}
		want := "INSERT INTO \"audit\".\"accounts\" (\"id\", \"code\", \"uid\", \"tenant\", \"email\", \"role\", \"roles\", \"tags\", \"scores\", \"balance\", \"created_at\", \"nickname\", \"note\") VALUES " +
			"(DEFAULT, $1, DEFAULT, $2, $3, DEFAULT, $4, $5, $6, DEFAULT, DEFAULT, DEFAULT, $7), " +
			"(DEFAULT, $8, DEFAULT, $9, $10, $11, $12, $13, $14, DEFAULT, DEFAULT, DEFAULT, $15)"
		if stmts[0].query != want {
			t.Fatalf("query = %q\nwant    %q", stmts[0].query, want)
		}
		args := stmts[0].args
		if assertCUID(t, args[0]) == assertCUID(t, args[7]) {
			t.Fatal("generated cuids must differ")
		}
		wantRest := []driver.Value{"t1", "a@x", "{}", "{}", "{}", nil, "t1", "b@x", "USER", "{}", "{\"x\"}", "{}", nil}
		gotRest := append(append([]driver.Value(nil), args[1:7]...), args[8:]...)
		if !reflect.DeepEqual(gotRest, wantRest) {
			t.Fatalf("args = %#v, want %#v", gotRest, wantRest)
		}
	})

	t.Run("mysql", func(t *testing.T) {
		c := openFeatureClient(t, "mysql")
		if _, err := c.Account.BulkCreate(data).Do(context.Background()); err != nil {
			t.Fatal(err)
		}
		stmts := captured()
		want := "INSERT INTO \x60accounts\x60 (\x60id\x60, \x60code\x60, \x60uid\x60, \x60tenant\x60, \x60email\x60, \x60role\x60, \x60roles\x60, \x60tags\x60, \x60scores\x60, \x60balance\x60, \x60created_at\x60, \x60nickname\x60, \x60note\x60) VALUES " +
			"(DEFAULT, ?, DEFAULT, ?, ?, DEFAULT, ?, ?, ?, DEFAULT, DEFAULT, DEFAULT, ?), " +
			"(DEFAULT, ?, DEFAULT, ?, ?, ?, ?, ?, ?, DEFAULT, DEFAULT, DEFAULT, ?)"
		if len(stmts) != 1 || stmts[0].query != want {
			t.Fatalf("statements = %#v\nwant query %q", stmts, want)
		}
		if got := stmts[0].args[12]; got != "[\"x\"]" {
			t.Fatalf("mysql list arg = %#v, want JSON array", got)
		}
	})

	t.Run("sqlite groups rows by provided columns", func(t *testing.T) {
		c := openFeatureClient(t, "sqlite")
		// Consecutive rows with the same provided columns share a statement;
		// input order is preserved.
		rows := []query.AccountCreateInput{data[0], {Tenant: "t2", Email: "c@x"}, data[1]}
		if _, err := c.Account.BulkCreate(rows).Do(context.Background()); err != nil {
			t.Fatal(err)
		}
		stmts := captured()
		if len(stmts) != 2 {
			t.Fatalf("statements = %d, want 2: %#v", len(stmts), stmts)
		}
		want0 := "INSERT INTO \"accounts\" (\"code\", \"tenant\", \"email\", \"roles\", \"tags\", \"scores\", \"note\") VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?)"
		want1 := "INSERT INTO \"accounts\" (\"code\", \"tenant\", \"email\", \"role\", \"roles\", \"tags\", \"scores\", \"note\") VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
		if stmts[0].query != want0 || stmts[1].query != want1 {
			t.Fatalf("queries:\n%q\n%q\nwant:\n%q\n%q", stmts[0].query, stmts[1].query, want0, want1)
		}
		if stmts[0].args[2] != "a@x" || stmts[0].args[9] != "c@x" || stmts[1].args[3] != "USER" {
			t.Fatalf("unexpected sqlite args: %#v / %#v", stmts[0].args, stmts[1].args)
		}
	})
}

func TestBulkCreateBatchesRespectBindParameterLimit(t *testing.T) {
	events := make([]query.EventCreateInput, 40000)
	for i := range events {
		events[i] = query.EventCreateInput{Name: "e", N: i}
	}
	tests := []struct {
		dialect   string
		batchSize int
		wantRows  []int
	}{
		{"postgresql", 0, []int{32767, 7233}},
		{"postgresql", 1000000, []int{32767, 7233}},
		{"postgresql", 15000, []int{15000, 15000, 10000}},
		{"mysql", 0, []int{32767, 7233}},
		{"sqlite", 0, []int{16383, 16383, 7234}},
		{"sqlite", 1000000, []int{16383, 16383, 7234}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%d", tt.dialect, tt.batchSize), func(t *testing.T) {
			c := openFeatureClient(t, tt.dialect)
			n, err := c.Event.BulkCreate(events).BatchSize(tt.batchSize).Do(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			stmts := captured()
			if n != int64(len(stmts)) || len(stmts) != len(tt.wantRows) {
				t.Fatalf("statements = %d (affected %d), want %d", len(stmts), n, len(tt.wantRows))
			}
			for i, stmt := range stmts {
				if len(stmt.args) != 2*tt.wantRows[i] {
					t.Fatalf("statement %d has %d args, want %d", i, len(stmt.args), 2*tt.wantRows[i])
				}
			}
		})
	}
}

func TestUpdateOneDeleteOneLimitToSingleRow(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		dialect    string
		run        func(c *client.Client) error
		wantPrefix string
	}{
		{"postgresql", func(c *client.Client) error {
			_, err := c.Account.UpdateOne(ctx, query.Account.Tenant.Equals("t1"), query.Account.Note.Set("n"))
			return err
		}, "UPDATE \"audit\".\"accounts\" SET \"note\" = $1 WHERE \"id\" IN (SELECT \"id\" FROM \"audit\".\"accounts\" WHERE \"tenant\" = $2 LIMIT 1 FOR UPDATE) RETURNING \"id\","},
		{"postgresql", func(c *client.Client) error {
			_, err := c.Event.UpdateOne(ctx, query.Event.Name.Equals("dup"), query.Event.N.Set(9))
			return err
		}, "UPDATE \"events\" SET \"n\" = $1 WHERE (tableoid, ctid) IN (SELECT tableoid, ctid FROM \"events\" WHERE \"name\" = $2 LIMIT 1 FOR UPDATE) RETURNING \"name\", \"n\""},
		{"postgresql", func(c *client.Client) error {
			_, err := c.Account.DeleteOne(ctx, query.Account.Tenant.Equals("t1"))
			return err
		}, "DELETE FROM \"audit\".\"accounts\" WHERE \"id\" IN (SELECT \"id\" FROM \"audit\".\"accounts\" WHERE \"tenant\" = $1 LIMIT 1 FOR UPDATE) RETURNING \"id\","},
		{"postgresql", func(c *client.Client) error {
			_, err := c.Event.DeleteOne(ctx, query.Event.Name.Equals("dup"))
			return err
		}, "DELETE FROM \"events\" WHERE (tableoid, ctid) IN (SELECT tableoid, ctid FROM \"events\" WHERE \"name\" = $1 LIMIT 1 FOR UPDATE) RETURNING \"name\", \"n\""},
		{"mysql", func(c *client.Client) error {
			_, err := c.Account.UpdateOne(ctx, query.Account.Tenant.Equals("t1"), query.Account.Note.Set("n"))
			return err
		}, "UPDATE \x60accounts\x60 SET \x60note\x60 = ? WHERE \x60tenant\x60 = ? LIMIT 1"},
		{"mysql", func(c *client.Client) error {
			_, err := c.Account.DeleteOne(ctx, query.Account.Tenant.Equals("t1"))
			return err
		}, "DELETE FROM \x60accounts\x60 WHERE \x60tenant\x60 = ? LIMIT 1"},
		{"sqlite", func(c *client.Client) error {
			_, err := c.Account.UpdateOne(ctx, query.Account.Tenant.Equals("t1"), query.Account.Note.Set("n"))
			return err
		}, "UPDATE \"accounts\" SET \"note\" = ? WHERE rowid IN (SELECT rowid FROM \"accounts\" WHERE \"tenant\" = ? LIMIT 1)"},
		{"sqlite", func(c *client.Client) error {
			_, err := c.Event.DeleteOne(ctx, query.Event.Name.Equals("dup"))
			return err
		}, "DELETE FROM \"events\" WHERE rowid IN (SELECT rowid FROM \"events\" WHERE \"name\" = ? LIMIT 1)"},
	}
	for _, tt := range tests {
		c := openFeatureClient(t, tt.dialect)
		if err := tt.run(c); err != nil {
			t.Fatalf("%s: %v", tt.dialect, err)
		}
		stmts := captured()
		if len(stmts) != 1 || !strings.HasPrefix(stmts[0].query, tt.wantPrefix) {
			t.Fatalf("%s: statements = %#v\nwant prefix %q", tt.dialect, stmts, tt.wantPrefix)
		}
		if tt.dialect != "postgresql" && stmts[0].query != tt.wantPrefix {
			t.Fatalf("%s: query = %q, want %q", tt.dialect, stmts[0].query, tt.wantPrefix)
		}
	}
}

func TestUpsertConflictTargets(t *testing.T) {
	ctx := context.Background()

	t.Run("compound unique target", func(t *testing.T) {
		c := openFeatureClient(t, "postgresql")
		where := query.Account.AND(query.Account.Tenant.Equals("t1"), query.Account.Email.Equals("a@x"))
		_, err := c.Account.UpsertOne(ctx, where,
			[]query.AccountSetClause{query.Account.Tenant.Set("t1"), query.Account.Email.Set("a@x")},
			[]query.AccountSetClause{query.Account.Note.Set("again")})
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("UpsertOne on empty capture rows error = %v, want sql.ErrNoRows", err)
		}
		stmts := captured()
		want := "INSERT INTO \"audit\".\"accounts\" (\"tenant\", \"email\", \"code\") VALUES ($1, $2, $3) ON CONFLICT (\"tenant\", \"email\") DO UPDATE SET \"note\" = $4 RETURNING \"id\","
		if len(stmts) != 1 || !strings.HasPrefix(stmts[0].query, want) {
			t.Fatalf("statements = %#v\nwant prefix %q", stmts, want)
		}
		assertCUID(t, stmts[0].args[2])
	})

	t.Run("do nothing loads the existing row", func(t *testing.T) {
		c := openFeatureClient(t, "postgresql")
		_, err := c.Account.UpsertOne(ctx, query.Account.Code.Equals("k1"),
			[]query.AccountSetClause{query.Account.Code.Set("k1"), query.Account.Tenant.Set("t"), query.Account.Email.Set("e")}, nil)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("error = %v, want wrapped sql.ErrNoRows when the existing row is missing", err)
		}
		stmts := captured()
		if len(stmts) != 2 || !strings.Contains(stmts[0].query, "ON CONFLICT (\"code\") DO NOTHING RETURNING") ||
			!strings.HasPrefix(stmts[1].query, "SELECT ") || !strings.HasSuffix(stmts[1].query, "WHERE \"code\" = $1 LIMIT 1") {
			t.Fatalf("statements = %#v", stmts)
		}
	})

	t.Run("mysql empty update is a no-op", func(t *testing.T) {
		c := openFeatureClient(t, "mysql")
		if _, err := c.Account.UpsertOne(ctx, query.Account.Code.Equals("k1"),
			[]query.AccountSetClause{query.Account.Code.Set("k1"), query.Account.Tenant.Set("t"), query.Account.Email.Set("e")}, nil); err != nil {
			t.Fatal(err)
		}
		if got := captured()[0].query; !strings.HasSuffix(got, " ON DUPLICATE KEY UPDATE \x60code\x60 = \x60code\x60") {
			t.Fatalf("query = %q", got)
		}
	})

	t.Run("non-equality where is rejected", func(t *testing.T) {
		c := openFeatureClient(t, "postgresql")
		_, err := c.Account.UpsertOne(ctx, query.Account.Tenant.In("t1"),
			[]query.AccountSetClause{query.Account.Tenant.Set("t1"), query.Account.Email.Set("a@x")}, nil)
		if err == nil || !strings.Contains(err.Error(), "equality") {
			t.Fatalf("error = %v, want equality validation error", err)
		}
		if len(captured()) != 0 {
			t.Fatal("no statement should be executed")
		}
	})
}

func TestWhereRejectsUnknownFieldsAndOperators(t *testing.T) {
	ctx := context.Background()
	unknown := query.AccountWhereClause{Field: "missing", Operator: "=", Value: 1}
	badOp := query.AccountWhereClause{Field: "tenant", Operator: "LIKE", Value: "x"}
	c := openFeatureClient(t, "postgresql")

	checks := map[string]func() error{
		"DeleteMany NOT unknown": func() error { _, err := c.Account.DeleteMany(ctx, query.Account.NOT(unknown)); return err },
		"FindMany bad operator":  func() error { _, err := c.Account.FindMany(ctx, badOp); return err },
		"Count OR unknown": func() error {
			_, err := c.Account.Count(ctx, query.Account.OR(query.Account.Tenant.Equals("t"), unknown))
			return err
		},
		"UpdateMany unknown": func() error {
			_, err := c.Account.UpdateMany(ctx, []query.AccountWhereClause{unknown}, query.Account.Note.Set("x"))
			return err
		},
		"UpdateOne unknown":    func() error { _, err := c.Account.UpdateOne(ctx, unknown, query.Account.Note.Set("x")); return err },
		"DeleteOne unknown":    func() error { _, err := c.Account.DeleteOne(ctx, unknown); return err },
		"FindUnique unknown":   func() error { _, err := c.Account.FindUnique(ctx, unknown); return err },
		"AggregateWhere bad":   func() error { _, err := c.Account.AggregateWhere(ctx, []query.AccountWhereClause{badOp}); return err },
		"GroupByWhere unknown": func() error { _, err := c.Account.GroupByWhere(ctx, []string{"tenant"}, []query.AccountWhereClause{unknown}); return err },
		"NOT malformed": func() error {
			_, err := c.Account.DeleteMany(ctx, query.AccountWhereClause{Field: "__NOT__", Value: "oops"})
			return err
		},
	}
	for name, run := range checks {
		resetCapture()
		if err := run(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if stmts := captured(); len(stmts) != 0 {
			t.Errorf("%s: no SQL should run, got %#v", name, stmts)
		}
	}

	resetCapture()
	if _, err := c.Account.DeleteMany(ctx, query.Account.OR()); err != nil {
		t.Fatal(err)
	}
	if got := captured()[0].query; got != "DELETE FROM \"audit\".\"accounts\" WHERE 1 = 0" {
		t.Fatalf("empty OR query = %q", got)
	}
}

func TestGeneratedColumnsAreNotWritable(t *testing.T) {
	ctx := context.Background()
	c := openFeatureClient(t, "postgresql")
	slug := query.AccountSetClause{Field: "slug", Value: "x"}
	if _, err := c.Account.CreateOne(ctx, query.Account.Tenant.Set("t"), slug); err == nil || !strings.Contains(err.Error(), "generated column") {
		t.Fatalf("CreateOne error = %v", err)
	}
	if _, err := c.Account.UpdateMany(ctx, nil, slug); err == nil {
		t.Fatal("UpdateMany should reject generated columns")
	}
	if _, err := c.Account.UpsertOne(ctx, query.Account.Code.Equals("k"), []query.AccountSetClause{query.Account.Code.Set("k")}, []query.AccountSetClause{slug}); err == nil {
		t.Fatal("UpsertOne should reject generated columns")
	}
	if len(captured()) != 0 {
		t.Fatal("no SQL should run")
	}
}

func TestCreateOneGeneratesCUIDWhenOmitted(t *testing.T) {
	ctx := context.Background()
	c := openFeatureClient(t, "sqlite")
	if _, err := c.Account.Create().Set(query.Account.Tenant.Set("t"), query.Account.Email.Set("e")).Do(ctx); err != nil {
		t.Fatal(err)
	}
	stmt := captured()[0]
	if stmt.query != "INSERT INTO \"accounts\" (\"tenant\", \"email\", \"code\") VALUES (?, ?, ?)" {
		t.Fatalf("query = %q", stmt.query)
	}
	assertCUID(t, stmt.args[2])

	resetCapture()
	if _, err := c.Account.CreateOne(ctx, query.Account.Code.Set("mine"), query.Account.Tenant.Set("t")); err != nil {
		t.Fatal(err)
	}
	if stmt := captured()[0]; len(stmt.args) != 2 || stmt.args[0] != "mine" {
		t.Fatalf("explicit code must be kept: %#v", stmt)
	}
}

func TestListValuesEncodePerDialect(t *testing.T) {
	ctx := context.Background()
	tags := []string{"a\"b", "c\\d", "e,f", "NULL"}
	for dialect, want := range map[string]string{
		"postgresql": "{\"a\\\"b\",\"c\\\\d\",\"e,f\",\"NULL\"}",
		"mysql":      "[\"a\\\"b\",\"c\\\\d\",\"e,f\",\"NULL\"]",
		"sqlite":     "[\"a\\\"b\",\"c\\\\d\",\"e,f\",\"NULL\"]",
	} {
		c := openFeatureClient(t, dialect)
		if _, err := c.Account.FindMany(ctx, query.Account.Tags.Equals(tags), query.Account.Roles.In([]model.Role{model.RoleADMIN})); err != nil {
			t.Fatal(err)
		}
		stmt := captured()[0]
		args := stmt.args
		if dialect == "mysql" && !strings.HasSuffix(stmt.query, "WHERE \x60tags\x60 = CAST(? AS JSON) AND \x60roles\x60 IN (CAST(? AS JSON))") {
			t.Errorf("mysql list predicates must compare as JSON: %q", stmt.query)
		}
		if args[0] != want {
			t.Errorf("%s: tags arg = %#v, want %#v", dialect, args[0], want)
		}
		if dialect == "postgresql" && args[1] != "{\"admin\"}" {
			t.Errorf("roles arg = %#v, want {\"admin\"}", args[1])
		}
	}
}

func TestAggregateAndGroupByWhere(t *testing.T) {
	ctx := context.Background()
	c := openFeatureClient(t, "postgresql")
	_, _ = c.Account.AggregateWhere(ctx, []query.AccountWhereClause{query.Account.Tenant.Equals("t1")},
		query.AccountAggregateOption{Fn: "min", Field: query.AccountCreatedAtColumn},
		query.AccountAggregateOption{Fn: "AVG", Field: query.AccountIdColumn})
	_, _ = c.Account.GroupByWhere(ctx, []string{query.AccountTenantColumn}, []query.AccountWhereClause{query.Account.Tenant.Equals("t1")},
		query.AccountAggregateOption{Fn: "max", Field: query.AccountCreatedAtColumn})
	stmts := captured()
	want := []string{
		"SELECT COUNT(*), MIN(\"created_at\"), AVG(\"id\") FROM \"audit\".\"accounts\" WHERE \"tenant\" = $1",
		"SELECT \"tenant\", COUNT(*), MAX(\"created_at\") FROM \"audit\".\"accounts\" WHERE \"tenant\" = $1 GROUP BY \"tenant\"",
	}
	if len(stmts) != 2 || stmts[0].query != want[0] || stmts[1].query != want[1] {
		t.Fatalf("statements = %#v\nwant %#v", stmts, want)
	}
}

func TestNestedTxUsesSavepoints(t *testing.T) {
	ctx := context.Background()
	c := openFeatureClient(t, "postgresql")
	errInner := errors.New("inner failed")
	err := c.Tx(ctx, func(tx *client.Client) error {
		if err := tx.Tx(ctx, func(inner *client.Client) error { return errInner }); !errors.Is(err, errInner) {
			return fmt.Errorf("nested Tx error = %v", err)
		}
		return tx.Tx(ctx, func(inner *client.Client) error {
			return inner.Tx(ctx, func(*client.Client) error { return nil })
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, stmt := range captured() {
		got = append(got, stmt.query)
	}
	want := []string{
		"SAVEPOINT gco_sp_1", "ROLLBACK TO SAVEPOINT gco_sp_1", "RELEASE SAVEPOINT gco_sp_1",
		"SAVEPOINT gco_sp_2", "SAVEPOINT gco_sp_3", "RELEASE SAVEPOINT gco_sp_3", "RELEASE SAVEPOINT gco_sp_2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("savepoint statements = %#v, want %#v", got, want)
	}
}

type classifiedError struct{ cause error }

func (e *classifiedError) Error() string { return "classified: " + e.cause.Error() }
func (e *classifiedError) Unwrap() error { return e.cause }

func TestErrorHandlerWrapsDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := openFeatureClient(t, "sqlite", client.WithErrorHandler(func(err error) error {
		calls++
		return &classifiedError{cause: err}
	}))
	_, err := c.Account.CreateOne(ctx, query.Account.Tenant.Set("FAIL"), query.Account.Email.Set("e"))
	var classified *classifiedError
	if !errors.As(err, &classified) || !errors.Is(err, errCaptureFail) || !strings.HasPrefix(err.Error(), "Account.CreateOne: ") {
		t.Fatalf("CreateOne error = %v", err)
	}
	if _, err := c.RawExec(ctx, "FAIL"); !errors.As(err, &classified) {
		t.Fatalf("RawExec error = %v", err)
	}
	if _, err := c.Account.FindMany(ctx, query.AccountWhereClause{Field: "missing", Operator: "="}); errors.As(err, &classified) {
		t.Fatal("validation errors are not database errors and must not reach the handler")
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2", calls)
	}

	typedNil := openFeatureClient(t, "sqlite", client.WithErrorHandler(func(err error) error {
		var none *classifiedError
		return none
	}))
	if _, err := typedNil.RawExec(ctx, "FAIL"); err == nil || !errors.Is(err, errCaptureFail) {
		t.Fatalf("typed-nil handler result should keep the original error, got %v", err)
	}
}
`

const listCodecInternalTestSource = `package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func roundTripList[T any](t *testing.T, dialect string, in []T) []T {
	t.Helper()
	v, err := listValue{dialect: dialect, list: in}.Value()
	if err != nil {
		t.Fatalf("%s Value(%#v): %v", dialect, in, err)
	}
	var out []T
	if err := (listScanner{dialect: dialect, dest: &out}).Scan(v); err != nil {
		t.Fatalf("%s Scan(%#v): %v", dialect, v, err)
	}
	return out
}

func TestListCodecRoundTrips(t *testing.T) {
	ts := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		if got := roundTripList(t, dialect, []string{"a\"b", "c\\d", "e,f", "{g}", "NULL", "", " h "}); !reflect.DeepEqual(got, []string{"a\"b", "c\\d", "e,f", "{g}", "NULL", "", " h "}) {
			t.Errorf("%s strings = %#v", dialect, got)
		}
		if got := roundTripList(t, dialect, []int64{1, -2, 1 << 40}); !reflect.DeepEqual(got, []int64{1, -2, 1 << 40}) {
			t.Errorf("%s ints = %#v", dialect, got)
		}
		if got := roundTripList(t, dialect, []float64{1.5, -2e21}); !reflect.DeepEqual(got, []float64{1.5, -2e21}) {
			t.Errorf("%s floats = %#v", dialect, got)
		}
		if got := roundTripList(t, dialect, []bool{true, false}); !reflect.DeepEqual(got, []bool{true, false}) {
			t.Errorf("%s bools = %#v", dialect, got)
		}
		if got := roundTripList(t, dialect, []time.Time{ts}); len(got) != 1 || !got[0].Equal(ts) {
			t.Errorf("%s times = %#v", dialect, got)
		}
		if got := roundTripList(t, dialect, [][]byte{{0, 1, 0xff}}); !reflect.DeepEqual(got, [][]byte{{0, 1, 0xff}}) {
			t.Errorf("%s bytes = %#v", dialect, got)
		}
		if got := roundTripList(t, dialect, []json.RawMessage{json.RawMessage("{\"k\":[1,2]}")}); len(got) != 1 || string(got[0]) != "{\"k\":[1,2]}" {
			t.Errorf("%s json = %#v", dialect, got)
		}
		if got := roundTripList[string](t, dialect, nil); got == nil || len(got) != 0 {
			t.Errorf("%s nil list should encode as an empty list, got %#v", dialect, got)
		}
	}
}

func TestListScannerParsesPostgresOutput(t *testing.T) {
	var tags []string
	if err := (listScanner{dialect: "postgresql", dest: &tags}).Scan([]byte("{plain,\"quoted, x\",NULL,\"back\\\\slash\"}")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"plain", "quoted, x", "", "back\\slash"}) {
		t.Fatalf("tags = %#v", tags)
	}
	var times []time.Time
	if err := (listScanner{dialect: "postgresql", dest: &times}).Scan("{\"2024-01-02 03:04:05.5\",\"2024-01-02 03:04:05+02\"}"); err != nil {
		t.Fatal(err)
	}
	if times[0].Nanosecond() != 500000000 || times[1].UTC().Hour() != 1 {
		t.Fatalf("times = %#v", times)
	}
	var bounded []int
	if err := (listScanner{dialect: "postgresql", dest: &bounded}).Scan("[0:1]={7,8}"); err != nil || !reflect.DeepEqual(bounded, []int{7, 8}) {
		t.Fatalf("bounded = %#v, %v", bounded, err)
	}
	if err := (listScanner{dialect: "postgresql", dest: &bounded}).Scan(nil); err != nil || bounded != nil {
		t.Fatalf("NULL list = %#v, %v", bounded, err)
	}
	for _, bad := range []string{"{{1,2},{3,4}}", "{\"open", "1,2", "{1,x}"} {
		var ints []int
		if err := (listScanner{dialect: "postgresql", dest: &ints}).Scan(bad); err == nil {
			t.Errorf("Scan(%q) should fail, got %#v", bad, ints)
		}
	}
	var notSlice string
	if err := (listScanner{dialect: "postgresql", dest: &notSlice}).Scan("{}"); err == nil || !strings.Contains(err.Error(), "pointer to a slice") {
		t.Fatalf("non-slice destination error = %v", err)
	}
}

func TestNewCUIDIsUnique(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		id := newCUID()
		if len(id) != 25 || id[0] != 'c' || seen[id] {
			t.Fatalf("bad or duplicate cuid %q", id)
		}
		seen[id] = true
	}
}

func TestBulkRowsPerStatement(t *testing.T) {
	pg := &Client{dialect: "postgresql"}
	sqlite := &Client{dialect: "sqlite"}
	if got := pg.bulkRowsPerStatement(0, 13); got != 65535/13 {
		t.Errorf("pg default = %d", got)
	}
	if got := pg.bulkRowsPerStatement(10, 13); got != 10 {
		t.Errorf("pg explicit = %d", got)
	}
	if got := sqlite.bulkRowsPerStatement(1 << 30, 2); got != 32766/2 {
		t.Errorf("sqlite clamp = %d", got)
	}
	if got := sqlite.bulkRowsPerStatement(0, 40000); got != 1 {
		t.Errorf("wide rows = %d", got)
	}
}
`

const sqliteRuntimeTestSource = `package client_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"example.com/gentest/gen/client"
	"example.com/gentest/gen/model"
	"example.com/gentest/gen/query"
)

const sqliteDDL = ` + "`" + `
CREATE TABLE accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  code TEXT NOT NULL UNIQUE,
  uid TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))),
  tenant TEXT NOT NULL,
  email TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'admin',
  roles TEXT NOT NULL,
  tags TEXT NOT NULL,
  scores TEXT NOT NULL,
  balance TEXT NOT NULL DEFAULT '0',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  nickname TEXT DEFAULT 'anon',
  note TEXT,
  slug TEXT GENERATED ALWAYS AS (lower(email)) VIRTUAL,
  UNIQUE (tenant, email)
);
CREATE TABLE events (name TEXT NOT NULL, n INTEGER NOT NULL);
CREATE TABLE counters (id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER NOT NULL DEFAULT 0);
` + "`" + `

func strPtr(s string) *string { return &s }

// upperTags scans itself; Raw must not route it through the list codec.
type upperTags []string

func (u *upperTags) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	}
	*u = upperTags{strings.ToUpper(text)}
	return nil
}

type rawCustomRow struct {
	ID   int       ` + "`" + `db:"id"` + "`" + `
	Tags upperTags ` + "`" + `db:"tags"` + "`" + `
	IP   net.IP    ` + "`" + `db:"ip"` + "`" + `
}

func TestSQLiteRawKeepsCustomSliceTypes(t *testing.T) {
	ctx := context.Background()
	c := openSQLite(t)
	if _, err := c.Account.BulkCreate([]query.AccountCreateInput{{Tenant: "t", Email: "e", Tags: []string{"a"}}}).Do(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := client.Raw[rawCustomRow](ctx, c, "SELECT id, tags, CAST('10.0.0.1' AS BLOB) AS ip FROM accounts")
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Tags) != 1 || rows[0].Tags[0] != strings.ToUpper(` + "`" + `["a"]` + "`" + `) || string(rows[0].IP) != "10.0.0.1" {
		t.Fatalf("Raw custom types = %#v", rows)
	}
}

func TestSQLiteBulkCreateAllDefaultsWithConflictIgnore(t *testing.T) {
	ctx := context.Background()
	c := openSQLite(t)
	n, err := c.Counter.BulkCreate([]query.CounterCreateInput{{}, {}}).OnConflictDoNothing().Do(ctx)
	if err != nil || n != 2 {
		t.Fatalf("BulkCreate all-default rows with OnConflictDoNothing = %d, %v", n, err)
	}
}

func openSQLite(t *testing.T, opts ...client.Option) *client.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(sqliteDDL); err != nil {
		t.Fatal(err)
	}
	return client.New(db, opts...)
}

func TestSQLiteBulkCreateDefaultsAndLists(t *testing.T) {
	ctx := context.Background()
	c := openSQLite(t)
	user := model.RoleUSER
	nick := "nick"
	tags := []string{"q\"uote", "back\\slash", "com,ma", "", "NULL"}
	n, err := c.Account.BulkCreate([]query.AccountCreateInput{
		{Tenant: "t1", Email: "A@X"},
		{Tenant: "t1", Email: "b@x", Role: &user, Nickname: &nick, Tags: tags, Roles: []model.Role{model.RoleADMIN, model.RoleUSER}, Scores: []int{1, -2}},
		{Tenant: "t2", Email: "c@x"},
	}).Do(ctx)
	if err != nil || n != 3 {
		t.Fatalf("BulkCreate = %d, %v", n, err)
	}
	rows, err := c.Account.Query().OrderBy(query.Account.Id.Asc()).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	codes := map[string]bool{}
	for _, r := range rows {
		if len(r.Code) != 25 || r.Code[0] != 'c' || codes[r.Code] {
			t.Fatalf("bad cuid %q", r.Code)
		}
		codes[r.Code] = true
		if r.Uid == "" || r.Balance != "0" || r.CreatedAt.IsZero() {
			t.Fatalf("database defaults not applied: %#v", r)
		}
	}
	if rows[0].Role != model.RoleADMIN || rows[0].Nickname == nil || *rows[0].Nickname != "anon" || rows[0].Slug != "a@x" {
		t.Fatalf("row 0 = %#v", rows[0])
	}
	if rows[1].Role != model.RoleUSER || *rows[1].Nickname != "nick" {
		t.Fatalf("row 1 = %#v", rows[1])
	}
	if !reflect.DeepEqual(rows[1].Tags, tags) || !reflect.DeepEqual(rows[1].Roles, []model.Role{model.RoleADMIN, model.RoleUSER}) || !reflect.DeepEqual(rows[1].Scores, []int{1, -2}) {
		t.Fatalf("list round trip = %#v %#v %#v", rows[1].Tags, rows[1].Roles, rows[1].Scores)
	}
	if len(rows[0].Tags) != 0 {
		t.Fatalf("omitted list should be empty, got %#v", rows[0].Tags)
	}
	found, err := c.Account.Query().Where(query.Account.Tags.Equals(tags)).Do(ctx)
	if err != nil || len(found) != 1 || found[0].Id != rows[1].Id {
		t.Fatalf("list where = %#v, %v", found, err)
	}
	raw, err := client.Raw[model.Account](ctx, c, "SELECT id, tags FROM accounts WHERE id = ?", rows[1].Id)
	if err != nil || !reflect.DeepEqual(raw[0].Tags, tags) {
		t.Fatalf("Raw list scan = %#v, %v", raw, err)
	}
}

func TestSQLiteBulkCreateAboveParameterLimit(t *testing.T) {
	ctx := context.Background()
	c := openSQLite(t)
	events := make([]query.EventCreateInput, 20000)
	for i := range events {
		events[i] = query.EventCreateInput{Name: "e", N: i}
	}
	if n, err := c.Event.BulkCreate(events).Do(ctx); err != nil || n != 20000 {
		t.Fatalf("BulkCreate = %d, %v", n, err)
	}
	if n, err := c.Event.BulkCreate(events).BatchSize(1000000).Do(ctx); err != nil || n != 20000 {
		t.Fatalf("BulkCreate with oversized batch = %d, %v", n, err)
	}
}

func TestSQLiteSingleRowWritesAndUpserts(t *testing.T) {
	ctx := context.Background()
	c := openSQLite(t)
	if _, err := c.Event.BulkCreate([]query.EventCreateInput{{Name: "dup", N: 1}, {Name: "dup", N: 1}, {Name: "dup", N: 1}}).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Event.UpdateOne(ctx, query.Event.Name.Equals("dup"), query.Event.N.Set(99)); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Event.Count(ctx, query.Event.N.Equals(99)); n != 1 {
		t.Fatalf("UpdateOne changed %d rows, want 1", n)
	}
	if _, err := c.Event.DeleteOne(ctx, query.Event.Name.Equals("dup")); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Event.Count(ctx); n != 2 {
		t.Fatalf("DeleteOne left %d rows, want 2", n)
	}

	if _, err := c.Account.BulkCreate([]query.AccountCreateInput{{Tenant: "t1", Email: "a"}, {Tenant: "t1", Email: "b"}}).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Account.UpdateOne(ctx, query.Account.Tenant.Equals("t1"), query.Account.Note.Set("one")); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Account.Count(ctx, query.Account.Note.Equals(strPtr("one"))); n != 1 {
		t.Fatalf("Account UpdateOne changed %d rows, want 1", n)
	}

	where := query.Account.AND(query.Account.Tenant.Equals("t1"), query.Account.Email.Equals("b"))
	create := []query.AccountSetClause{query.Account.Tenant.Set("t1"), query.Account.Email.Set("b"), query.Account.Tags.Set([]string{"x"}), query.Account.Roles.Set(nil), query.Account.Scores.Set(nil)}
	if _, err := c.Account.UpsertOne(ctx, where, create, []query.AccountSetClause{query.Account.Note.Set("upserted")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Account.UpsertOne(ctx, where, create, nil); err != nil {
		t.Fatalf("UpsertOne DO NOTHING on existing row: %v", err)
	}
	got, err := c.Account.FindUnique(ctx, where)
	if err != nil || got == nil || got.Note == nil || *got.Note != "upserted" {
		t.Fatalf("compound upsert result = %#v, %v", got, err)
	}
	if n, _ := c.Account.Count(ctx); n != 2 {
		t.Fatalf("upserts created rows: count = %d, want 2", n)
	}

	if _, err := c.Account.DeleteMany(ctx, query.Account.NOT(query.AccountWhereClause{Field: "missing", Operator: "="})); err == nil {
		t.Fatal("NOT(unknown field) must fail instead of matching every row")
	}
	if n, _ := c.Account.Count(ctx); n != 2 {
		t.Fatalf("rows deleted by invalid where: count = %d", n)
	}

	agg, err := c.Account.AggregateWhere(ctx, []query.AccountWhereClause{query.Account.Tenant.Equals("t1")},
		query.AccountAggregateOption{Fn: "MIN", Field: query.AccountCreatedAtColumn},
		query.AccountAggregateOption{Fn: "MAX", Field: query.AccountEmailColumn})
	if err != nil || agg.Count != 2 || agg.Min["created_at"] == nil || agg.Max["email"] != "b" {
		t.Fatalf("AggregateWhere = %#v, %v", agg, err)
	}
	groups, err := c.Account.GroupByWhere(ctx, []string{query.AccountTenantColumn}, []query.AccountWhereClause{query.Account.Email.Equals("a")},
		query.AccountAggregateOption{Fn: "max", Field: query.AccountEmailColumn})
	if err != nil || len(groups) != 1 || groups[0].Group["tenant"] != "t1" || groups[0].Max["email"] != "a" {
		t.Fatalf("GroupByWhere = %#v, %v", groups, err)
	}
}

func TestSQLiteNestedTxSavepoint(t *testing.T) {
	ctx := context.Background()
	c := openSQLite(t)
	errInner := errors.New("inner")
	err := c.Tx(ctx, func(tx *client.Client) error {
		if _, err := tx.Event.CreateOne(ctx, query.Event.Name.Set("outer"), query.Event.N.Set(1)); err != nil {
			return err
		}
		err := tx.Tx(ctx, func(inner *client.Client) error {
			if _, err := inner.Event.CreateOne(ctx, query.Event.Name.Set("rolled-back"), query.Event.N.Set(2)); err != nil {
				return err
			}
			return errInner
		})
		if !errors.Is(err, errInner) {
			return err
		}
		return tx.Tx(ctx, func(inner *client.Client) error {
			_, err := inner.Event.CreateOne(ctx, query.Event.Name.Set("kept"), query.Event.N.Set(3))
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := c.Event.Query().OrderBy(query.Event.N.Asc()).Do(ctx)
	if err != nil || len(events) != 2 || events[0].Name != "outer" || events[1].Name != "kept" {
		t.Fatalf("events after nested tx = %#v, %v", events, err)
	}
}

func TestSQLiteErrorHandlerAndGeneratedColumn(t *testing.T) {
	ctx := context.Background()
	var seen []error
	c := openSQLite(t, client.WithErrorHandler(func(err error) error {
		seen = append(seen, err)
		return err
	}))
	if _, err := c.Account.CreateOne(ctx, query.Account.Code.Set("same"), query.Account.Tenant.Set("t"), query.Account.Email.Set("e"), query.Account.Roles.Set(nil), query.Account.Tags.Set(nil), query.Account.Scores.Set(nil)); err != nil {
		t.Fatal(err)
	}
	_, err := c.Account.CreateOne(ctx, query.Account.Code.Set("same"), query.Account.Tenant.Set("t2"), query.Account.Email.Set("e"), query.Account.Roles.Set(nil), query.Account.Tags.Set(nil), query.Account.Scores.Set(nil))
	if err == nil || len(seen) != 1 || !errors.Is(err, seen[0]) || !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("duplicate CreateOne error = %v (handler saw %v)", err, seen)
	}
	if _, err := c.Account.UpdateMany(ctx, nil, query.AccountSetClause{Field: "slug", Value: "x"}); err == nil {
		t.Fatal("writing a generated column must fail")
	}
}
`

const postgresRuntimeTestSource = `package client_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"example.com/gentest/gen/client"
	"example.com/gentest/gen/model"
	"example.com/gentest/gen/query"
)

const postgresDDL = ` + "`" + `
DROP SCHEMA IF EXISTS audit CASCADE;
DROP TABLE IF EXISTS public.accounts;
DROP TABLE IF EXISTS public.events;
DROP TYPE IF EXISTS "Role";
CREATE SCHEMA audit;
CREATE TYPE "Role" AS ENUM ('admin', 'USER');
CREATE TABLE audit.accounts (
  id integer GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  code text NOT NULL UNIQUE,
  uid uuid NOT NULL DEFAULT gen_random_uuid(),
  tenant text NOT NULL,
  email text NOT NULL,
  role "Role" NOT NULL DEFAULT 'admin',
  roles "Role"[] NOT NULL,
  tags text[] NOT NULL,
  scores integer[] NOT NULL,
  balance numeric(12,2) NOT NULL DEFAULT 0,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  nickname text DEFAULT 'anon',
  note text,
  slug text GENERATED ALWAYS AS (lower(email)) STORED,
  UNIQUE (tenant, email)
);
CREATE TABLE public.accounts (decoy integer);
CREATE TABLE public.events (name text NOT NULL, n integer NOT NULL);
` + "`" + `

func strPtr(s string) *string { return &s }

func openPostgres(t *testing.T, opts ...client.Option) *client.Client {
	t.Helper()
	db, err := sql.Open("pgx", os.Getenv("GCO_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(postgresDDL); err != nil {
		t.Fatal(err)
	}
	return client.New(db, append([]client.Option{client.WithDialect("postgresql")}, opts...)...)
}

func TestPostgresGeneratedClient(t *testing.T) {
	ctx := context.Background()
	var handled []error
	c := openPostgres(t, client.WithErrorHandler(func(err error) error {
		handled = append(handled, err)
		return err
	}))

	// BulkCreate with omitted defaults uses DEFAULT and client-side cuid().
	user := model.RoleUSER
	nick := "nick"
	inserted, err := c.Account.BulkCreate([]query.AccountCreateInput{
		{Tenant: "t1", Email: "A@X"},
		{Tenant: "t1", Email: "b@x", Role: &user, Nickname: &nick},
		{Tenant: "t2", Email: "c@x"},
	}).DoReturning(ctx)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	if len(inserted) != 3 {
		t.Fatalf("inserted = %d", len(inserted))
	}
	for _, a := range inserted {
		if a.Id <= 0 || len(a.Uid) != 36 || len(a.Code) != 25 || a.Code[0] != 'c' || a.Balance != "0.00" ||
			time.Since(a.CreatedAt) > 24*time.Hour || time.Since(a.CreatedAt) < -24*time.Hour {
			t.Fatalf("defaults not applied: %#v", a)
		}
	}
	first, second := inserted[0], inserted[1]
	if first.Role != model.RoleADMIN || string(first.Role) != "admin" || first.Nickname == nil || *first.Nickname != "anon" || first.Note != nil || first.Slug != "a@x" {
		t.Fatalf("first row = %#v", first)
	}
	if second.Role != model.RoleUSER || *second.Nickname != "nick" {
		t.Fatalf("second row = %#v", second)
	}

	// The table reference is schema-qualified: public.accounts stays empty.
	if rows, err := c.RawRows(ctx, "SELECT count(*) AS n FROM audit.accounts"); err != nil || rows[0]["n"] != int64(3) {
		t.Fatalf("audit.accounts count = %#v, %v", rows, err)
	}
	if rows, err := c.RawRows(ctx, "SELECT count(*) AS n FROM public.accounts"); err != nil || rows[0]["n"] != int64(0) {
		t.Fatalf("public.accounts count = %#v, %v", rows, err)
	}

	// Scalar list round trip through PostgreSQL arrays.
	tags := []string{"q\"uote", "back\\slash", "com,ma", "{brace}", "NULL", "", " sp "}
	roles := []model.Role{model.RoleADMIN, model.RoleUSER}
	created, err := c.Account.CreateOne(ctx, query.Account.Tenant.Set("lists"), query.Account.Email.Set("l@x"),
		query.Account.Tags.Set(tags), query.Account.Roles.Set(roles), query.Account.Scores.Set([]int{1, -2, 3}))
	if err != nil {
		t.Fatalf("CreateOne lists: %v", err)
	}
	if !reflect.DeepEqual(created.Tags, tags) || !reflect.DeepEqual(created.Roles, roles) || !reflect.DeepEqual(created.Scores, []int{1, -2, 3}) {
		t.Fatalf("list round trip = %#v %#v %#v", created.Tags, created.Roles, created.Scores)
	}
	if len(created.Code) != 25 {
		t.Fatalf("CreateOne cuid = %q", created.Code)
	}
	found, err := c.Account.Query().Where(query.Account.Tags.Equals(tags), query.Account.Roles.In(roles)).Do(ctx)
	if err != nil || len(found) != 1 || found[0].Id != created.Id {
		t.Fatalf("list where = %#v, %v", found, err)
	}
	raw, err := client.Raw[model.Account](ctx, c, "SELECT id, tags, roles FROM audit.accounts WHERE id = $1", created.Id)
	if err != nil || !reflect.DeepEqual(raw[0].Tags, tags) || !reflect.DeepEqual(raw[0].Roles, roles) {
		t.Fatalf("Raw list scan = %#v, %v", raw, err)
	}

	// UpdateOne / DeleteOne touch a single row (primary key and ctid paths).
	if _, err := c.Account.UpdateOne(ctx, query.Account.Tenant.Equals("t1"), query.Account.Note.Set("one")); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Account.Count(ctx, query.Account.Note.Equals(strPtr("one"))); n != 1 {
		t.Fatalf("UpdateOne changed %d accounts, want 1", n)
	}
	if _, err := c.Event.BulkCreate([]query.EventCreateInput{{Name: "dup", N: 1}, {Name: "dup", N: 1}, {Name: "dup", N: 1}}).Do(ctx); err != nil {
		t.Fatal(err)
	}
	updated, err := c.Event.UpdateOne(ctx, query.Event.Name.Equals("dup"), query.Event.N.Set(99))
	if err != nil || updated == nil || updated.N != 99 {
		t.Fatalf("Event UpdateOne = %#v, %v", updated, err)
	}
	if n, _ := c.Event.Count(ctx, query.Event.N.Equals(99)); n != 1 {
		t.Fatalf("Event UpdateOne changed %d rows, want 1", n)
	}
	if deleted, err := c.Event.DeleteOne(ctx, query.Event.Name.Equals("dup")); err != nil || deleted == nil {
		t.Fatalf("Event DeleteOne = %#v, %v", deleted, err)
	}
	if n, _ := c.Event.Count(ctx, query.Event.Name.Equals("dup")); n != 2 {
		t.Fatalf("DeleteOne left %d rows, want 2", n)
	}

	// BulkCreate above the 65535 bind-parameter limit.
	events := make([]query.EventCreateInput, 40000)
	for i := range events {
		events[i] = query.EventCreateInput{Name: "bulk", N: i}
	}
	if n, err := c.Event.BulkCreate(events).Do(ctx); err != nil || n != 40000 {
		t.Fatalf("large BulkCreate = %d, %v", n, err)
	}
	if n, err := c.Event.BulkCreate(events).BatchSize(1000000).Do(ctx); err != nil || n != 40000 {
		t.Fatalf("large BulkCreate with oversized batch = %d, %v", n, err)
	}

	// Upsert with an empty update returns the existing row.
	existing, err := c.Account.UpsertOne(ctx, query.Account.Code.Equals(first.Code),
		[]query.AccountSetClause{query.Account.Code.Set(first.Code), query.Account.Tenant.Set("zz"), query.Account.Email.Set("zz"),
			query.Account.Roles.Set(nil), query.Account.Tags.Set(nil), query.Account.Scores.Set(nil)}, nil)
	if err != nil || existing == nil || existing.Id != first.Id || existing.Tenant != "t1" {
		t.Fatalf("UpsertOne DO NOTHING = %#v, %v", existing, err)
	}

	// Compound unique upsert.
	compound := query.Account.AND(query.Account.Tenant.Equals("t1"), query.Account.Email.Equals("b@x"))
	up, err := c.Account.UpsertOne(ctx, compound,
		[]query.AccountSetClause{query.Account.Tenant.Set("t1"), query.Account.Email.Set("b@x"),
			query.Account.Roles.Set(nil), query.Account.Tags.Set(nil), query.Account.Scores.Set(nil)},
		[]query.AccountSetClause{query.Account.Note.Set("upserted")})
	if err != nil || up == nil || up.Id != second.Id || up.Note == nil || *up.Note != "upserted" {
		t.Fatalf("compound UpsertOne = %#v, %v", up, err)
	}

	// NOT(unknown) is an error, not a match-all filter.
	before, _ := c.Account.Count(ctx)
	if _, err := c.Account.DeleteMany(ctx, query.Account.NOT(query.AccountWhereClause{Field: "missing", Operator: "="})); err == nil {
		t.Fatal("NOT(unknown field) must fail")
	}
	if after, _ := c.Account.Count(ctx); after != before || before == 0 {
		t.Fatalf("invalid where deleted rows: %d -> %d", before, after)
	}

	// MIN/MAX keep the column type.
	agg, err := c.Account.AggregateWhere(ctx, []query.AccountWhereClause{query.Account.Tenant.Equals("t1")},
		query.AccountAggregateOption{Fn: "MIN", Field: query.AccountCreatedAtColumn},
		query.AccountAggregateOption{Fn: "MAX", Field: query.AccountCreatedAtColumn},
		query.AccountAggregateOption{Fn: "MIN", Field: query.AccountBalanceColumn},
		query.AccountAggregateOption{Fn: "AVG", Field: query.AccountIdColumn})
	if err != nil || agg.Count != 2 {
		t.Fatalf("AggregateWhere = %#v, %v", agg, err)
	}
	if _, ok := agg.Min["created_at"].(time.Time); !ok {
		t.Fatalf("MIN(created_at) = %#v, want time.Time", agg.Min["created_at"])
	}
	if _, ok := agg.Max["created_at"].(time.Time); !ok {
		t.Fatalf("MAX(created_at) = %#v, want time.Time", agg.Max["created_at"])
	}
	if agg.Min["balance"] != "0.00" || agg.Avg["id"] == nil {
		t.Fatalf("aggregates = %#v", agg)
	}
	groups, err := c.Account.GroupByWhere(ctx, []string{query.AccountTenantColumn}, []query.AccountWhereClause{query.Account.Tenant.In("t1", "t2")},
		query.AccountAggregateOption{Fn: "MAX", Field: query.AccountCreatedAtColumn})
	if err != nil || len(groups) != 2 {
		t.Fatalf("GroupByWhere = %#v, %v", groups, err)
	}

	// Nested Tx uses savepoints; a failed nested statement does not abort the outer tx.
	errInner := errors.New("inner")
	handled = nil
	err = c.Tx(ctx, func(tx *client.Client) error {
		if _, err := tx.Event.CreateOne(ctx, query.Event.Name.Set("tx-outer"), query.Event.N.Set(1)); err != nil {
			return err
		}
		if err := tx.Tx(ctx, func(inner *client.Client) error {
			if _, err := inner.Event.CreateOne(ctx, query.Event.Name.Set("tx-rolled-back"), query.Event.N.Set(2)); err != nil {
				return err
			}
			return errInner
		}); !errors.Is(err, errInner) {
			return err
		}
		dupErr := tx.Tx(ctx, func(inner *client.Client) error {
			_, err := inner.Account.CreateOne(ctx, query.Account.Code.Set(first.Code), query.Account.Tenant.Set("x"), query.Account.Email.Set("x"),
				query.Account.Roles.Set(nil), query.Account.Tags.Set(nil), query.Account.Scores.Set(nil))
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(dupErr, &pgErr) || pgErr.Code != "23505" || len(handled) != 1 {
			t.Errorf("duplicate in savepoint error = %v (handled %v)", dupErr, handled)
		}
		_, err := tx.Event.CreateOne(ctx, query.Event.Name.Set("tx-after-error"), query.Event.N.Set(3))
		return err
	})
	if err != nil {
		t.Fatalf("outer Tx: %v", err)
	}
	txEvents, err := c.Event.Query().Where(query.Event.Name.StartsWith("tx-")).OrderBy(query.Event.N.Asc()).Do(ctx)
	if err != nil || len(txEvents) != 2 || txEvents[0].Name != "tx-outer" || txEvents[1].Name != "tx-after-error" {
		t.Fatalf("events after nested tx = %#v, %v", txEvents, err)
	}

	// Generated columns are read-only.
	if _, err := c.Account.UpdateMany(ctx, nil, query.AccountSetClause{Field: "slug", Value: "x"}); err == nil || !strings.Contains(err.Error(), "generated column") {
		t.Fatalf("generated column write error = %v", err)
	}
}
`
