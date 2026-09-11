// Purpose: memTarget (the in-memory Target every test file in this package
//
//	shares) plus the layout and object/config accessor tests.
//
// SPORT: internal.backup.repo/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// memTarget is an in-memory Target every test file in this package uses in
// place of a real fs/s3/rclone driver (S-41.T3's, not built by this
// ticket). It is test infrastructure, never shipped: production Target
// implementations live entirely outside this package.
type memTarget struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemTarget() *memTarget { return &memTarget{data: map[string][]byte{}} }

func (m *memTarget) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = data
	return nil
}

func (m *memTarget) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.data[key]
	if !ok {
		return nil, cascade.New(cascade.KindNotFound, "memTarget: no such key "+key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memTarget) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *memTarget) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memTarget) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok
}

func TestObjectKeyShardsByHashPrefix(t *testing.T) {
	var hash [32]byte
	hash[0] = 0xAB
	hash[1] = 0xCD
	key := ObjectKey(hash)
	want := "objects/ab/ab" + "cd" + strings.Repeat("00", 30)
	if key != want {
		t.Fatalf("ObjectKey = %q, want %q", key, want)
	}
}

func TestPutObjectThenHasObjectThenGetObject(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	hash := ObjectHash([]byte("payload"))

	existed, err := PutObject(ctx, target, hash, []byte("ciphertext"))
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if existed {
		t.Fatal("PutObject reported existed=true on first write")
	}
	if !target.has(ObjectKey(hash)) {
		t.Fatal("PutObject did not store under the sharded object key")
	}

	ok, err := HasObject(ctx, target, hash)
	if err != nil || !ok {
		t.Fatalf("HasObject = %v, %v, want true, nil", ok, err)
	}

	got, err := GetObject(ctx, target, hash)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(got) != "ciphertext" {
		t.Fatalf("GetObject = %q, want %q", got, "ciphertext")
	}
}

func TestPutObjectSkipsExisting(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	hash := ObjectHash([]byte("payload"))
	if _, err := PutObject(ctx, target, hash, []byte("first")); err != nil {
		t.Fatalf("PutObject (first): %v", err)
	}
	existed, err := PutObject(ctx, target, hash, []byte("second"))
	if err != nil {
		t.Fatalf("PutObject (second): %v", err)
	}
	if !existed {
		t.Fatal("PutObject reported existed=false on second write of the same hash")
	}
	got, err := GetObject(ctx, target, hash)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(got) != "first" {
		t.Fatalf("second PutObject overwrote the stored payload: got %q, want %q", got, "first")
	}
}

func TestHasObjectFalseForAbsent(t *testing.T) {
	ok, err := HasObject(context.Background(), newMemTarget(), ObjectHash([]byte("nope")))
	if err != nil {
		t.Fatalf("HasObject: %v", err)
	}
	if ok {
		t.Fatal("HasObject reported true for a key never stored")
	}
}

func TestGetObjectNotFound(t *testing.T) {
	_, err := GetObject(context.Background(), newMemTarget(), ObjectHash([]byte("nope")))
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("GetObject error kind = %v, want KindNotFound", err)
	}
}

func TestWriteReadRepoConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	_, recipient := newTestAgeKeypair(t)
	cfg := RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient}

	if err := WriteRepoConfig(ctx, target, cfg); err != nil {
		t.Fatalf("WriteRepoConfig: %v", err)
	}
	if !target.has("config/repo.json") {
		t.Fatal("WriteRepoConfig did not write to the fixed config/repo.json key")
	}
	got, err := ReadRepoConfig(ctx, target)
	if err != nil {
		t.Fatalf("ReadRepoConfig: %v", err)
	}
	if got != cfg {
		t.Fatalf("ReadRepoConfig = %+v, want %+v", got, cfg)
	}
}

func TestReadRepoConfigNotFound(t *testing.T) {
	_, err := ReadRepoConfig(context.Background(), newMemTarget())
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("ReadRepoConfig error kind = %v, want KindNotFound", err)
	}
}

// faultyTarget wraps memTarget and can be configured to fail Put/Get with
// an injected, non-taxonomy error, so every KindUnavailable wrap branch in
// this package is reachable from a test without a real I/O fault.
type faultyTarget struct {
	*memTarget
	failPut, failGet bool
}

func newFaultyTarget() *faultyTarget { return &faultyTarget{memTarget: newMemTarget()} }

func (f *faultyTarget) Put(ctx context.Context, key string, r io.Reader) error {
	if f.failPut {
		return errInjectedFault
	}
	return f.memTarget.Put(ctx, key, r)
}

func (f *faultyTarget) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if f.failGet {
		return nil, errInjectedFault
	}
	return f.memTarget.Get(ctx, key)
}

var errInjectedFault = errInjected{}

type errInjected struct{}

func (errInjected) Error() string { return "faultyTarget: injected failure" }

// failingReadCloser errors on Read (never on Close), so GetObject and
// ReadRepoConfig's io.ReadAll wrap branches are reachable without a real
// I/O fault.
type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errInjectedFault }
func (failingReadCloser) Close() error             { return nil }

type readFailsTarget struct{ *memTarget }

func (r readFailsTarget) Get(context.Context, string) (io.ReadCloser, error) {
	return failingReadCloser{}, nil
}

func TestHasObjectWrapsNonNotFoundError(t *testing.T) {
	target := newFaultyTarget()
	target.failGet = true
	if _, err := HasObject(context.Background(), target, ObjectHash([]byte("x"))); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("HasObject error kind = %v, want KindUnavailable", err)
	}
}

func TestPutObjectWrapsPutError(t *testing.T) {
	target := newFaultyTarget()
	target.failPut = true
	if _, err := PutObject(context.Background(), target, ObjectHash([]byte("x")), []byte("y")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("PutObject error kind = %v, want KindUnavailable", err)
	}
}

func TestPutObjectPropagatesHasObjectError(t *testing.T) {
	target := newFaultyTarget()
	target.failGet = true
	if _, err := PutObject(context.Background(), target, ObjectHash([]byte("x")), []byte("y")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("PutObject error kind = %v, want KindUnavailable", err)
	}
}

func TestGetObjectWrapsNonNotFoundError(t *testing.T) {
	target := newFaultyTarget()
	target.failGet = true
	if _, err := GetObject(context.Background(), target, ObjectHash([]byte("x"))); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("GetObject error kind = %v, want KindUnavailable", err)
	}
}

func TestGetObjectWrapsReadBodyError(t *testing.T) {
	target := readFailsTarget{newMemTarget()}
	if _, err := GetObject(context.Background(), target, ObjectHash([]byte("x"))); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("GetObject error kind = %v, want KindUnavailable", err)
	}
}

func TestWriteRepoConfigWrapsPutError(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	target := newFaultyTarget()
	target.failPut = true
	err := WriteRepoConfig(context.Background(), target, RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("WriteRepoConfig error kind = %v, want KindUnavailable", err)
	}
}

func TestReadRepoConfigWrapsNonNotFoundError(t *testing.T) {
	target := newFaultyTarget()
	target.failGet = true
	if _, err := ReadRepoConfig(context.Background(), target); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ReadRepoConfig error kind = %v, want KindUnavailable", err)
	}
}

func TestReadRepoConfigWrapsReadBodyError(t *testing.T) {
	target := readFailsTarget{newMemTarget()}
	if _, err := ReadRepoConfig(context.Background(), target); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ReadRepoConfig error kind = %v, want KindUnavailable", err)
	}
}
