package dbpush

import (
	"strings"
	"testing"
)

// Each index below is stored by PostgreSQL in a form that differs textually
// from the schema declaration.
const postgresDeparsedIndexSchema = `
enum Role {
  USER
  ADMIN @map("admin")
}

model Account {
  id        BigInt    @id @default(autoincrement())
  email     String    @db.VarChar(255)
  code      String    @default("")
  name      String    @default("")
  role      Role      @default(USER)
  score     Float     @default(0)
  neg       Int       @default(0)
  sid       String    @default("")
  ended     DateTime?
  createdAt DateTime  @default(now()) @map("created_at")
  meta      Json      @db.JsonB @default("{\"a\":1,\"b\":[1, 2]}")
  serverID  UUID      @map("server_id")

  @@unique([email, id], name: "uq_open", expressions: ["", "(CASE WHEN ended IS NULL THEN 0 ELSE id END)"])
  @@index([role], name: "idx_in", where: "role IN ('admin', 'USER')")
  @@index([email], name: "idx_varchar_in", where: "email in ('a','b')")
  @@index([neg], name: "idx_not_in", where: "neg NOT IN (1,2)")
  @@index([serverID], name: "idx_uuid", where: "server_id = '00000000-0000-0000-0000-000000000000'")
  @@index([score], name: "idx_between", where: "score BETWEEN 1 AND 10")
  @@index([score], name: "idx_float", where: "score > 1.5")
  @@index([neg], name: "idx_neg", where: "neg > -1 AND neg != 7")
  @@index([email], name: "idx_like", where: "email LIKE '%@x.com' OR email ilike 'a%'")
  @@index([sid], name: "idx_cast", where: "sid::int > 0")
  @@index([createdAt], name: "idx_ts", where: "created_at > '2020-01-01'")
  @@index([neg], name: "idx_coalesce", where: "coalesce(neg, 0) > 0")
  @@index([name], name: "idx_concat", expression: "(name || '-' || code)")
  @@index([meta], name: "idx_json", expression: "(meta -> 'a' ->> 'b')")
  @@index([name], name: "idx_collate", collate: ["C"])
  @@map("accounts")
}
`

func TestPostgresPushDoesNotRebuildDeparsedIndexes(t *testing.T) {
	p := openPostgresPushDB(t)
	p.push(postgresDeparsedIndexSchema, false)
	p.requireNoop(postgresDeparsedIndexSchema)
}

func TestPostgresPushStillRebuildsChangedIndexes(t *testing.T) {
	p := openPostgresPushDB(t)
	p.push(postgresDeparsedIndexSchema, false)
	p.exec(`INSERT INTO accounts (email, server_id, sid) VALUES ('a@x.com', gen_random_uuid(), '1')`)

	changed := strings.NewReplacer(
		`score BETWEEN 1 AND 10`, `score BETWEEN 1 AND 11`,
		`role IN ('admin', 'USER')`, `role IN ('admin')`,
		`collate: ["C"]`, `collate: ["POSIX"]`,
		`"(meta -> 'a' ->> 'b')"`, `"(meta -> 'a' ->> 'c')"`,
	).Replace(postgresDeparsedIndexSchema)

	result := p.push(changed, true)
	var drops int
	for _, stmt := range result.AppliedStatements {
		if strings.HasPrefix(stmt, "DROP INDEX") {
			drops++
		}
	}
	if drops != 4 {
		t.Fatalf("dropped %d index(es), want 4:\n%s", drops, strings.Join(result.AppliedStatements, "\n"))
	}
	p.check("true", `SELECT pg_get_expr(indpred, indrelid) LIKE '%(11)::double precision%' FROM pg_index WHERE indexrelid = 'idx_between'::regclass`)
	p.check("0", `SELECT count(*) FROM pg_class WHERE relname LIKE 'gco_push_index_probe%'`)
	p.requireNoop(changed)
}

// A column type change makes PostgreSQL re-parse dependent indexes, so the
// probe must not hide their rebuild.
func TestPostgresPushSkipsIndexProbeForAlteredTables(t *testing.T) {
	p := openPostgresPushDB(t)
	schema := "model Item {\n  id Int @id\n  qty Int\n  @@index([qty], name: \"idx_qty\", where: \"qty > -1\")\n}\n"
	p.push(schema, false)
	altered := strings.Replace(schema, "qty Int", "qty BigInt", 1)
	result := p.push(altered, true)
	if !strings.Contains(strings.Join(result.AppliedStatements, "\n"), `DROP INDEX`) {
		t.Fatalf("expected idx_qty to be rebuilt with the type change:\n%s", strings.Join(result.AppliedStatements, "\n"))
	}
	p.requireNoop(altered)
}
