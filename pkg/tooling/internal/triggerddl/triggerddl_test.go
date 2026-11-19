package triggerddl

import (
	"reflect"
	"testing"
)

func TestSQLiteDefinitionParsesUpdateOfColumns(t *testing.T) {
	const tail = ` ON "items" FOR EACH ROW
BEGIN /* gcorm:trigger */
SELECT 1;
END;`
	for _, tt := range []struct {
		name, event string
		want        []string
	}{
		{"no columns", `AFTER UPDATE`, nil},
		{"quoted", `AFTER UPDATE OF "price_cents", "status"`, []string{"price_cents", "status"}},
		{"escaped quote", `AFTER UPDATE OF "we""ird"`, []string{`we"ird`}},
		{"rewritten quoting", "BEFORE UPDATE OF price_cents,`status`, [note]", []string{"price_cents", "status", "note"}},
		{"name containing ON", `AFTER UPDATE OF "x ON y"`, []string{"x ON y"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := SQLiteDefinition("audit", `CREATE TRIGGER "audit" `+tt.event+tail)
			if got == nil {
				t.Fatal("definition not recognized")
			}
			if !reflect.DeepEqual(got.Columns, tt.want) || got.Event != "UPDATE" || got.Body != "SELECT 1;" {
				t.Fatalf("SQLiteDefinition() = %+v, want columns %v", got, tt.want)
			}
		})
	}

	for _, invalid := range []string{
		`AFTER INSERT OF "a"`,     // OF is only valid for UPDATE
		`AFTER UPDATE OF "a",`,    // trailing comma
		`AFTER UPDATE OF "a" "b"`, // missing comma
		`AFTER UPDATE OF "a`,      // unterminated quote
	} {
		if got := SQLiteDefinition("audit", `CREATE TRIGGER "audit" `+invalid+tail); got != nil {
			t.Errorf("%q recognized as managed: %+v", invalid, got)
		}
	}
}
