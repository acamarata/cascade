package rpc

// Purpose (this file): verify that Origin attaches to a context and is
// retrieved intact, and that an absent or zero Origin reports absent.

import (
	"context"
	"testing"
)

// TestOriginRoundTrip verifies that a context carrying an Origin returns it
// via OriginFrom, and that the returned origin matches the one attached.
func TestOriginRoundTrip(t *testing.T) {
	ctx := WithOrigin(context.Background(), OriginTool)
	o, ok := OriginFrom(ctx)
	if !ok {
		t.Fatal("OriginFrom reported absent for a context carrying OriginTool")
	}
	if o != OriginTool {
		t.Errorf("OriginFrom = %q, want %q", o, OriginTool)
	}
}

// TestOriginAbsentIsFalse verifies that a context without an Origin, or with
// the zero Origin, reports absent.
func TestOriginAbsentIsFalse(t *testing.T) {
	if _, ok := OriginFrom(context.Background()); ok {
		t.Error("OriginFrom reported present for an empty context")
	}
	if _, ok := OriginFrom(WithOrigin(context.Background(), "")); ok {
		t.Error("OriginFrom reported present for a context carrying the zero Origin")
	}
}
