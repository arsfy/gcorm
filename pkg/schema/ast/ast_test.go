package ast

import "testing"

// ---------------------------------------------------------------------------
// FieldType.String()
// ---------------------------------------------------------------------------

func TestFieldTypeString_Plain(t *testing.T) {
	ft := FieldType{Name: "String"}
	if got := ft.String(); got != "String" {
		t.Errorf("FieldType.String() = %q, want %q", got, "String")
	}
}

func TestFieldTypeString_Optional(t *testing.T) {
	ft := FieldType{Name: "String", IsOptional: true}
	if got := ft.String(); got != "String?" {
		t.Errorf("FieldType.String() = %q, want %q", got, "String?")
	}
}

func TestFieldTypeString_List(t *testing.T) {
	ft := FieldType{Name: "Post", IsList: true}
	if got := ft.String(); got != "Post[]" {
		t.Errorf("FieldType.String() = %q, want %q", got, "Post[]")
	}
}

// ---------------------------------------------------------------------------
// Expression interface compliance
// ---------------------------------------------------------------------------

func TestStringLiteralImplementsExpression(t *testing.T) {
	var _ Expression = StringLiteral{Value: "hello"}
}

func TestNumberLiteralImplementsExpression(t *testing.T) {
	var _ Expression = NumberLiteral{Value: "42"}
}

func TestBooleanLiteralImplementsExpression(t *testing.T) {
	var _ Expression = BooleanLiteral{Value: true}
}

func TestIdentifierImplementsExpression(t *testing.T) {
	var _ Expression = Identifier{Name: "id", Parts: []string{"id"}}
}

func TestFunctionCallImplementsExpression(t *testing.T) {
	var _ Expression = FunctionCall{Name: "env", Args: []Expression{StringLiteral{Value: "DB"}}}
}

func TestArrayLiteralImplementsExpression(t *testing.T) {
	var _ Expression = ArrayLiteral{Elements: []Expression{NumberLiteral{Value: "1"}}}
}

// ---------------------------------------------------------------------------
// ExprSpan returns the stored Span
// ---------------------------------------------------------------------------

func TestExprSpanReturnsCorrectSpan(t *testing.T) {
	span := Span{
		Start: Position{File: "schema.gcorm", Line: 5, Column: 3, Offset: 42},
		End:   Position{File: "schema.gcorm", Line: 5, Column: 10, Offset: 49},
	}

	cases := []struct {
		name string
		expr Expression
	}{
		{"StringLiteral", StringLiteral{Value: "x", Span: span}},
		{"NumberLiteral", NumberLiteral{Value: "1", Span: span}},
		{"BooleanLiteral", BooleanLiteral{Value: false, Span: span}},
		{"Identifier", Identifier{Name: "a", Parts: []string{"a"}, Span: span}},
		{"FunctionCall", FunctionCall{Name: "fn", Span: span}},
		{"ArrayLiteral", ArrayLiteral{Span: span}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.expr.ExprSpan()
			if got != span {
				t.Errorf("ExprSpan() = %+v, want %+v", got, span)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Position and Span zero values
// ---------------------------------------------------------------------------

func TestPositionZeroValue(t *testing.T) {
	var p Position
	if p.File != "" {
		t.Errorf("zero Position.File = %q, want empty", p.File)
	}
	if p.Line != 0 {
		t.Errorf("zero Position.Line = %d, want 0", p.Line)
	}
	if p.Column != 0 {
		t.Errorf("zero Position.Column = %d, want 0", p.Column)
	}
	if p.Offset != 0 {
		t.Errorf("zero Position.Offset = %d, want 0", p.Offset)
	}
}

func TestSpanZeroValue(t *testing.T) {
	var s Span
	if s.Start != (Position{}) {
		t.Errorf("zero Span.Start = %+v, want zero Position", s.Start)
	}
	if s.End != (Position{}) {
		t.Errorf("zero Span.End = %+v, want zero Position", s.End)
	}
}
