// Package topics (topic_stack_test.go): Purpose: topicStack's bounded FIFO
// behavior in isolation - empty-stack Peek/Pop, ordering, and the "no
// unbounded growth" eviction rule pushing past maxDepth must enforce.
package topics

import "testing"

func TestTopicStack_EmptyPeekAndPop(t *testing.T) {
	s := newTopicStack(3)
	if _, ok := s.Peek(); ok {
		t.Fatal("Peek on empty stack: ok = true, want false")
	}
	if _, ok := s.Pop(); ok {
		t.Fatal("Pop on empty stack: ok = true, want false")
	}
	if s.Len() != 0 {
		t.Fatalf("Len on empty stack = %d, want 0", s.Len())
	}
}

func TestTopicStack_PushPeekPopOrder(t *testing.T) {
	s := newTopicStack(3)
	s.Push("a")
	s.Push("b")
	if top, ok := s.Peek(); !ok || top != "b" {
		t.Fatalf("Peek after push a,b = (%v, %v), want (b, true)", top, ok)
	}
	if id, ok := s.Pop(); !ok || id != "b" {
		t.Fatalf("Pop = (%v, %v), want (b, true)", id, ok)
	}
	if top, ok := s.Peek(); !ok || top != "a" {
		t.Fatalf("Peek after popping b = (%v, %v), want (a, true)", top, ok)
	}
}

// TestTopicStack_EvictsOldestPastMaxDepth proves "no unbounded growth" by
// mutation: pushing maxDepth+2 distinct entries must leave Len()==maxDepth
// (never maxDepth+2, which is what an unbounded append would leave), and
// the two OLDEST entries ("t0", "t1") must be gone while the newest
// (maxDepth+1's worth) remain, in order.
func TestTopicStack_EvictsOldestPastMaxDepth(t *testing.T) {
	const depth = 3
	s := newTopicStack(depth)
	ids := []topicID{"t0", "t1", "t2", "t3", "t4"}
	for _, id := range ids {
		s.Push(id)
	}
	if s.Len() != depth {
		t.Fatalf("Len after pushing %d entries into depth %d = %d, want %d", len(ids), depth, s.Len(), depth)
	}
	want := []topicID{"t2", "t3", "t4"}
	for i := len(want) - 1; i >= 0; i-- {
		id, ok := s.Pop()
		if !ok || id != want[i] {
			t.Fatalf("pop order mismatch at position %d: got (%v, %v), want (%v, true)", i, id, ok, want[i])
		}
	}
	if s.Len() != 0 {
		t.Fatalf("Len after popping every surviving entry = %d, want 0", s.Len())
	}
}

func TestTopicStackNonPositiveDepthNeverGrows(t *testing.T) {
	s := newTopicStack(0)
	s.Push("a")
	s.Push("b")
	if s.Len() != 0 {
		t.Fatalf("Len with maxDepth=0 after two pushes = %d, want 0", s.Len())
	}
}
