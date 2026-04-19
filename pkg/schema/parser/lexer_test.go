package parser

import (
	"testing"
)

// filterTokens removes whitespace tokens but keeps newlines and EOF.
func filterTokens(tokens []Token) []Token {
	var result []Token
	for _, tok := range tokens {
		if tok.Type != TokenWhitespace {
			result = append(result, tok)
		}
	}
	return result
}

func tokenize(t *testing.T, input string) []Token {
	t.Helper()
	lex := NewLexer("test.gco", []byte(input))
	tokens, err := lex.Tokenize()
	if err != nil {
		t.Fatalf("unexpected lexer error: %v", err)
	}
	return filterTokens(tokens)
}

func tokenizeWithError(t *testing.T, input string) ([]Token, error) {
	t.Helper()
	lex := NewLexer("test.gco", []byte(input))
	tokens, err := lex.Tokenize()
	return filterTokens(tokens), err
}

func assertTokenTypes(t *testing.T, tokens []Token, expected []TokenType) {
	t.Helper()
	if len(tokens) != len(expected) {
		t.Errorf("expected %d tokens, got %d", len(expected), len(tokens))
		for i, tok := range tokens {
			t.Logf("  token[%d]: %s", i, tok)
		}
		return
	}
	for i, tt := range expected {
		if tokens[i].Type != tt {
			t.Errorf("token[%d]: expected %s, got %s (%q)", i, tt, tokens[i].Type, tokens[i].Value)
		}
	}
}

func assertTokenValues(t *testing.T, tokens []Token, expected []string) {
	t.Helper()
	if len(tokens) != len(expected) {
		t.Errorf("expected %d tokens, got %d", len(expected), len(tokens))
		for i, tok := range tokens {
			t.Logf("  token[%d]: %s", i, tok)
		}
		return
	}
	for i, val := range expected {
		if tokens[i].Value != val {
			t.Errorf("token[%d]: expected value %q, got %q", i, val, tokens[i].Value)
		}
	}
}

func TestBasicSymbols(t *testing.T) {
	tokens := tokenize(t, "{ } ( ) [ ] = , . @ @@ ? :")
	assertTokenTypes(t, tokens, []TokenType{
		TokenLBrace, TokenRBrace,
		TokenLParen, TokenRParen,
		TokenLBracket, TokenRBracket,
		TokenEquals, TokenComma, TokenDot,
		TokenAt, TokenAtAt, TokenQuestion, TokenColon,
		TokenEOF,
	})
}

func TestStringLiterals(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		tokens := tokenize(t, `"hello"`)
		assertTokenTypes(t, tokens, []TokenType{TokenString, TokenEOF})
		if tokens[0].Value != "hello" {
			t.Errorf("expected %q, got %q", "hello", tokens[0].Value)
		}
	})

	t.Run("escaped quotes", func(t *testing.T) {
		tokens := tokenize(t, `"with \"escapes\""`)
		assertTokenTypes(t, tokens, []TokenType{TokenString, TokenEOF})
		if tokens[0].Value != `with "escapes"` {
			t.Errorf("expected %q, got %q", `with "escapes"`, tokens[0].Value)
		}
	})

	t.Run("escaped newline", func(t *testing.T) {
		tokens := tokenize(t, `"with\nnewline"`)
		assertTokenTypes(t, tokens, []TokenType{TokenString, TokenEOF})
		if tokens[0].Value != "with\nnewline" {
			t.Errorf("expected %q, got %q", "with\nnewline", tokens[0].Value)
		}
	})

	t.Run("escaped tab", func(t *testing.T) {
		tokens := tokenize(t, `"with\ttab"`)
		assertTokenTypes(t, tokens, []TokenType{TokenString, TokenEOF})
		if tokens[0].Value != "with\ttab" {
			t.Errorf("expected %q, got %q", "with\ttab", tokens[0].Value)
		}
	})

	t.Run("escaped backslash", func(t *testing.T) {
		tokens := tokenize(t, `"back\\slash"`)
		assertTokenTypes(t, tokens, []TokenType{TokenString, TokenEOF})
		if tokens[0].Value != `back\slash` {
			t.Errorf("expected %q, got %q", `back\slash`, tokens[0].Value)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		tokens := tokenize(t, `""`)
		assertTokenTypes(t, tokens, []TokenType{TokenString, TokenEOF})
		if tokens[0].Value != "" {
			t.Errorf("expected empty string, got %q", tokens[0].Value)
		}
	})
}

