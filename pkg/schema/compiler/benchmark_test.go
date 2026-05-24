package compiler

import (
	"os"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/parser"
)

func BenchmarkCompile(b *testing.B) {
	source, err := os.ReadFile("../../../testdata/full_schema.gcorm")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ds, err := parser.ParseMulti(map[string][]byte{"full_schema.gcorm": source})
		if err != nil {
			b.Fatal(err)
		}
		result := Compile(ds)
		if result.HasErrors() {
			b.Fatalf("compile errors: %v", result.Errors)
		}
	}
}

func BenchmarkParseOnly(b *testing.B) {
	source, err := os.ReadFile("../../../testdata/full_schema.gcorm")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := parser.ParseMulti(map[string][]byte{"full_schema.gcorm": source})
		if err != nil {
			b.Fatal(err)
		}
	}
}
