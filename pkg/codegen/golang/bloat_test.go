package golang

import (
	"bytes"
	"os"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/parser"
)

// TestCodegenBloatThreshold verifies that generated code line count stays
// within a regression threshold.
//
// DEVELOPMENT_PLAN.md §6.6 sets the aspirational target at ≤ 8× schema line
// count (excluding gen/internal). The current codegen has not yet applied the
// planned compression strategies (generics, shared scalar filters), so we use
// a higher regression ceiling for now and track progress toward the 8× goal.
func TestCodegenBloatThreshold(t *testing.T) {
	const (
		targetRatio     = 8.0   // §6.6 aspirational goal
		regressionLimit = 100.0 // ceiling after real query execution codegen
	)

	schemaSource, err := os.ReadFile("../../../testdata/full_schema.gcorm")
	if err != nil {
		t.Fatal(err)
	}

	ds, err := parser.ParseMulti(map[string][]byte{"full_schema.gcorm": schemaSource})
	if err != nil {
		t.Fatal(err)
	}

	result := compiler.Compile(ds)
	if result.HasErrors() {
		t.Fatalf("compile errors: %v", result.Errors)
	}

	gen := NewGenerator(result.Schema)
	files, err := gen.Generate()
	if err != nil {
		t.Fatal(err)
	}

	schemaLines := countLines(schemaSource)

	var totalGenLines int
	var totalGenBytes int
	for _, f := range files {
		totalGenLines += countLines(f.Content)
		totalGenBytes += len(f.Content)
	}

	lineRatio := float64(totalGenLines) / float64(schemaLines)
	byteRatio := float64(totalGenBytes) / float64(len(schemaSource))

	t.Logf("Schema: %d lines (%d bytes)", schemaLines, len(schemaSource))
	t.Logf("Generated: %d lines (%d bytes) across %d files", totalGenLines, totalGenBytes, len(files))
	t.Logf("Line ratio: %.1fx (target: %.0fx, regression limit: %.0fx)", lineRatio, targetRatio, regressionLimit)
	t.Logf("Byte ratio: %.1fx", byteRatio)

	if lineRatio > regressionLimit {
		t.Errorf("Bloat regression: generated code is %.1fx schema lines (limit %.0fx)", lineRatio, regressionLimit)
	}
	if lineRatio > targetRatio {
		t.Logf("NOTE: line ratio %.1fx exceeds §6.6 target of %.0fx — compression work pending", lineRatio, targetRatio)
	}
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}