func TestNumbers(t *testing.T) {
	t.Run("integer", func(t *testing.T) {
		tokens := tokenize(t, "123")
		assertTokenTypes(t, tokens, []TokenType{TokenNumber, TokenEOF})
		if tokens[0].Value != "123" {
			t.Errorf("expected %q, got %q", "123", tokens[0].Value)
		}
	})

	t.Run("decimal", func(t *testing.T) {
		tokens := tokenize(t, "3.14")
		assertTokenTypes(t, tokens, []TokenType{TokenNumber, TokenEOF})
		if tokens[0].Value != "3.14" {
			t.Errorf("expected %q, got %q", "3.14", tokens[0].Value)
		}
	})

	t.Run("zero", func(t *testing.T) {
		tokens := tokenize(t, "0")
		assertTokenTypes(t, tokens, []TokenType{TokenNumber, TokenEOF})
		if tokens[0].Value != "0" {
			t.Errorf("expected %q, got %q", "0", tokens[0].Value)
		}
	})
}

func TestBooleans(t *testing.T) {
	tokens := tokenize(t, "true false")
	assertTokenTypes(t, tokens, []TokenType{TokenTrue, TokenFalse, TokenEOF})
	if tokens[0].Value != "true" {
		t.Errorf("expected %q, got %q", "true", tokens[0].Value)
	}
	if tokens[1].Value != "false" {
		t.Errorf("expected %q, got %q", "false", tokens[1].Value)
	}
}

func TestIdentifiers(t *testing.T) {
	tokens := tokenize(t, "model User createdAt String _private")
	assertTokenTypes(t, tokens, []TokenType{
		TokenIdent, TokenIdent, TokenIdent, TokenIdent, TokenIdent, TokenEOF,
	})
	assertTokenValues(t, tokens, []string{
		"model", "User", "createdAt", "String", "_private", "",
	})
}

func TestLineComment(t *testing.T) {
	tokens := tokenize(t, "// this is a comment")
	assertTokenTypes(t, tokens, []TokenType{TokenLineComment, TokenEOF})
	if tokens[0].Value != "// this is a comment" {
		t.Errorf("expected %q, got %q", "// this is a comment", tokens[0].Value)
	}
}

func TestDocComment(t *testing.T) {
	tokens := tokenize(t, "/// doc comment")
	assertTokenTypes(t, tokens, []TokenType{TokenDocComment, TokenEOF})
	if tokens[0].Value != "/// doc comment" {
		t.Errorf("expected %q, got %q", "/// doc comment", tokens[0].Value)
	}
}

func TestCommentBeforeNewline(t *testing.T) {
	tokens := tokenize(t, "// comment\nident")
	assertTokenTypes(t, tokens, []TokenType{
		TokenLineComment, TokenNewline, TokenIdent, TokenEOF,
	})
}

func TestAtAt(t *testing.T) {
	tokens := tokenize(t, "@@index")
	assertTokenTypes(t, tokens, []TokenType{TokenAtAt, TokenIdent, TokenEOF})
	if tokens[0].Value != "@@" {
		t.Errorf("expected %q, got %q", "@@", tokens[0].Value)
	}
	if tokens[1].Value != "index" {
		t.Errorf("expected %q, got %q", "index", tokens[1].Value)
	}
}

func TestAtVsAtAt(t *testing.T) {
	tokens := tokenize(t, "@id @@map")
	assertTokenTypes(t, tokens, []TokenType{
		TokenAt, TokenIdent, TokenAtAt, TokenIdent, TokenEOF,
	})
	if tokens[0].Value != "@" {
		t.Errorf("expected %q, got %q", "@", tokens[0].Value)
	}
	if tokens[1].Value != "id" {
		t.Errorf("expected %q, got %q", "id", tokens[1].Value)
	}
	if tokens[2].Value != "@@" {
		t.Errorf("expected %q, got %q", "@@", tokens[2].Value)
	}
	if tokens[3].Value != "map" {
		t.Errorf("expected %q, got %q", "map", tokens[3].Value)
	}
}

