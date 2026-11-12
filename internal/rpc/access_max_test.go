package rpc

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAccessMaxIsAccessNode keeps the closed access range inclusive of node methods.
func TestAccessMaxIsAccessNode(t *testing.T) {
	if accessMax != AccessNode {
		t.Fatalf("accessMax = %d, want AccessNode = %d", accessMax, AccessNode)
	}
	err := checkAccess(context.Background(), AccessNode+1)
	if err != errUnknownAccess {
		t.Fatalf("class above AccessNode: %v, want canonical unknown access error", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal || err.(*cascade.Error).Msg != "unknown access class" {
		t.Fatalf("class above AccessNode: %v, want internal unknown access class", err)
	}
}
