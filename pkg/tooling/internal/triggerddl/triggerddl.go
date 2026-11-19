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
// normalized. ALTER TABLE may add or change identifier quoting. The optional
// group between the event and ON holds an UPDATE OF column list.
var sqliteTrigger = regexp.MustCompile(`(?s)^CREATE TRIGGER "(?:[^"]|"")*" (BEFORE|AFTER) (INSERT|UPDATE|DELETE)((?:\s+OF\s+.*?)?) ON "(?:[^"]|"")*" FOR EACH ROW\s+BEGIN /\* gcorm:trigger \*/\s+(.*)\s+END;?$`)

// SQLiteDefinition recognizes a GCORM CREATE TRIGGER statement from sqlite_schema.
func SQLiteDefinition(name, sql string) *ir.Trigger {
	match := sqliteTrigger.FindStringSubmatch(strings.TrimSpace(sql))
	if match == nil {
		return nil
	}
	trigger := &ir.Trigger{Name: name, Timing: match[1], Event: match[2], Body: strings.TrimSpace(match[4])}
	if of := strings.TrimSpace(match[3]); of != "" {
		columns, ok := sqliteColumnList(strings.TrimSpace(of[len("OF"):]))
		if !ok || trigger.Event != "UPDATE" {
			return nil
		}
		trigger.Columns = columns
	}
	return trigger
}

// sqliteColumnList parses a comma-separated identifier list. Identifiers may
// be bare or quoted with "", “, or [] (as ALTER TABLE can rewrite them).
func sqliteColumnList(list string) ([]string, bool) {
	var columns []string
	for i := 0; i < len(list); {
		var name string
		switch list[i] {
		case '"', '`':
			quote := list[i]
			var b strings.Builder
			j := i + 1
			for ; j < len(list); j++ {
				if list[j] == quote {
					if j+1 < len(list) && list[j+1] == quote {
						b.WriteByte(quote)
						j++
						continue
					}
					break
				}
				b.WriteByte(list[j])
			}
			if j >= len(list) {
				return nil, false
			}
			name, i = b.String(), j+1
		case '[':
			end := strings.IndexByte(list[i:], ']')
			if end < 0 {
				return nil, false
			}
			name, i = list[i+1:i+end], i+end+1
		default:
			j := i
			for j < len(list) && list[j] != ',' && list[j] != ' ' && list[j] != '\t' && list[j] != '\n' {
				j++
			}
			name, i = list[i:j], j
		}
		if name == "" {
			return nil, false
		}
		columns = append(columns, name)
		for i < len(list) && (list[i] == ' ' || list[i] == '\t' || list[i] == '\n') {
			i++
		}
		if i == len(list) {
			break
		}
		if list[i] != ',' {
			return nil, false
		}
		i++
		for i < len(list) && (list[i] == ' ' || list[i] == '\t' || list[i] == '\n') {
			i++
		}
		if i == len(list) {
			return nil, false
		}
	}
	return columns, len(columns) > 0
}
