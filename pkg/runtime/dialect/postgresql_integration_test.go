package dialect

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// TestPostgreSQLClassifyErrorAgainstServer runs real statements through the
// pgx stdlib driver and checks the classification of the server errors.
// It requires GCO_TEST_POSTGRES_URL.
func TestPostgreSQLClassifyErrorAgainstServer(t *testing.T) {
	url := os.Getenv("GCO_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("GCO_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := fmt.Sprintf("gco_classify_%d", time.Now().UnixNano())
	mustExec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`CREATE SCHEMA "` + schema + `"`)
	defer db.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
	q := func(name string) string { return `"` + schema + `"."` + name + `"` }
	mustExec(`CREATE TABLE ` + q("parent") + ` (id int PRIMARY KEY)`)
	mustExec(`CREATE TABLE ` + q("item") + ` (
		id int PRIMARY KEY,
		email text CONSTRAINT item_email_key UNIQUE,
		qty int CONSTRAINT unique_violation_chk CHECK (qty >= 0),
		name varchar(3),
		parent_id int CONSTRAINT item_parent_fkey REFERENCES ` + q("parent") + `(id),
		label text NOT NULL DEFAULT 'x',
		area circle,
		CONSTRAINT no_overlap EXCLUDE USING gist (area WITH &&)
	)`)
	mustExec(`INSERT INTO ` + q("item") + ` (id, email, qty, area) VALUES (1, 'a@x', 1, '<(0,0),1>')`)

	tests := []struct {
		name       string
		stmt       string
		code       gcoerr.ErrorCode
		constraint string
	}{
		{"unique", `INSERT INTO ` + q("item") + ` (id, email) VALUES (2, 'a@x')`, gcoerr.CodeUniqueViolation, "item_email_key"},
		{"check named unique_violation_chk", `INSERT INTO ` + q("item") + ` (id, qty) VALUES (3, -1)`, gcoerr.CodeCheckViolation, "unique_violation_chk"},
		{"exclusion", `INSERT INTO ` + q("item") + ` (id, area) VALUES (4, '<(0,0),2>')`, gcoerr.CodeExclusionViolation, "no_overlap"},
		{"foreign key", `INSERT INTO ` + q("item") + ` (id, parent_id) VALUES (5, 99)`, gcoerr.CodeForeignKeyViolation, "item_parent_fkey"},
		{"not null", `INSERT INTO ` + q("item") + ` (id, label) VALUES (6, NULL)`, gcoerr.CodeNotNullViolation, ""},
		{"too long", `INSERT INTO ` + q("item") + ` (id, name) VALUES (7, 'abcdef')`, gcoerr.CodeDataTruncation, ""},
		{"out of range", `INSERT INTO ` + q("item") + ` (id, qty) VALUES (8, 99999999999)`, gcoerr.CodeNumericOutOfRange, ""},
		{"invalid text", `SELECT 'abc'::int`, gcoerr.CodeInvalidTextRepresentation, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, execErr := db.Exec(tc.stmt)
			if execErr == nil {
				t.Fatalf("expected error from %s", tc.stmt)
			}
			got := PostgreSQL{}.ClassifyError(execErr)
			if got == nil {
				t.Fatalf("ClassifyError(%v) = nil", execErr)
			}
			if got.Code != tc.code {
				t.Fatalf("Code = %v, want %v (%v)", got.Code, tc.code, execErr)
			}
			if tc.constraint != "" && got.Meta["constraint"] != tc.constraint {
				t.Fatalf("Meta[constraint] = %q, want %q", got.Meta["constraint"], tc.constraint)
			}
		})
	}
}
