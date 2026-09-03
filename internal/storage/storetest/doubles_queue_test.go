// Purpose: minimal, functional in-package test-only doubles for
//
//	VectorStore, BlobStore, Cache, and Queue (Art.1.1: doubles live here,
//	in a _test.go file, never in pkg/provider non-test files), used to
//	drive the corresponding Run*Tests suites in storetest_test.go. This is
//	a file-cap split of storetest_test.go, authorized in-package per
//	R-14.117 (splitting a ticket-owned file to stay under Art.10.3's
//	300-line cap).
//
// Constraints: no BLAKE3 dependency is available on this ticket (R-14.115);
//
//	fakeBlobStore content-addresses with stdlib crypto/sha256 instead
//	(also a 32-byte digest, so it fits provider.Hash's shape) — a
//	test-only substitution, not a claim that sha256 is the production
//	algorithm (BlobStore's real drivers use BLAKE3-256, per blob.go).
//
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).
package storetest

import (
	"context"
	"fmt"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"sync"
	"time"
)

// fakeQueue is a minimal functional provider.Queue with real
// visibility-timeout semantics and an optional bounded Capacity for the
// enqueue-overflow suite case. Its notion of "now" is wall-clock based by
// default (acceptable in a test-only double) unless a clock is supplied
// via newFakeQueueWithClock, in which case it reads that clock instead —
// letting it participate in RunQueueTests' WithQueueClock deterministic
// AckTimeout path (R-14.136).
type fakeQueue struct {
	mu       sync.Mutex
	messages map[string][]*fakeQueueMsg
	seq      int
	capacity int
	clock    *fakeClock // nil => now() falls back to time.Now
}

func newFakeQueue(capacity int) *fakeQueue {
	return &fakeQueue{messages: make(map[string][]*fakeQueueMsg), capacity: capacity}
}

// newFakeQueueWithClock is newFakeQueue but reads clock for all
// visibility-timeout comparisons instead of the wall clock, so that
// clock.Advance moves this queue's notion of "now" directly.
func newFakeQueueWithClock(capacity int, clock *fakeClock) *fakeQueue {
	return &fakeQueue{messages: make(map[string][]*fakeQueueMsg), capacity: capacity, clock: clock}
}

// now returns the wall clock, or the injected fakeClock's time when one
// was supplied via newFakeQueueWithClock.
func (f *fakeQueue) now() time.Time {
	if f.clock != nil {
		return f.clock.Now()
	}
	return time.Now()
}

// Capacity implements BoundedQueue.
func (f *fakeQueue) Capacity(string) int { return f.capacity }

func (f *fakeQueue) Enqueue(_ context.Context, ns string, payload []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.capacity > 0 && len(f.messages[ns]) >= f.capacity {
		return "", cascade.Newf(cascade.KindQuotaExhausted, "namespace %q is at capacity %d", ns, f.capacity)
	}
	f.seq++
	id := fmt.Sprintf("msg-%d", f.seq)
	f.messages[ns] = append(f.messages[ns], &fakeQueueMsg{id: id, payload: payload})
	return id, nil
}

func (f *fakeQueue) Dequeue(_ context.Context, ns string, visibility time.Duration) (*provider.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for _, msg := range f.messages[ns] {
		if msg.visibleAt.After(now) {
			continue
		}
		f.seq++
		msg.receipt = fmt.Sprintf("receipt-%d", f.seq)
		msg.visibleAt = now.Add(visibility)
		return &provider.Message{ID: msg.id, Payload: msg.payload, Receipt: msg.receipt}, nil
	}
	return nil, nil
}

func (f *fakeQueue) Ack(_ context.Context, ns, receipt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, msg := range f.messages[ns] {
		if msg.receipt != receipt {
			continue
		}
		if !msg.visibleAt.After(f.now()) {
			return cascade.Newf(cascade.KindTimeout, "receipt %q already expired", receipt)
		}
		f.messages[ns] = append(f.messages[ns][:i], f.messages[ns][i+1:]...)
		return nil
	}
	return cascade.Newf(cascade.KindTimeout, "receipt %q not found (expired and redelivered)", receipt)
}

func (f *fakeQueue) Nack(_ context.Context, ns, receipt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, msg := range f.messages[ns] {
		if msg.receipt != receipt {
			continue
		}
		if !msg.visibleAt.After(f.now()) {
			return cascade.Newf(cascade.KindTimeout, "receipt %q already expired", receipt)
		}
		msg.visibleAt = time.Time{}
		return nil
	}
	return cascade.Newf(cascade.KindTimeout, "receipt %q not found (expired and redelivered)", receipt)
}
