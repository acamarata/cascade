// Purpose: a runnable godoc Example for provider.Queue (Art.10.6), backed
//   by a minimal in-memory double (see example_store_test.go for the
//   package-level "why a local double, not storetest" rationale).
// Constraints: this double's visibility-timeout check uses time.Now()
//   directly for the same reason example_cache_test.go's TTL check does —
//   pkg/provider cannot reach internal/runtime.Clock, and _test.go files
//   are forbidigo-exempt (.golangci.yml exclusions, Art.7.3). Overflow
//   returns cascade.KindQuotaExhausted per R-14.125 — matching the fix this
//   CR follow-up applies to queue.go's doc comment and the storetest
//   conformance assertion.
// SPORT: pkg.provider.Queue/ADDED (P1-E02-W1-S02-T1 CR follow-up).

package provider_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type exampleQueueMsg struct {
	id        string
	payload   []byte
	receipt   string
	visibleAt time.Time // zero means immediately visible
}

// exampleQueue is a real, minimal at-least-once queue: Dequeue makes a
// message invisible until visibleAt, and a stale-receipt Ack/Nack against
// an already-redelivered message fails with cascade.KindTimeout.
type exampleQueue struct {
	mu       sync.Mutex
	messages map[string][]*exampleQueueMsg
	seq      int
	capacity int
}

func newExampleQueue(capacity int) *exampleQueue {
	return &exampleQueue{messages: make(map[string][]*exampleQueueMsg), capacity: capacity}
}

// Capacity implements storetest.BoundedQueue's shape so this double could
// also be run under RunQueueTests, though the Example below exercises it
// directly.
func (q *exampleQueue) Capacity(string) int { return q.capacity }

func (q *exampleQueue) Enqueue(_ context.Context, namespace string, payload []byte) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.capacity > 0 && len(q.messages[namespace]) >= q.capacity {
		return "", cascade.Newf(cascade.KindQuotaExhausted, "namespace %q is at capacity %d", namespace, q.capacity)
	}
	q.seq++
	id := fmt.Sprintf("msg-%d", q.seq)
	q.messages[namespace] = append(q.messages[namespace], &exampleQueueMsg{id: id, payload: payload, receipt: id + "-r1"})
	return id, nil
}

func (q *exampleQueue) Dequeue(_ context.Context, namespace string, visibilityTimeout time.Duration) (*provider.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	for _, msg := range q.messages[namespace] {
		if !msg.visibleAt.IsZero() && now.Before(msg.visibleAt) {
			continue
		}
		msg.visibleAt = now.Add(visibilityTimeout)
		return &provider.Message{ID: msg.id, Payload: msg.payload, Receipt: msg.receipt}, nil
	}
	return nil, nil
}

func (q *exampleQueue) Ack(_ context.Context, namespace, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, msg := range q.messages[namespace] {
		if msg.receipt == receipt {
			q.messages[namespace] = append(q.messages[namespace][:i], q.messages[namespace][i+1:]...)
			return nil
		}
	}
	return cascade.Newf(cascade.KindTimeout, "receipt %q in namespace %q is stale", receipt, namespace)
}

func (q *exampleQueue) Nack(_ context.Context, namespace, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, msg := range q.messages[namespace] {
		if msg.receipt == receipt {
			msg.visibleAt = time.Time{}
			return nil
		}
	}
	return cascade.Newf(cascade.KindTimeout, "receipt %q in namespace %q is stale", receipt, namespace)
}

// ExampleQueue demonstrates Enqueue/Dequeue/Ack and the enqueue-overflow
// error path (cascade.KindQuotaExhausted, R-14.125) once the namespace
// reaches its capacity.
func ExampleQueue() {
	ctx := context.Background()
	var q provider.Queue = newExampleQueue(1)

	id, err := q.Enqueue(ctx, "jobs", []byte("payload"))
	if err != nil {
		fmt.Println("enqueue error:", err)
		return
	}

	msg, err := q.Dequeue(ctx, "jobs", time.Minute)
	if err != nil {
		fmt.Println("dequeue error:", err)
		return
	}
	fmt.Println(msg.ID == id)

	if err := q.Ack(ctx, "jobs", msg.Receipt); err != nil {
		fmt.Println("ack error:", err)
		return
	}

	if _, err := q.Enqueue(ctx, "jobs", []byte("a")); err != nil {
		fmt.Println("enqueue error:", err)
		return
	}
	_, err = q.Enqueue(ctx, "jobs", []byte("overflow"))
	fmt.Println(cascade.HasKind(err, cascade.KindQuotaExhausted))

	// Output:
	// true
	// true
}
