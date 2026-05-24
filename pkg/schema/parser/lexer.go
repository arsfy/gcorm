package parser

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// Lexer tokenizes GCO schema source code.
type Lexer struct {
	filename string
	src      []byte
	offset   int // current byte offset
	line     int // 1-based current line
	column   int // 1-based current column
	errors   []string
}

// NewLexer creates a new lexer for the given source.
func NewLexer(filename string, src []byte) *Lexer {
	return &Lexer{
		filename: filename,
		src:      src,
		offset:   0,
		line:     1,
		column:   1,
	}
}

// pos returns the current source position.
func (l *Lexer) pos() ast.Position {
	return ast.Position{
		File:   l.filename,
		Offset: l.offset,
		Line:   l.line,
		Column: l.column,
	}
}

// peek returns the current rune without advancing.
func (l *Lexer) peek() (rune, int) {
	if l.offset >= len(l.src) {
		return 0, 0
	}
	return utf8.DecodeRune(l.src[l.offset:])
}

// advance moves forward by one rune and updates position tracking.
func (l *Lexer) advance() rune {
	if l.offset >= len(l.src) {
		return 0
	}
	r, size := utf8.DecodeRune(l.src[l.offset:])
	l.offset += size
	if r == '\n' {
		l.line++
		l.column = 1
	} else {
		l.column++
	}
	return r
}

// NextToken returns the next token from the source.
func (l *Lexer) NextToken() Token {
	if l.offset >= len(l.src) {
		return Token{Type: TokenEOF, Value: "", Pos: l.pos()}
	}

	r, _ := l.peek()

	// Newlines
	if r == '\n' {
		pos := l.pos()
		l.advance()
		return Token{Type: TokenNewline, Value: "\n", Pos: pos}
	}

	// Whitespace (excluding newlines)
	if r == ' ' || r == '\t' || r == '\r' {
		return l.readWhitespace()
	}

	// Comments: // and ///
	if r == '/' && l.offset+1 < len(l.src) && l.src[l.offset+1] == '/' {
		return l.readComment()
	}

	// String literals
	if r == '"' {
		return l.readString()
	}

	// Numbers
	if r >= '0' && r <= '9' {
		return l.readNumber()
	}

	// Identifiers and keywords (true/false)
	if r == '_' || unicode.IsLetter(r) {
		return l.readIdent()
	}

	// Symbols
	return l.readSymbol()
}

// readWhitespace consumes horizontal whitespace (spaces, tabs, carriage returns).
func (l *Lexer) readWhitespace() Token {
	pos := l.pos()
	start := l.offset
	for l.offset < len(l.src) {
		r, _ := l.peek()
		if r != ' ' && r != '\t' && r != '\r' {
			break
		}
		l.advance()
	}
	return Token{Type: TokenWhitespace, Value: string(l.src[start:l.offset]), Pos: pos}
}

// readComment consumes a // or /// comment through end of line.
func (l *Lexer) readComment() Token {
	pos := l.pos()
	start := l.offset

	// Skip the first two slashes
	l.advance() // /
	l.advance() // /

	// Check for doc comment (///)
	tokenType := TokenLineComment
	if l.offset < len(l.src) && l.src[l.offset] == '/' {
		l.advance() // third /
		tokenType = TokenDocComment
	}

	// Consume until end of line or EOF
	for l.offset < len(l.src) {
		r, _ := l.peek()
		if r == '\n' {
			break
		}
		l.advance()
	}

	return Token{Type: tokenType, Value: string(l.src[start:l.offset]), Pos: pos}
}

// readString consumes a double-quoted string literal with escape sequences.
func (l *Lexer) readString() Token {
	pos := l.pos()
	l.advance() // opening quote

	var buf strings.Builder
	for {
		if l.offset >= len(l.src) {
			l.addError(pos, "unterminated string literal")
			return Token{Type: TokenString, Value: buf.String(), Pos: pos}
		}
		r, _ := l.peek()
		if r == '\n' {
			l.addError(pos, "unterminated string literal")
			return Token{Type: TokenString, Value: buf.String(), Pos: pos}
		}
		if r == '"' {
			l.advance() // closing quote
			return Token{Type: TokenString, Value: buf.String(), Pos: pos}
		}
		if r == '\\' {
			l.advance() // backslash
			if l.offset >= len(l.src) {
				l.addError(pos, "unterminated string literal")
				return Token{Type: TokenString, Value: buf.String(), Pos: pos}
			}
			escaped := l.advance()
			switch escaped {
			case 'n':
				buf.WriteByte('\n')
			case 't':
				buf.WriteByte('\t')
			case '\\':
				buf.WriteByte('\\')
			case '"':
				buf.WriteByte('"')
			default:
				l.addError(l.pos(), fmt.Sprintf("invalid escape sequence '\\%c'", escaped))
				buf.WriteRune(escaped)
			}
			continue
		}
		l.advance()
		buf.WriteRune(r)
	}
}

