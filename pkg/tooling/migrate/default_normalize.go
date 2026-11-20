package migrate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// equivalentJSONDefaults reports whether two literal Json defaults hold the
// same JSON document. PostgreSQL jsonb re-serializes values ({"a":1} is read
// back as {"a": 1}), so comparing the text would rewrite the default on every
// push.
func equivalentJSONDefaults(a, b *ir.Field) bool {
	if a == nil || b == nil || !strings.EqualFold(a.ScalarType, "Json") || !strings.EqualFold(b.ScalarType, "Json") {
		return false
	}
	av, ok := literalJSONDefault(a.Default)
	if !ok {
		return false
	}
	bv, ok := literalJSONDefault(b.Default)
	if !ok {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func literalJSONDefault(d *ir.DefaultValue) (any, bool) {
	if d == nil || d.IsFunction || d.IsArray {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(d.Value)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	return v, true
}
