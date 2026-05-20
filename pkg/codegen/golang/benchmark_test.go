package golang

import (
	"os"
	"testing"

	"github.com/arsfy/gco-orm/pkg/schema/compiler"
	"github.com/arsfy/gco-orm/pkg/schema/parser"
)

func BenchmarkCodegen(b *testing.B) {
	source, err := os.ReadFile("../../../testdata/full_schema.gcorm")
	if err != nil {
		b.Fatal(err)
	}
	ds, err := parser.ParseMulti(map[string][]byte{"full_schema.gcorm": source})
	if err != nil {
		b.Fatal(err)
	}
	result := compiler.Compile(ds)
	if result.HasErrors() {
		b.Fatalf("compile errors: %v", result.Errors)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gen := NewGenerator(result.Schema)
		_, err := gen.Generate()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCodeSize(b *testing.B) {
	source, err := os.ReadFile("../../../testdata/full_schema.gcorm")
	if err != nil {
		b.Fatal(err)
	}
	ds, err := parser.ParseMulti(map[string][]byte{"full_schema.gcorm": source})
	if err != nil {
		b.Fatal(err)
	}
	result := compiler.Compile(ds)
	if result.HasErrors() {
		b.Fatalf("compile errors: %v", result.Errors)
	}

	gen := NewGenerator(result.Schema)
	files, err := gen.Generate()
	if err != nil {
		b.Fatal(err)
	}

	var total int
	for _, f := range files {
		total += len(f.Content)
	}

	b.ReportMetric(float64(total), "bytes/op")
	b.ReportMetric(float64(total)/float64(len(source)), "x-bloat")
}
