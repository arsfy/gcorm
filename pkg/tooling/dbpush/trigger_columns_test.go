package dbpush

import (
	"fmt"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/formatter"
)

// triggerColumnsSchema declares an AFTER UPDATE OF trigger on Item. columns
// is the raw @@trigger columns list, e.g. "[priceCents]"; empty omits it.
func triggerColumnsSchema(provider, schema, columns, body string) string {
	var extra, columnsArg string
	if schema != "" {
		extra = fmt.Sprintf("  schema = %q\n", schema)
	}
	if columns != "" {
		columnsArg = ", columns: " + columns
	}
	return fmt.Sprintf(`datasource db {
  provider = %q
  url      = "unused"
%s}
model Item {
  id         Int     @id
  note       String?
  priceCents Int?    @map("price_cents")
  @@map("items")
  @@trigger(name: "item_price_audit", timing: After, event: Update%s, body: %s)
}
model AuditLog {
  id      Int    @id
  message String
  @@map("audit_log")
}
`, provider, extra, columnsArg, formatter.FormatExpression(ast.StringLiteral{Value: body}))
}

func TestPushTriggerUpdateOfColumns(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgresql"} {
		t.Run(provider, func(t *testing.T) {
			db, dsn, schema := openTriggerTestDatabase(t, provider)
			table, logTable := "items", "audit_log"
			if provider == "postgresql" {
				table, logTable = schema+"."+table, schema+"."+logTable
			}
			body := "INSERT INTO audit_log (id, message) VALUES (NEW.id, 'price');"
			if provider == "postgresql" {
				body = "BEGIN\n" + body + "\nRETURN NEW;\nEND;"
			}

			content := triggerColumnsSchema(provider, schema, "[priceCents]", body)
			plan, err := Push(t.Context(), nil, Options{SchemaFS: triggerFS(content), DryRun: true, SkipIntrospection: true})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(plan.Statements, "\n"), `AFTER UPDATE OF "price_cents" ON`) {
				t.Fatalf("plan does not use the mapped column:\n%s", strings.Join(plan.Statements, "\n"))
			}

			pushTriggerSchema(t, db, dsn, content, false)
			requireTriggerNoop(t, db, dsn, content)

			// Only updates of the listed column fire the trigger.
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (1)")
			execTriggerSQL(t, db, "UPDATE "+table+" SET note = 'x' WHERE id = 1")
			requireAuditMessage(t, db, logTable, 1, "")
			execTriggerSQL(t, db, "UPDATE "+table+" SET price_cents = 100 WHERE id = 1")
			requireAuditMessage(t, db, logTable, 1, "price")

			// Changing the column set replaces the trigger; order is irrelevant.
			wider := triggerColumnsSchema(provider, schema, "[priceCents, note]", body)
			if _, err := Push(t.Context(), db, Options{SchemaFS: triggerFS(wider), DatabaseURL: dsn}); err == nil || !strings.Contains(err.Error(), "DropTrigger") {
				t.Fatalf("column change without force error = %v", err)
			}
			pushTriggerSchema(t, db, dsn, wider, true)
			requireTriggerNoop(t, db, dsn, wider)
			requireTriggerNoop(t, db, dsn, triggerColumnsSchema(provider, schema, "[note, priceCents]", body))
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (2)")
			execTriggerSQL(t, db, "UPDATE "+table+" SET note = 'y' WHERE id = 2")
			requireAuditMessage(t, db, logTable, 2, "price")

			// Dropping the column list is detected as well.
			anyColumn := triggerColumnsSchema(provider, schema, "", body)
			result := pushTriggerSchema(t, db, dsn, anyColumn, true)
			if result.Noop {
				t.Fatal("removing the column list was not detected")
			}
			requireTriggerNoop(t, db, dsn, anyColumn)
		})
	}
}
