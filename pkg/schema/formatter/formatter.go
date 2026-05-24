// Package formatter provides canonical formatting for GCORM schema ASTs.
package formatter

import (
	"sort"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// Format formats a Document AST into a canonical schema string.
// The output is deterministic and idempotent.
func Format(doc *ast.Document) string {
	if doc == nil {
		return ""
	}

	var blocks []string

	// Free-floating document comments.
	if len(doc.Comments) > 0 {
		var lines []string
		for _, c := range doc.Comments {
			lines = append(lines, commentLine(c))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}

	// Datasource blocks (sorted by name).
	datasources := make([]ast.DatasourceDecl, len(doc.Datasources))
	copy(datasources, doc.Datasources)
	sort.Slice(datasources, func(i, j int) bool { return datasources[i].Name < datasources[j].Name })
	for _, d := range datasources {
		blocks = append(blocks, formatDatasource(d))
	}

	// Generator blocks (sorted by name).
	generators := make([]ast.GeneratorDecl, len(doc.Generators))
	copy(generators, doc.Generators)
	sort.Slice(generators, func(i, j int) bool { return generators[i].Name < generators[j].Name })
	for _, g := range generators {
		blocks = append(blocks, formatGenerator(g))
	}

	// Enum blocks (sorted by name).
	enums := make([]ast.EnumDecl, len(doc.Enums))
	copy(enums, doc.Enums)
	sort.Slice(enums, func(i, j int) bool { return enums[i].Name < enums[j].Name })
	for _, e := range enums {
		blocks = append(blocks, formatEnum(e))
	}

	// Model blocks (sorted by name).
	models := make([]ast.ModelDecl, len(doc.Models))
	copy(models, doc.Models)
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	for _, m := range models {
		blocks = append(blocks, formatModel(m))
	}

	if len(blocks) == 0 {
		return ""
	}

	return strings.Join(blocks, "\n\n") + "\n"
}

// FormatExpression formats a single expression.
func FormatExpression(expr ast.Expression) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case ast.StringLiteral:
		return "\"" + e.Value + "\""
	case ast.NumberLiteral:
		return e.Value
	case ast.BooleanLiteral:
		if e.Value {
			return "true"
		}
		return "false"
	case ast.Identifier:
		return e.Name
	case ast.FunctionCall:
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = FormatExpression(a)
		}
		return e.Name + "(" + strings.Join(args, ", ") + ")"
	case ast.ArrayLiteral:
		elems := make([]string, len(e.Elements))
		for i, el := range e.Elements {
			elems[i] = FormatExpression(el)
		}
		return "[" + strings.Join(elems, ", ") + "]"
	default:
		return ""
	}
}

func formatDatasource(d ast.DatasourceDecl) string {
	return formatConfigBlock("datasource", d.Name, d.Entries, d.Comments)
}

func formatGenerator(g ast.GeneratorDecl) string {
	return formatConfigBlock("generator", g.Name, g.Entries, g.Comments)
}

func formatConfigBlock(keyword, name string, entries []ast.ConfigEntry, comments []ast.Comment) string {
	var b strings.Builder

	for _, c := range comments {
		b.WriteString(commentLine(c))
		b.WriteByte('\n')
	}

	b.WriteString(keyword)
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(" {\n")

	maxKeyLen := 0
	for _, e := range entries {
		if len(e.Key) > maxKeyLen {
			maxKeyLen = len(e.Key)
		}
	}

	for _, e := range entries {
		b.WriteString("  ")
		b.WriteString(padRight(e.Key, maxKeyLen))
		b.WriteString(" = ")
		b.WriteString(FormatExpression(e.Value))
		b.WriteByte('\n')
	}

	b.WriteByte('}')
	return b.String()
}

func formatModel(m ast.ModelDecl) string {
	var b strings.Builder

	for _, c := range m.Comments {
		b.WriteString(commentLine(c))
		b.WriteByte('\n')
	}

	b.WriteString("model ")
	b.WriteString(m.Name)
	b.WriteString(" {\n")

	maxNameLen := 0
	maxTypeLen := 0
	for _, f := range m.Fields {
		if len(f.Name) > maxNameLen {
			maxNameLen = len(f.Name)
		}
		if ts := f.Type.String(); len(ts) > maxTypeLen {
			maxTypeLen = len(ts)
		}
	}

	for _, f := range m.Fields {
		for _, c := range f.Comments {
			b.WriteString("  ")
			b.WriteString(commentLine(c))
			b.WriteByte('\n')
		}

		line := "  " + padRight(f.Name, maxNameLen) + " " + padRight(f.Type.String(), maxTypeLen)
		if len(f.Attributes) > 0 {
			line += " " + formatFieldAttributes(f.Attributes)
		}
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteByte('\n')
	}

	if len(m.Attributes) > 0 {
		if len(m.Fields) > 0 {
			b.WriteByte('\n')
		}
		for _, a := range m.Attributes {
			b.WriteString("  ")
			b.WriteString(formatAttribute(a))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('}')
	return b.String()
}

func formatEnum(e ast.EnumDecl) string {
	var b strings.Builder

	for _, c := range e.Comments {
		b.WriteString(commentLine(c))
		b.WriteByte('\n')
	}

	b.WriteString("enum ")
	b.WriteString(e.Name)
	b.WriteString(" {\n")

	for _, v := range e.Values {
		for _, c := range v.Comments {
			b.WriteString("  ")
			b.WriteString(commentLine(c))
			b.WriteByte('\n')
		}
		b.WriteString("  ")
		b.WriteString(v.Name)
		if len(v.Attributes) > 0 {
			b.WriteByte(' ')
			b.WriteString(formatFieldAttributes(v.Attributes))
		}
		b.WriteByte('\n')
	}

	if len(e.Attributes) > 0 {
		if len(e.Values) > 0 {
			b.WriteByte('\n')
		}
		for _, a := range e.Attributes {
			b.WriteString("  ")
			b.WriteString(formatAttribute(a))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('}')
	return b.String()
}

func formatAttribute(a ast.Attribute) string {
	prefix := "@"
	if a.IsModelLevel {
		prefix = "@@"
	}

	if len(a.Args) == 0 {
		return prefix + a.Name
	}

	args := make([]string, len(a.Args))
	for i, arg := range a.Args {
		if arg.Name != "" {
			args[i] = arg.Name + ": " + FormatExpression(arg.Value)
		} else {
			args[i] = FormatExpression(arg.Value)
		}
	}

	return prefix + a.Name + "(" + strings.Join(args, ", ") + ")"
}

func formatFieldAttributes(attrs []ast.Attribute) string {
	parts := make([]string, len(attrs))
	for i, a := range attrs {
		parts[i] = formatAttribute(a)
	}
	return strings.Join(parts, " ")
}

func commentLine(c ast.Comment) string {
	if c.IsDoc {
		return "/// " + c.Text
	}
	return "// " + c.Text
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
