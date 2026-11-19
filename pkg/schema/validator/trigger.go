package validator

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

func (v *validator) validateTriggers(ds *ast.DocumentSet) {
	provider := datasourceProvider(ds)
	databaseNames := make(map[string]bool)
	for _, doc := range ds.Documents {
		for _, model := range doc.Models {
			names := databaseNames
			if provider == "postgresql" {
				names = make(map[string]bool)
			}
			for _, attr := range model.Attributes {
				if attr.Name != "trigger" {
					continue
				}
				name := v.validateTrigger(attr, provider)
				if name == "" {
					continue
				}
				key := name
				if provider == "sqlite" {
					key = strings.Map(func(r rune) rune {
						if r >= 'A' && r <= 'Z' {
							return r + ('a' - 'A')
						}
						return r
					}, key)
				}
				if names[key] {
					v.addError(attr.Span.Start, fmt.Sprintf("duplicate trigger name %q", name))
				}
				names[key] = true
			}
		}
	}
}

func (v *validator) validateTrigger(attr ast.Attribute, provider string) string {
	seen := make(map[string]bool)
	var name string
	for _, arg := range attr.Args {
		pos := arg.Span.Start
		if seen[arg.Name] {
			v.addError(pos, fmt.Sprintf("@@trigger repeats argument %q", arg.Name))
			continue
		}
		seen[arg.Name] = true
		switch arg.Name {
		case "name":
			literal, ok := arg.Value.(ast.StringLiteral)
			if !ok || !validTriggerName(literal.Value, provider) {
				v.addError(pos, "@@trigger name must be a valid, non-empty database identifier string")
			} else {
				name = literal.Value
			}
		case "timing":
			id, ok := arg.Value.(ast.Identifier)
			if !ok || !keywordMatches(id.Name, "BEFORE", "AFTER") {
				v.addError(pos, "@@trigger timing must be Before or After")
			}
		case "event":
			id, ok := arg.Value.(ast.Identifier)
			if !ok || !keywordMatches(id.Name, "INSERT", "UPDATE", "DELETE") {
				v.addError(pos, "@@trigger event must be Insert, Update, or Delete")
			}
		case "body":
			literal, ok := arg.Value.(ast.StringLiteral)
			if !ok || strings.TrimSpace(literal.Value) == "" || strings.ContainsRune(literal.Value, 0) {
				v.addError(pos, "@@trigger body must be a non-empty SQL string without NUL bytes")
			}
		default:
			if arg.Name == "" {
				v.addError(pos, "@@trigger requires named arguments")
			} else {
				v.addError(pos, fmt.Sprintf("unknown @@trigger argument %q", arg.Name))
			}
		}
	}
	for _, required := range []string{"name", "timing", "event", "body"} {
		if !seen[required] {
			v.addError(attr.Span.Start, fmt.Sprintf("@@trigger requires the %q argument", required))
		}
	}
	return name
}

func validTriggerName(name, provider string) bool {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	switch provider {
	case "sqlite":
		return !strings.HasPrefix(strings.ToLower(name), "sqlite_")
	case "mysql":
		return utf8.RuneCountInString(name) <= 64 && !strings.ContainsFunc(name, func(r rune) bool { return r > '\uFFFF' })
	default:
		return len(name) <= 63
	}
}

func keywordMatches(value string, keywords ...string) bool {
	for _, keyword := range keywords {
		if strings.EqualFold(value, keyword) {
			return true
		}
	}
	return false
}