func TestDatasourceBlock(t *testing.T) {
	input := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
  schema   = "public"
}`
	tokens := tokenize(t, input)
	assertTokenTypes(t, tokens, []TokenType{
		// datasource db {
		TokenIdent, TokenIdent, TokenLBrace, TokenNewline,
		// provider = "postgresql"
		TokenIdent, TokenEquals, TokenString, TokenNewline,
		// url      = env("DATABASE_URL")
		TokenIdent, TokenEquals, TokenIdent, TokenLParen, TokenString, TokenRParen, TokenNewline,
		// schema   = "public"
		TokenIdent, TokenEquals, TokenString, TokenNewline,
		// }
		TokenRBrace,
		TokenEOF,
	})
	// Verify some key values
	if tokens[0].Value != "datasource" {
		t.Errorf("expected %q, got %q", "datasource", tokens[0].Value)
	}
	if tokens[1].Value != "db" {
		t.Errorf("expected %q, got %q", "db", tokens[1].Value)
	}
	if tokens[6].Value != "postgresql" {
		t.Errorf("expected %q, got %q", "postgresql", tokens[6].Value)
	}
	if tokens[10].Value != "env" {
		t.Errorf("expected %q, got %q", "env", tokens[10].Value)
	}
	if tokens[12].Value != "DATABASE_URL" {
		t.Errorf("expected %q, got %q", "DATABASE_URL", tokens[12].Value)
	}
}

func TestModelBlock(t *testing.T) {
	input := `model User {
  id        String   @id @default(uuid())
  email     String   @unique
  profile   Profile?
  posts     Post[]
  createdAt DateTime @default(now())

  @@index([createdAt])
  @@map("users")
}`
	tokens := tokenize(t, input)

	// Verify key tokens exist in order
	expectedSequences := []struct {
		typ TokenType
		val string
	}{
		{TokenIdent, "model"},
		{TokenIdent, "User"},
		{TokenLBrace, "{"},
		{TokenIdent, "id"},
		{TokenIdent, "String"},
		{TokenAt, "@"},
		{TokenIdent, "id"},
		{TokenAt, "@"},
		{TokenIdent, "default"},
		{TokenLParen, "("},
		{TokenIdent, "uuid"},
		{TokenLParen, "("},
		{TokenRParen, ")"},
		{TokenRParen, ")"},
		{TokenIdent, "email"},
		{TokenIdent, "String"},
		{TokenAt, "@"},
		{TokenIdent, "unique"},
		{TokenIdent, "profile"},
		{TokenIdent, "Profile"},
		{TokenQuestion, "?"},
		{TokenIdent, "posts"},
		{TokenIdent, "Post"},
		{TokenLBracket, "["},
		{TokenRBracket, "]"},
		{TokenIdent, "createdAt"},
		{TokenIdent, "DateTime"},
		{TokenAt, "@"},
		{TokenIdent, "default"},
		{TokenLParen, "("},
		{TokenIdent, "now"},
		{TokenLParen, "("},
		{TokenRParen, ")"},
		{TokenRParen, ")"},
		{TokenAtAt, "@@"},
		{TokenIdent, "index"},
		{TokenLParen, "("},
		{TokenLBracket, "["},
		{TokenIdent, "createdAt"},
		{TokenRBracket, "]"},
		{TokenRParen, ")"},
		{TokenAtAt, "@@"},
		{TokenIdent, "map"},
		{TokenLParen, "("},
		{TokenString, "users"},
		{TokenRParen, ")"},
		{TokenRBrace, "}"},
	}

	// Filter out newlines for this check
	var nonNewline []Token
	for _, tok := range tokens {
		if tok.Type != TokenNewline && tok.Type != TokenEOF {
			nonNewline = append(nonNewline, tok)
		}
	}

	if len(nonNewline) != len(expectedSequences) {
		t.Fatalf("expected %d non-newline tokens, got %d", len(expectedSequences), len(nonNewline))
	}

	for i, exp := range expectedSequences {
		if nonNewline[i].Type != exp.typ {
			t.Errorf("token[%d]: expected type %s, got %s", i, exp.typ, nonNewline[i].Type)
		}
		if nonNewline[i].Value != exp.val {
			t.Errorf("token[%d]: expected value %q, got %q", i, exp.val, nonNewline[i].Value)
		}
	}
}

func TestEnumBlock(t *testing.T) {
	input := `enum Role {
  USER
  ADMIN
}`
	tokens := tokenize(t, input)
	assertTokenTypes(t, tokens, []TokenType{
		TokenIdent, TokenIdent, TokenLBrace, TokenNewline,
		TokenIdent, TokenNewline,
		TokenIdent, TokenNewline,
		TokenRBrace,
		TokenEOF,
	})
	if tokens[0].Value != "enum" {
		t.Errorf("expected %q, got %q", "enum", tokens[0].Value)
	}
	if tokens[1].Value != "Role" {
		t.Errorf("expected %q, got %q", "Role", tokens[1].Value)
	}
	if tokens[4].Value != "USER" {
		t.Errorf("expected %q, got %q", "USER", tokens[4].Value)
	}
	if tokens[6].Value != "ADMIN" {
		t.Errorf("expected %q, got %q", "ADMIN", tokens[6].Value)
	}
}

func TestErrorUnterminatedString(t *testing.T) {
	_, err := tokenizeWithError(t, `"unterminated`)
	if err == nil {
		t.Fatal("expected error for unterminated string")
	}
}

