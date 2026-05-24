package parser

import (
	"fmt"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// TokenType represents the type of a lexical token.
type TokenType int

const (
	TokenEOF        TokenType = iota
	TokenNewline              // \n
	TokenWhitespace           // spaces, tabs

	// Literals
	TokenIdent  // identifiers: model, User, field names
	TokenString // "quoted string"
	TokenNumber // 123, 3.14
	TokenTrue   // true
	TokenFalse  // false

	// Symbols
	TokenLBrace   // {
	TokenRBrace   // }
	TokenLParen   // (
	TokenRParen   // )
	TokenLBracket // [
	TokenRBracket // ]
	TokenEquals   // =
	TokenComma    // ,
	TokenDot      // .
	TokenAt       // @
	TokenAtAt     // @@
	TokenQuestion // ?
	TokenColon    // :

	// Comments
	TokenLineComment // // comment
	TokenDocComment  // /// doc comment
)

var tokenNames = [...]string{
	TokenEOF:        "EOF",
	TokenNewline:    "Newline",
	TokenWhitespace: "Whitespace",

	TokenIdent:  "Ident",
	TokenString: "String",
	TokenNumber: "Number",
	TokenTrue:   "True",
	TokenFalse:  "False",

	TokenLBrace:   "LBrace",
	TokenRBrace:   "RBrace",
	TokenLParen:   "LParen",
	TokenRParen:   "RParen",
	TokenLBracket: "LBracket",
	TokenRBracket: "RBracket",
	TokenEquals:   "Equals",
	TokenComma:    "Comma",
	TokenDot:      "Dot",
	TokenAt:       "At",
	TokenAtAt:     "AtAt",
	TokenQuestion: "Question",
	TokenColon:    "Colon",

	TokenLineComment: "LineComment",
	TokenDocComment:  "DocComment",
}

// String returns a human-readable name for the token type.
func (t TokenType) String() string {
	if int(t) < len(tokenNames) {
		return tokenNames[t]
	}
	return fmt.Sprintf("TokenType(%d)", int(t))
}

// Token represents a single lexical token produced by the lexer.
type Token struct {
	Type  TokenType
	Value string
	Pos   ast.Position
}

func (t Token) String() string {
	if t.Pos.File != "" {
		return fmt.Sprintf("%s(%q) at %s:%d:%d", t.Type, t.Value, t.Pos.File, t.Pos.Line, t.Pos.Column)
	}
	return fmt.Sprintf("%s(%q) at %d:%d", t.Type, t.Value, t.Pos.Line, t.Pos.Column)
}
