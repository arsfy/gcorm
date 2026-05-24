package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// ParseError represents a parsing error with source location.
type ParseError struct {
	Message string
	Pos     ast.Position
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.Pos.File, e.Pos.Line, e.Pos.Column, e.Message)
}

// Parse parses a single schema file into a Document AST.
func Parse(filename string, src []byte) (*ast.Document, error) {
	lex := NewLexer(filename, src)
	tokens, lexErr := lex.Tokenize()
	if lexErr != nil {
		return nil, lexErr
	}
	p := &parser{
		tokens:   tokens,
		filename: filename,
	}
	doc := p.parseDocument()
	if len(p.errors) > 0 {
		return doc, p.errors[0]
	}
	return doc, nil
}

// ParseMulti parses multiple schema files into a DocumentSet.
func ParseMulti(files map[string][]byte) (*ast.DocumentSet, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	ds := &ast.DocumentSet{}
	var firstErr error
	for _, name := range names {
		doc, err := Parse(name, files[name])
		if err != nil && firstErr == nil {
			firstErr = err
		}
		ds.Documents = append(ds.Documents, doc)
		ds.Files = append(ds.Files, name)
	}
	return ds, firstErr
}

// ---------------------------------------------------------------------------
// Internal parser
// ---------------------------------------------------------------------------

type parser struct {
	tokens   []Token
	pos      int
	errors   []*ParseError
	filename string
	pending  []ast.Comment
}

func (p *parser) current() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokenEOF, Pos: ast.Position{File: p.filename}}
	}
	return p.tokens[p.pos]
}

func (p *parser) peek() TokenType { return p.current().Type }

