// Package compiler orchestrates the full schema compilation pipeline:
// validate → resolve → produce IR.
package compiler

import (
	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/schema/resolve"
	"github.com/arsfy/gcorm/pkg/schema/validator"
)

// CompileResult holds the output of the compilation pipeline.
type CompileResult struct {
	Schema     *ir.Schema
	Validation *validator.ValidationResult
	Errors     []error
}

// HasErrors returns true when the compilation produced any errors.
func (r *CompileResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// Compile runs the full compilation pipeline: validate → resolve → annotate.
func Compile(ds *ast.DocumentSet) *CompileResult {
	result := &CompileResult{}

	// Step 1: structural validation.
	vr := validator.ValidateDocumentSet(ds)
	result.Validation = vr
	if vr.HasErrors() {
		result.Errors = vr.AllErrors()
		return result
	}

	// Step 2: resolve references and build IR.
	schema, errs := resolve.Resolve(ds)
	if len(errs) > 0 {
		result.Errors = errs
		return result
	}

	result.Schema = schema
	return result
}

// CompileFile compiles a single schema file represented as raw bytes.
// The filename is used for error reporting only.
func CompileFile(filename string, src []byte) *CompileResult {
	_ = src // Parsing is handled by the parser package (not yet wired in).
	// Build a minimal DocumentSet with the file recorded.
	ds := &ast.DocumentSet{
		Documents: []*ast.Document{{}},
		Files:     []string{filename},
	}
	return Compile(ds)
}
