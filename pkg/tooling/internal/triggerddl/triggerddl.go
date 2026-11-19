// Package triggerddl defines the catalog representations used to recognize
// triggers created by GCORM without taking ownership of native triggers.
package triggerddl

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

const (
	MySQLBegin  = "__gco_trigger: BEGIN"
	SQLiteBegin = "BEGIN /* gcorm:trigger */"
)

// PostgresFunctionName is stable across edits to a trigger definition and fits
// within PostgreSQL's 63-byte identifier limit.
func PostgresFunctionName(table, name string) string {
	hash := sha256.Sum256([]byte(table + "\x00" + name))
	return fmt.Sprintf("__gco_trg_%x", hash[:16])
}

// MySQLBody recovers the SQL inside GCORM's labeled compound statement.
func MySQLBody(action string) (string, bool) {
	action = strings.TrimSpace(action)
	body, ok := strings.CutPrefix(action, MySQLBegin)
	if !ok {
		return "", false
	}
	body, ok = strings.CutSuffix(body, "END")
	return strings.TrimSpace(body), ok
}

// SQLite stores the original CREATE TRIGGER SQL, with its leading keywords
// normalized. ALTER TABLE may add or change identifier quoting.
var sqliteTrigger = regexp.MustCompile(`(?s)^CREATE TRIGGER "(?:[^"]|"")*" (BEFORE|AFTER) (INSERT|UPDATE|DELETE) ON "(?:[^"]|"")*" FOR EACH ROW\s+BEGIN /\* gcorm:trigger \*/\s+(.*)\s+END;?$`)

// SQLiteDefinition recognizes a GCORM CREATE TRIGGER statement from sqlite_schema.
func SQLiteDefinition(name, sql string) *ir.Trigger {
	match := sqliteTrigger.FindStringSubmatch(strings.TrimSpace(sql))
	if match == nil {
		return nil
	}
	return &ir.Trigger{Name: name, Timing: match[1], Event: match[2], Body: strings.TrimSpace(match[3])}
}
