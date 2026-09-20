// Purpose: the bounded topic stack (component 4 of the four-component
//   segmenter): a FIFO of recently-committed topic labels, capped at a
//   configurable max depth, that segmenter_core.go's Segment loop reads to
//   know the CURRENT topic (its top) for label-change comparison and
//   pushes to on every commit.
// Inputs: topicID values pushed by segmenter_core.go on each committed
//   Boundary.
// Outputs: the current topic (Peek) and the evicted-when-full push
//   behavior (Push, Pop).
// Constraints: unexported - like hysteresisFilter, this is Segment's own
//   internal bookkeeping, never named as a dependency by any other
//   ticket's contract. "No unbounded growth" (this ticket's own task
//   wording) means Push on a full stack evicts the OLDEST entry, never
//   grows past maxDepth.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

// topicID names one topic on the stack. A defined string type rather than
// a bare string, per this ticket's own wording ("a bounded FIFO ... of
// TopicID values"), so a topicID and a raw classifier Label are never
// interchanged by accident even though both currently carry the same
// underlying label string - segmenter_core.go is the only place that
// converts one to the other.
type topicID string

// topicStack is a bounded FIFO of topicID: index 0 is the oldest entry,
// the last index is the current (most recently pushed) topic. Build one
// with newTopicStack; the zero value has maxDepth 0, which makes every
// Push a no-op eviction of what it just pushed, so it is never used
// directly.
type topicStack struct {
	maxDepth int
	items    []topicID
}

// newTopicStack returns a topicStack bounded to maxDepth entries. A
// non-positive maxDepth is not rejected here (the stack's zero value
// already behaves safely - see topicStack's doc comment); segmenter_core.go
// always calls this with defaultTopicStackDepth, a positive constant.
func newTopicStack(maxDepth int) *topicStack {
	return &topicStack{maxDepth: maxDepth}
}

// Push appends id as the new current topic. When the stack is already at
// maxDepth, the oldest entry (index 0) is evicted first, so Push never
// grows items past maxDepth - the "no unbounded growth" requirement.
func (s *topicStack) Push(id topicID) {
	if s.maxDepth <= 0 {
		return
	}
	if len(s.items) >= s.maxDepth {
		s.items = append(s.items[:0], s.items[1:]...)
	}
	s.items = append(s.items, id)
}

// Peek returns the current (most recently pushed) topic and true, or the
// zero topicID and false when the stack is empty (nothing has been pushed
// yet - segmenter_core.go's Segment loop treats this as "no prior topic",
// never as an error).
func (s *topicStack) Peek() (topicID, bool) {
	if len(s.items) == 0 {
		return "", false
	}
	return s.items[len(s.items)-1], true
}

// Pop removes and returns the current topic, and true; or the zero
// topicID and false when the stack is empty. Segment's own loop never
// calls Pop (it only pushes on commit and peeks to compare); Pop exists so
// topic_stack_test.go can assert eviction and ordering directly without
// reaching into the unexported items field.
func (s *topicStack) Pop() (topicID, bool) {
	id, ok := s.Peek()
	if !ok {
		return "", false
	}
	s.items = s.items[:len(s.items)-1]
	return id, true
}

// Len reports how many topics are currently on the stack, for
// topic_stack_test.go's bounds assertions.
func (s *topicStack) Len() int {
	return len(s.items)
}