func TestErrorUnterminatedStringAtNewline(t *testing.T) {
	_, err := tokenizeWithError(t, "\"unterminated\nmore")
	if err == nil {
		t.Fatal("expected error for string crossing newline")
	}
}

func TestErrorInvalidCharacter(t *testing.T) {
	_, err := tokenizeWithError(t, "model User { # invalid }")
	if err == nil {
		t.Fatal("expected error for invalid character #")
	}
}

func TestPositionTracking(t *testing.T) {
	input := "model User {\n  id String\n}"
	lex := NewLexer("test.gco", []byte(input))
	tokens, err := lex.Tokenize()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tokens = filterTokens(tokens)

	// model -> line 1, col 1
	if tokens[0].Pos.Line != 1 || tokens[0].Pos.Column != 1 {
		t.Errorf("'model': expected 1:1, got %d:%d", tokens[0].Pos.Line, tokens[0].Pos.Column)
	}

	// User -> line 1, col 7
	if tokens[1].Pos.Line != 1 || tokens[1].Pos.Column != 7 {
		t.Errorf("'User': expected 1:7, got %d:%d", tokens[1].Pos.Line, tokens[1].Pos.Column)
	}

	// { -> line 1, col 12
	if tokens[2].Pos.Line != 1 || tokens[2].Pos.Column != 12 {
		t.Errorf("'{': expected 1:12, got %d:%d", tokens[2].Pos.Line, tokens[2].Pos.Column)
	}

	// \n -> line 1, col 13
	if tokens[3].Type != TokenNewline || tokens[3].Pos.Line != 1 || tokens[3].Pos.Column != 13 {
		t.Errorf("newline: expected Newline at 1:13, got %s at %d:%d", tokens[3].Type, tokens[3].Pos.Line, tokens[3].Pos.Column)
	}

	// id -> line 2, col 3
	if tokens[4].Pos.Line != 2 || tokens[4].Pos.Column != 3 {
		t.Errorf("'id': expected 2:3, got %d:%d", tokens[4].Pos.Line, tokens[4].Pos.Column)
	}

	// String -> line 2, col 6
	if tokens[5].Pos.Line != 2 || tokens[5].Pos.Column != 6 {
		t.Errorf("'String': expected 2:6, got %d:%d", tokens[5].Pos.Line, tokens[5].Pos.Column)
	}

	// \n -> line 2, col 12
	if tokens[6].Type != TokenNewline || tokens[6].Pos.Line != 2 || tokens[6].Pos.Column != 12 {
		t.Errorf("newline: expected Newline at 2:12, got %s at %d:%d", tokens[6].Type, tokens[6].Pos.Line, tokens[6].Pos.Column)
	}

	// } -> line 3, col 1
	if tokens[7].Pos.Line != 3 || tokens[7].Pos.Column != 1 {
		t.Errorf("'}': expected 3:1, got %d:%d", tokens[7].Pos.Line, tokens[7].Pos.Column)
	}

	// Verify filename is set
	if tokens[0].Pos.File != "test.gco" {
		t.Errorf("expected filename %q, got %q", "test.gco", tokens[0].Pos.File)
	}
}

