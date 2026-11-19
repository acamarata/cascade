package graph

import "testing"

// TestProtocol9Incompatible verifies that a node with protocol 9..9 is incompatible.
func TestProtocol9Incompatible(t *testing.T) {
	n := Node{ID: "A", ProtoMin: 9, ProtoMax: 9}
	if !IsIncompatible(n) {
		t.Fatal("expected node with protocol 9..9 to be incompatible")
	}
}

// TestSchedulableSkipsProtocol9 verifies that Schedulable excludes protocol 9..9 nodes.
func TestSchedulableSkipsProtocol9(t *testing.T) {
	nodes := []Node{
		{ID: "A", ProtoMin: 1, ProtoMax: 1},
		{ID: "B", ProtoMin: 9, ProtoMax: 9},
	}
	got := Schedulable(nodes)
	if len(got) != 1 || got[0].ID != "A" {
		t.Fatalf("expected [A], got %v", got)
	}
}

// TestOtherProtocolsSchedulable verifies that non-protocol-9 nodes are compatible and kept.
func TestOtherProtocolsSchedulable(t *testing.T) {
	n := Node{ID: "A", ProtoMin: 1, ProtoMax: 1}
	if IsIncompatible(n) {
		t.Fatal("expected node with protocol 1 to be compatible")
	}
	got := Schedulable([]Node{n})
	if len(got) != 1 || got[0].ID != "A" {
		t.Fatalf("expected [A], got %v", got)
	}
}

// TestEmptyNodeListSchedulable verifies that an empty input returns empty output without panic.
func TestEmptyNodeListSchedulable(t *testing.T) {
	got := Schedulable(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}
