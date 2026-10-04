package rpc

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// TestRegistryMethods proves Methods returns every Register-ed name, sorted,
// including a name registered after Use, and that an empty registry yields an
// empty (non-nil) slice.
func TestRegistryMethods(t *testing.T) {
	noop := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	r := NewRegistry()
	if got := r.Methods(); got == nil || len(got) != 0 {
		t.Fatalf("empty registry Methods() = %#v, want empty non-nil slice", got)
	}
	r.Register("zeta.last", noop)
	r.Register("alpha.first", noop)
	r.Use(func(_ string, next HandlerFunc) HandlerFunc { return next })
	r.Register("mid.after-use", noop)
	r.Register("alpha.first", noop) // re-registration must not duplicate

	want := []string{"alpha.first", "mid.after-use", "zeta.last"}
	if got := r.Methods(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Methods() = %v, want %v", got, want)
	}
}