func (p *parser) advance() Token {
	tok := p.current()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

// skipTrivia skips whitespace, newlines, and comments. Comments are collected
// into the pending slice so they can be attached to the next AST node.
func (p *parser) skipTrivia() {
	for {
		switch p.peek() {
		case TokenWhitespace, TokenNewline:
			p.advance()
		case TokenLineComment, TokenDocComment:
			p.pending = append(p.pending, p.makeComment(p.current()))
			p.advance()
		default:
			return
		}
	}
}

func (p *parser) makeComment(tok Token) ast.Comment {
	text := tok.Value
	isDoc := tok.Type == TokenDocComment
	if isDoc {
		text = strings.TrimPrefix(text, "///")
	} else {
		text = strings.TrimPrefix(text, "//")
	}
	if len(text) > 0 && text[0] == ' ' {
		text = text[1:]
	}
	return ast.Comment{
		Text:  text,
		IsDoc: isDoc,
		Span:  tokenSpan(tok),
	}
}

func (p *parser) takePending() []ast.Comment {
	c := p.pending
	p.pending = nil
	return c
}

func (p *parser) addError(pos ast.Position, msg string) {
	p.errors = append(p.errors, &ParseError{Message: msg, Pos: pos})
}

func (p *parser) expect(typ TokenType) (Token, bool) {
	p.skipTrivia()
	tok := p.current()
	if tok.Type != typ {
		p.addError(tok.Pos, fmt.Sprintf("expected %s, got %s", typ, tok.Type))
		return tok, false
	}
	p.advance()
	return tok, true
}

// peekNextMeaningful returns the type of the next non-trivia token after the
// current position, without advancing.
func (p *parser) peekNextMeaningful() TokenType {
	for i := p.pos + 1; i < len(p.tokens); i++ {
		switch p.tokens[i].Type {
		case TokenWhitespace, TokenNewline, TokenLineComment, TokenDocComment:
			continue
		default:
			return p.tokens[i].Type
		}
	}
	return TokenEOF
}

// ---------------------------------------------------------------------------
// Span helpers
// ---------------------------------------------------------------------------

func tokenSpan(tok Token) ast.Span {
	end := tok.Pos
	end.Column += len(tok.Value)
	end.Offset += len(tok.Value)
	return ast.Span{Start: tok.Pos, End: end}
}

func spanFromTo(a, b Token) ast.Span {
	end := b.Pos
	end.Column += len(b.Value)
	end.Offset += len(b.Value)
	return ast.Span{Start: a.Pos, End: end}
}

// ---------------------------------------------------------------------------
// Document
// ---------------------------------------------------------------------------

func (p *parser) parseDocument() *ast.Document {
	doc := &ast.Document{}
	for {
		p.skipTrivia()
		if p.peek() == TokenEOF {
			break
		}
		comments := p.takePending()
		tok := p.current()
		if tok.Type != TokenIdent {
			p.addError(tok.Pos, fmt.Sprintf("unexpected %s; expected top-level declaration", tok.Type))
			doc.Comments = append(doc.Comments, comments...)
			p.advance()
			continue
		}
		switch tok.Value {
		case "datasource":
			decl := p.parseDatasource()
			decl.Comments = append(comments, decl.Comments...)
			doc.Datasources = append(doc.Datasources, decl)
		case "generator":
			decl := p.parseGenerator()
			decl.Comments = append(comments, decl.Comments...)
			doc.Generators = append(doc.Generators, decl)
		case "model":
			decl := p.parseModel()
			decl.Comments = append(comments, decl.Comments...)
			doc.Models = append(doc.Models, decl)
		case "enum":
			decl := p.parseEnum()
			decl.Comments = append(comments, decl.Comments...)
			doc.Enums = append(doc.Enums, decl)
		default:
			p.addError(tok.Pos, fmt.Sprintf("unknown top-level keyword %q", tok.Value))
			doc.Comments = append(doc.Comments, comments...)
			p.advance()
		}
	}
	doc.Comments = append(doc.Comments, p.takePending()...)
	return doc
}

// ---------------------------------------------------------------------------
// Datasource & Generator
// ---------------------------------------------------------------------------

func (p *parser) parseDatasource() ast.DatasourceDecl {
	startTok := p.advance() // "datasource"
	nameTok, _ := p.expect(TokenIdent)
	p.expect(TokenLBrace)
	entries, comments := p.parseConfigBlock()
	rbrace, _ := p.expect(TokenRBrace)
	return ast.DatasourceDecl{
		Name:     nameTok.Value,
		Entries:  entries,
		Comments: comments,
		Span:     spanFromTo(startTok, rbrace),
	}
}

func (p *parser) parseGenerator() ast.GeneratorDecl {
	startTok := p.advance() // "generator"
	nameTok, _ := p.expect(TokenIdent)
	p.expect(TokenLBrace)
	entries, comments := p.parseConfigBlock()
	rbrace, _ := p.expect(TokenRBrace)
	return ast.GeneratorDecl{
		Name:     nameTok.Value,
		Entries:  entries,
		Comments: comments,
		Span:     spanFromTo(startTok, rbrace),
	}
}

func (p *parser) parseConfigBlock() ([]ast.ConfigEntry, []ast.Comment) {
	var entries []ast.ConfigEntry
	var comments []ast.Comment
	for {
		p.skipTrivia()
		comments = append(comments, p.takePending()...)
		if p.peek() == TokenRBrace || p.peek() == TokenEOF {
			break
		}
		if p.peek() != TokenIdent {
			p.addError(p.current().Pos, fmt.Sprintf("expected identifier in config block, got %s", p.peek()))
			p.advance()
			continue
		}
		entries = append(entries, p.parseConfigEntry())
	}
	return entries, comments
}

func (p *parser) parseConfigEntry() ast.ConfigEntry {
	keyTok := p.advance()
	p.expect(TokenEquals)
	p.skipTrivia()
	value := p.parseExpression()
	return ast.ConfigEntry{
		Key:   keyTok.Value,
		Value: value,
		Span:  ast.Span{Start: keyTok.Pos, End: value.ExprSpan().End},
	}
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

func (p *parser) parseModel() ast.ModelDecl {
	startTok := p.advance() // "model"
	nameTok, _ := p.expect(TokenIdent)
	p.expect(TokenLBrace)

	var fields []ast.FieldDecl
	var attrs []ast.Attribute
	var comments []ast.Comment

	for {
		p.skipTrivia()
		if p.peek() == TokenRBrace || p.peek() == TokenEOF {
			comments = append(comments, p.takePending()...)
			break
		}
		pre := p.takePending()
		switch p.peek() {
		case TokenAtAt:
			comments = append(comments, pre...)
			attrs = append(attrs, p.parseAttribute(true))
		case TokenIdent:
			field := p.parseField()
			field.Comments = pre
			fields = append(fields, field)
		default:
			p.addError(p.current().Pos, fmt.Sprintf("unexpected %s in model body", p.peek()))
			comments = append(comments, pre...)
			p.advance()
		}
	}

	rbrace, _ := p.expect(TokenRBrace)
	return ast.ModelDecl{
		Name:       nameTok.Value,
		Fields:     fields,
		Attributes: attrs,
		Comments:   comments,
		Span:       spanFromTo(startTok, rbrace),
	}
}

func (p *parser) parseField() ast.FieldDecl {
	nameTok := p.advance()
	p.skipTrivia()
	ft := p.parseFieldType()

	var attrs []ast.Attribute
	for {
		p.skipTrivia()
		if p.peek() != TokenAt {
			break
		}
		attrs = append(attrs, p.parseAttribute(false))
	}

	span := ast.Span{Start: nameTok.Pos, End: ft.Span.End}
	if len(attrs) > 0 {
		span.End = attrs[len(attrs)-1].Span.End
	}
	return ast.FieldDecl{
		Name:       nameTok.Value,
		Type:       ft,
		Attributes: attrs,
		Span:       span,
	}
}

func (p *parser) parseFieldType() ast.FieldType {
	nameTok := p.advance()
	ft := ast.FieldType{Name: nameTok.Value, Span: tokenSpan(nameTok)}
	// Modifiers must be immediately adjacent (no trivia skipping).
	switch p.peek() {
	case TokenQuestion:
		q := p.advance()
		ft.IsOptional = true
		ft.Span.End = tokenSpan(q).End
	case TokenLBracket:
		p.advance() // [
		if p.peek() == TokenRBracket {
			rb := p.advance()
			ft.IsList = true
			ft.Span.End = tokenSpan(rb).End
		} else {
			p.addError(p.current().Pos, "expected ] after [ in type modifier")
		}
	}
	return ft
}

// ---------------------------------------------------------------------------
// Enum
// ---------------------------------------------------------------------------

func (p *parser) parseEnum() ast.EnumDecl {
	startTok := p.advance() // "enum"
	nameTok, _ := p.expect(TokenIdent)
	p.expect(TokenLBrace)

	var values []ast.EnumValue
	var attrs []ast.Attribute
	var comments []ast.Comment

	for {
		p.skipTrivia()
		if p.peek() == TokenRBrace || p.peek() == TokenEOF {
			comments = append(comments, p.takePending()...)
			break
		}
		pre := p.takePending()
		switch p.peek() {
		case TokenAtAt:
			comments = append(comments, pre...)
			attrs = append(attrs, p.parseAttribute(true))
		case TokenIdent:
			val := p.parseEnumValue()
			val.Comments = pre
			values = append(values, val)
		default:
			p.addError(p.current().Pos, fmt.Sprintf("unexpected %s in enum body", p.peek()))
			comments = append(comments, pre...)
			p.advance()
		}
	}

	rbrace, _ := p.expect(TokenRBrace)
	return ast.EnumDecl{
		Name:       nameTok.Value,
		Values:     values,
		Attributes: attrs,
		Comments:   comments,
		Span:       spanFromTo(startTok, rbrace),
	}
}

func (p *parser) parseEnumValue() ast.EnumValue {
	nameTok := p.advance()
	var attrs []ast.Attribute
	for {
		p.skipTrivia()
		if p.peek() != TokenAt {
			break
		}
		attrs = append(attrs, p.parseAttribute(false))
	}
	span := tokenSpan(nameTok)
	if len(attrs) > 0 {
		span.End = attrs[len(attrs)-1].Span.End
	}
	return ast.EnumValue{
		Name:       nameTok.Value,
		Attributes: attrs,
		Span:       span,
	}
}

// ---------------------------------------------------------------------------
// Attributes
// ---------------------------------------------------------------------------

func (p *parser) parseAttribute(isModelLevel bool) ast.Attribute {
	startTok := p.advance() // @ or @@

	name := ""
	var lastTok = startTok

	// Attribute name follows immediately (no trivia skip).
	if p.peek() == TokenIdent {
		lastTok = p.advance()
		name = lastTok.Value
		// Dotted names like db.VarChar.
		for p.peek() == TokenDot {
			p.advance() // .
			if p.peek() == TokenIdent {
				lastTok = p.advance()
				name += "." + lastTok.Value
			}
		}
	} else {
		p.addError(p.current().Pos, "expected attribute name after @")
	}

	var args []ast.AttributeArg

	// Parenthesized arguments follow immediately (no trivia skip).
	if p.peek() == TokenLParen {
		p.advance() // (
		args = p.parseAttributeArgs()
		if rparen, ok := p.expect(TokenRParen); ok {
			lastTok = rparen
		}
	}

	return ast.Attribute{
		Name:         name,
		IsModelLevel: isModelLevel,
		Args:         args,
		Span:         spanFromTo(startTok, lastTok),
	}
}

func (p *parser) parseAttributeArgs() []ast.AttributeArg {
	var args []ast.AttributeArg
	for {
		p.skipTrivia()
		if p.peek() == TokenRParen || p.peek() == TokenEOF {
			break
		}
		args = append(args, p.parseAttributeArg())
		p.skipTrivia()
		if p.peek() == TokenComma {
			p.advance()
		}
	}
	return args
}

func (p *parser) parseAttributeArg() ast.AttributeArg {
	p.skipTrivia()
	startPos := p.current().Pos

	// Named argument: ident : expr
	if p.peek() == TokenIdent && p.peekNextMeaningful() == TokenColon {
		nameTok := p.advance()
		p.expect(TokenColon)
		p.skipTrivia()
		value := p.parseExpression()
		return ast.AttributeArg{
			Name:  nameTok.Value,
			Value: value,
			Span:  ast.Span{Start: startPos, End: value.ExprSpan().End},
		}
	}

	// Positional argument.
	value := p.parseExpression()
	return ast.AttributeArg{
		Value: value,
		Span:  value.ExprSpan(),
	}
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

func (p *parser) parseExpression() ast.Expression {
	p.skipTrivia()
	tok := p.current()
	switch tok.Type {
	case TokenString:
		p.advance()
		return ast.StringLiteral{Value: tok.Value, Span: tokenSpan(tok)}
	case TokenNumber:
		p.advance()
		return ast.NumberLiteral{
			Value:   tok.Value,
			IsFloat: strings.Contains(tok.Value, "."),
			Span:    tokenSpan(tok),
		}
	case TokenTrue:
		p.advance()
		return ast.BooleanLiteral{Value: true, Span: tokenSpan(tok)}
	case TokenFalse:
		p.advance()
		return ast.BooleanLiteral{Value: false, Span: tokenSpan(tok)}
	case TokenIdent:
		return p.parseIdentOrFuncCall()
	case TokenLBracket:
		return p.parseArrayLiteral()
	default:
		p.addError(tok.Pos, fmt.Sprintf("unexpected %s in expression", tok.Type))
		p.advance()
		return ast.Identifier{Name: "", Span: tokenSpan(tok)}
	}
}

func (p *parser) parseIdentOrFuncCall() ast.Expression {
	nameTok := p.advance()
	name := nameTok.Value
	lastTok := nameTok

	// Dotted identifier (e.g. db.uuid).
	for p.peek() == TokenDot {
		p.advance()
		if p.peek() == TokenIdent {
			lastTok = p.advance()
			name += "." + lastTok.Value
		}
	}

	// Allow horizontal whitespace before ( for function calls.
	for p.peek() == TokenWhitespace {
		p.advance()
	}

	if p.peek() == TokenLParen {
		p.advance() // (
		var args []ast.Expression
		for {
			p.skipTrivia()
			if p.peek() == TokenRParen || p.peek() == TokenEOF {
				break
			}
			args = append(args, p.parseExpression())
			p.skipTrivia()
			if p.peek() == TokenComma {
				p.advance()
			}
		}
		rparen, _ := p.expect(TokenRParen)
		return ast.FunctionCall{
			Name: name,
			Args: args,
			Span: spanFromTo(nameTok, rparen),
		}
	}

	parts := strings.Split(name, ".")
	return ast.Identifier{
		Name:  name,
		Parts: parts,
		Span:  spanFromTo(nameTok, lastTok),
	}
}

func (p *parser) parseArrayLiteral() ast.Expression {
	lbracket := p.advance() // [
	var elements []ast.Expression
	for {
		p.skipTrivia()
		if p.peek() == TokenRBracket || p.peek() == TokenEOF {
			break
		}
		elements = append(elements, p.parseExpression())
		p.skipTrivia()
		if p.peek() == TokenComma {
			p.advance()
		}
	}
	rbracket, _ := p.expect(TokenRBracket)
	return ast.ArrayLiteral{
		Elements: elements,
		Span:     spanFromTo(lbracket, rbracket),
	}
}