func TestPositionMultipleLines(t *testing.T) {
	input := "a\nb\nc"
	tokens := tokenize(t, input)
	// a(1:1) \n(1:2) b(2:1) \n(2:2) c(3:1) EOF(3:2)
	assertTokenTypes(t, tokens, []TokenType{
		TokenIdent, TokenNewline, TokenIdent, TokenNewline, TokenIdent, TokenEOF,
	})
	if tokens[0].Pos.Line != 1 || tokens[0].Pos.Column != 1 {
		t.Errorf("'a': expected 1:1, got %d:%d", tokens[0].Pos.Line, tokens[0].Pos.Column)
	}
	if tokens[2].Pos.Line != 2 || tokens[2].Pos.Column != 1 {
		t.Errorf("'b': expected 2:1, got %d:%d", tokens[2].Pos.Line, tokens[2].Pos.Column)
	}
	if tokens[4].Pos.Line != 3 || tokens[4].Pos.Column != 1 {
		t.Errorf("'c': expected 3:1, got %d:%d", tokens[4].Pos.Line, tokens[4].Pos.Column)
	}
}

func TestEnvFunction(t *testing.T) {
	tokens := tokenize(t, `env("DATABASE_URL")`)
	assertTokenTypes(t, tokens, []TokenType{
		TokenIdent, TokenLParen, TokenString, TokenRParen, TokenEOF,
	})
	if tokens[0].Value != "env" {
		t.Errorf("expected %q, got %q", "env", tokens[0].Value)
	}
	if tokens[2].Value != "DATABASE_URL" {
		t.Errorf("expected %q, got %q", "DATABASE_URL", tokens[2].Value)
	}
}

func TestGeneratorBlock(t *testing.T) {
	input := `generator go {
  provider     = "gco-go"
  output       = "./gen"
  package      = "db"
  emitRuntime  = false
}`
	tokens := tokenize(t, input)
	assertTokenTypes(t, tokens, []TokenType{
		TokenIdent, TokenIdent, TokenLBrace, TokenNewline,
		TokenIdent, TokenEquals, TokenString, TokenNewline,
		TokenIdent, TokenEquals, TokenString, TokenNewline,
		TokenIdent, TokenEquals, TokenString, TokenNewline,
		TokenIdent, TokenEquals, TokenFalse, TokenNewline,
		TokenRBrace,
		TokenEOF,
	})
	if tokens[0].Value != "generator" {
		t.Errorf("expected %q, got %q", "generator", tokens[0].Value)
	}
	if tokens[18].Type != TokenFalse {
		t.Errorf("expected TokenFalse, got %s", tokens[18].Type)
	}
}

func TestEmptyInput(t *testing.T) {
	tokens := tokenize(t, "")
	assertTokenTypes(t, tokens, []TokenType{TokenEOF})
}

func TestOnlyWhitespace(t *testing.T) {
	tokens := tokenize(t, "   \t  ")
	assertTokenTypes(t, tokens, []TokenType{TokenEOF})
}

func TestOnlyNewlines(t *testing.T) {
	tokens := tokenize(t, "\n\n\n")
	assertTokenTypes(t, tokens, []TokenType{
		TokenNewline, TokenNewline, TokenNewline, TokenEOF,
	})
}

func TestTokenTypeString(t *testing.T) {
	cases := []struct {
		tt   TokenType
		want string
	}{
		{TokenEOF, "EOF"},
		{TokenIdent, "Ident"},
		{TokenString, "String"},
		{TokenAtAt, "AtAt"},
		{TokenDocComment, "DocComment"},
		{TokenType(999), "TokenType(999)"},
	}
	for _, tc := range cases {
		got := tc.tt.String()
		if got != tc.want {
			t.Errorf("%d.String() = %q, want %q", int(tc.tt), got, tc.want)
		}
	}
}

func TestTokenString(t *testing.T) {
	tok := Token{
		Type:  TokenIdent,
		Value: "model",
	}
	s := tok.String()
	if s == "" {
		t.Error("Token.String() returned empty string")
	}
}