// readNumber consumes an integer or decimal number.
func (l *Lexer) readNumber() Token {
	pos := l.pos()
	start := l.offset
	for l.offset < len(l.src) {
		r, _ := l.peek()
		if r < '0' || r > '9' {
			break
		}
		l.advance()
	}
	// Decimal part
	if l.offset < len(l.src) && l.src[l.offset] == '.' {
		next := l.offset + 1
		if next < len(l.src) && l.src[next] >= '0' && l.src[next] <= '9' {
			l.advance() // .
			for l.offset < len(l.src) {
				r, _ := l.peek()
				if r < '0' || r > '9' {
					break
				}
				l.advance()
			}
		}
	}
	return Token{Type: TokenNumber, Value: string(l.src[start:l.offset]), Pos: pos}
}

// readIdent consumes an identifier and checks for boolean keywords.
func (l *Lexer) readIdent() Token {
	pos := l.pos()
	start := l.offset
	for l.offset < len(l.src) {
		r, _ := l.peek()
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		l.advance()
	}
	value := string(l.src[start:l.offset])
	switch value {
	case "true":
		return Token{Type: TokenTrue, Value: value, Pos: pos}
	case "false":
		return Token{Type: TokenFalse, Value: value, Pos: pos}
	default:
		return Token{Type: TokenIdent, Value: value, Pos: pos}
	}
}

// readSymbol consumes a single or multi-character symbol.
func (l *Lexer) readSymbol() Token {
	pos := l.pos()
	r := l.advance()
	switch r {
	case '{':
		return Token{Type: TokenLBrace, Value: "{", Pos: pos}
	case '}':
		return Token{Type: TokenRBrace, Value: "}", Pos: pos}
	case '(':
		return Token{Type: TokenLParen, Value: "(", Pos: pos}
	case ')':
		return Token{Type: TokenRParen, Value: ")", Pos: pos}
	case '[':
		return Token{Type: TokenLBracket, Value: "[", Pos: pos}
	case ']':
		return Token{Type: TokenRBracket, Value: "]", Pos: pos}
	case '=':
		return Token{Type: TokenEquals, Value: "=", Pos: pos}
	case ',':
		return Token{Type: TokenComma, Value: ",", Pos: pos}
	case '.':
		return Token{Type: TokenDot, Value: ".", Pos: pos}
	case '?':
		return Token{Type: TokenQuestion, Value: "?", Pos: pos}
	case ':':
		return Token{Type: TokenColon, Value: ":", Pos: pos}
	case '@':
		if l.offset < len(l.src) && l.src[l.offset] == '@' {
			l.advance()
			return Token{Type: TokenAtAt, Value: "@@", Pos: pos}
		}
		return Token{Type: TokenAt, Value: "@", Pos: pos}
	default:
		l.addError(pos, fmt.Sprintf("unexpected character %q", r))
		return Token{Type: TokenIdent, Value: string(r), Pos: pos}
	}
}

// addError records a lexer error with position info.
func (l *Lexer) addError(pos ast.Position, msg string) {
	var loc string
	if pos.File != "" {
		loc = fmt.Sprintf("%s:%d:%d", pos.File, pos.Line, pos.Column)
	} else {
		loc = fmt.Sprintf("%d:%d", pos.Line, pos.Column)
	}
	l.errors = append(l.errors, fmt.Sprintf("%s: %s", loc, msg))
}

// Tokenize scans the entire source and returns all tokens.
// The returned slice always ends with a TokenEOF.
// If lexer errors were encountered, they are returned as a combined error.
func (l *Lexer) Tokenize() ([]Token, error) {
	var tokens []Token
	for {
		tok := l.NextToken()
		tokens = append(tokens, tok)
		if tok.Type == TokenEOF {
			break
		}
	}
	if len(l.errors) > 0 {
		return tokens, fmt.Errorf("lexer errors:\n%s", strings.Join(l.errors, "\n"))
	}
	return tokens, nil
}
