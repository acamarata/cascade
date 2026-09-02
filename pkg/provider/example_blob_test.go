// Purpose: a runnable godoc Example for provider.BlobStore (Art.10.6),
//   backed by a minimal in-memory double (see example_store_test.go for the
//   package-level "why a local double, not storetest" rationale).
// Constraints: this double content-addresses with sha256 (stdlib, already a
//   transitive dependency of crypto elsewhere in the module) rather than
//   BLAKE3 — pkg/provider deliberately declares only the Hash SHAPE and
//   takes no BLAKE3 module dependency (queue.go's sibling doc.go and
//   blob.go's own header explain the same constraint for the real driver,
//   which lands in B/S-02.T2). The digest algorithm is irrelevant to what
//   this Example demonstrates: content-addressed Put/Get/Exists.
// SPORT: pkg.provider.BlobStore/ADDED (P1-E02-W1-S02-T1 CR follow-up).

package provider_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// exampleBlobStore is a real, minimal content-addressed store: Put hashes
// its input and stores it keyed by that hash, so storing identical content
// twice is a genuine no-op write, not a simulated one.
type exampleBlobStore struct {
	mu   sync.Mutex
	data map[string]map[provider.Hash][]byte
}

func newExampleBlobStore() *exampleBlobStore {
	return &exampleBlobStore{data: make(map[string]map[provider.Hash][]byte)}
}

func exampleHash(content []byte) provider.Hash {
	sum := sha256.Sum256(content)
	var h provider.Hash
	copy(h[:], sum[:])
	return h
}

func (b *exampleBlobStore) Put(_ context.Context, namespace string, data io.Reader) (provider.Hash, error) {
	content, err := io.ReadAll(data)
	if err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindInvalidInput, err, "reading blob content")
	}
	hash := exampleHash(content)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data[namespace] == nil {
		b.data[namespace] = make(map[provider.Hash][]byte)
	}
	b.data[namespace][hash] = content
	return hash, nil
}

func (b *exampleBlobStore) Get(_ context.Context, namespace string, hash provider.Hash) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	content, ok := b.data[namespace][hash]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "blob %s not found in namespace %q", hash, namespace)
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (b *exampleBlobStore) Delete(_ context.Context, namespace string, hash provider.Hash) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data[namespace], hash)
	return nil
}

func (b *exampleBlobStore) Exists(_ context.Context, namespace string, hash provider.Hash) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.data[namespace][hash]
	return ok, nil
}

// ExampleBlobStore demonstrates content-addressed Put, Exists, and Get: the
// caller never chooses the blob's key, only its content.
func ExampleBlobStore() {
	ctx := context.Background()
	var blobs provider.BlobStore = newExampleBlobStore()

	hash, err := blobs.Put(ctx, "artifacts", strings.NewReader("hello blob"))
	if err != nil {
		fmt.Println("put error:", err)
		return
	}

	exists, err := blobs.Exists(ctx, "artifacts", hash)
	if err != nil {
		fmt.Println("exists error:", err)
		return
	}
	fmt.Println(exists)

	rc, err := blobs.Get(ctx, "artifacts", hash)
	if err != nil {
		fmt.Println("get error:", err)
		return
	}
	defer func() { _ = rc.Close() }()
	content, err := io.ReadAll(rc)
	if err != nil {
		fmt.Println("read error:", err)
		return
	}
	fmt.Println(string(content))

	// Output:
	// true
	// hello blob
}
