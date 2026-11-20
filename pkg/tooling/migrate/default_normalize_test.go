package migrate

import (
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func TestEquivalentJSONDefaults(t *testing.T) {
	field := func(v string) *ir.Field {
		return &ir.Field{ScalarType: "Json", Default: &ir.DefaultValue{Value: v, IsString: true}}
	}
	if !equivalentJSONDefaults(field(`{"a":1,"b":[1,2]}`), field(`{"b": [1, 2], "a": 1}`)) {
		t.Fatal("reformatted jsonb default should be equivalent")
	}
	if equivalentJSONDefaults(field(`{"a":1}`), field(`{"a":2}`)) {
		t.Fatal("different JSON values must differ")
	}
	text := &ir.Field{ScalarType: "String", Default: &ir.DefaultValue{Value: `{"a":1}`}}
	if equivalentJSONDefaults(text, &ir.Field{ScalarType: "String", Default: &ir.DefaultValue{Value: `{"a": 1}`}}) {
		t.Fatal("non-Json fields compare as text")
	}
}
