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
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeVectorStore is a minimal functional provider.VectorStore: exact
// dot-product ranking, no ANN index. Sufficient to prove
// RunVectorStoreTests' assertions genuinely exercise a driver.
type fakeVectorStore struct {
	mu   sync.Mutex
	data map[string]map[string]provider.Vector
}

func newFakeVectorStore() *fakeVectorStore {
	return &fakeVectorStore{data: make(map[string]map[string]provider.Vector)}
}

func (f *fakeVectorStore) Upsert(_ context.Context, ns string, vecs []provider.Vector) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.data[ns]
	if !ok {
		m = make(map[string]provider.Vector)
		f.data[ns] = m
	}
	for _, v := range vecs {
		m[v.ID] = v
	}
	return nil
}

func (f *fakeVectorStore) Query(_ context.Context, ns string, q provider.VectorQuery) ([]provider.VectorMatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matches []provider.VectorMatch
	for _, v := range f.data[ns] {
		matches = append(matches, provider.VectorMatch{ID: v.ID, Score: dotProduct(v.Values, q.Values), Metadata: v.Metadata})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if q.TopK > 0 && len(matches) > q.TopK {
		matches = matches[:q.TopK]
	}
	return matches, nil
}

func (f *fakeVectorStore) Delete(_ context.Context, ns string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.data[ns], id)
	}
	return nil
}

func (f *fakeVectorStore) Count(_ context.Context, ns string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data[ns]), nil
}

func (f *fakeVectorStore) Namespaces(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.data))
	for ns := range f.data {
		out = append(out, ns)
	}
	return out, nil
}

func dotProduct(a, b []float32) float32 {
	var sum float32
	for i := range a {
		if i < len(b) {
			sum += a[i] * b[i]
		}
	}
	return sum
}

// fakeBlobStore is a minimal functional provider.BlobStore, content
// addressed with sha256 (see file doc for why not BLAKE3).
type fakeBlobStore struct {
	mu   sync.Mutex
	data map[string]map[provider.Hash][]byte
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{data: make(map[string]map[provider.Hash][]byte)}
}

func (f *fakeBlobStore) Put(_ context.Context, ns string, r io.Reader) (provider.Hash, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindInvalidInput, err, "reading blob content")
	}
	h := provider.Hash(sha256.Sum256(content))
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.data[ns]
	if !ok {
		m = make(map[provider.Hash][]byte)
		f.data[ns] = m
	}
	m[h] = content
	return h, nil
}

func (f *fakeBlobStore) Get(_ context.Context, ns string, h provider.Hash) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.data[ns][h]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "blob %s not found in namespace %q", h, ns)
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (f *fakeBlobStore) Delete(_ context.Context, ns string, h provider.Hash) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data[ns], h)
	return nil
}

func (f *fakeBlobStore) Exists(_ context.Context, ns string, h provider.Hash) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[ns][h]
	return ok, nil
}

// fakeCache is a minimal functional provider.Cache (no TTL enforcement —
// the suite does not test expiry, only hit/miss/evict/flush).
type fakeCache struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newFakeCache() *fakeCache {
	return &fakeCache{data: make(map[string]map[string][]byte)}
}

func (f *fakeCache) Get(_ context.Context, ns, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[ns][key]
	return v, ok, nil
}

func (f *fakeCache) Set(_ context.Context, ns, key string, value []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.data[ns]
	if !ok {
		m = make(map[string][]byte)
		f.data[ns] = m
	}
	m[key] = value
	return nil
}

func (f *fakeCache) Evict(_ context.Context, ns, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data[ns], key)
	return nil
}

func (f *fakeCache) Flush(_ context.Context, ns string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, ns)
	return nil
}

// fakeQueueMsg is one fakeQueue entry.
type fakeQueueMsg struct {
	id        string
	payload   []byte
	receipt   string
	visibleAt time.Time // zero means "immediately visible"
}

// fakeQueue is a minimal functional provider.Queue with real
// visibility-timeout semantics (wall-clock based — acceptable in a
// test-only double) and an optional bounded Capacity for the
// enqueue-overflow suite case.
type fakeQueue struct {
	mu       sync.Mutex
	messages map[string][]*fakeQueueMsg
	seq      int
	capacity int
}

func newFakeQueue(capacity int) *fakeQueue {
	return &fakeQueue{messages: make(map[string][]*fakeQueueMsg), capacity: capacity}
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
	now := time.Now()
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
		if !msg.visibleAt.After(time.Now()) {
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
		if !msg.visibleAt.After(time.Now()) {
			return cascade.Newf(cascade.KindTimeout, "receipt %q already expired", receipt)
		}
		msg.visibleAt = time.Time{}
		return nil
	}
	return cascade.Newf(cascade.KindTimeout, "receipt %q not found (expired and redelivered)", receipt)
}
