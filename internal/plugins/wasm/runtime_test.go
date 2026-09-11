package wasm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Shared test fakes used across this package's test files.

type fakeLogger struct{ lines []string }

func (f *fakeLogger) Log(_ context.Context, level, msg string) error {
	f.lines = append(f.lines, level+": "+msg)
	return nil
}

type fakeEvents struct{ topics []string }

func (f *fakeEvents) Emit(_ context.Context, topic string, _ []byte) (string, error) {
	f.topics = append(f.topics, topic)
	return "evt-1", nil
}

type fakeStream struct{ chunks [][]byte }

func (f *fakeStream) Write(_ context.Context, _ string, chunk []byte) (int, error) {
	f.chunks = append(f.chunks, chunk)
	return len(chunk), nil
}

type fakeTools struct{ names []string }

func (f *fakeTools) Register(_ context.Context, name string, _ []byte) error {
	f.names = append(f.names, name)
	return nil
}

type fakeStorage struct{ data map[string][]byte }

func newFakeStorage() *fakeStorage { return &fakeStorage{data: map[string][]byte{}} }

func (f *fakeStorage) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := f.data[key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "fakeStorage: no such key %q", key)
	}
	return v, nil
}
func (f *fakeStorage) Set(_ context.Context, key string, value []byte) error {
	f.data[key] = value
	return nil
}
func (f *fakeStorage) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for k := range f.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeStorage) Delete(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}
func (f *fakeStorage) Migrate(context.Context, []plugin.Migration) (plugin.MigrationReport, error) {
	return plugin.MigrationReport{}, nil
}

var _ plugin.Storage = (*fakeStorage)(nil)

type fakeSecrets struct{ created []string }

func (f *fakeSecrets) CreateRef(_ context.Context, name string) (string, error) {
	f.created = append(f.created, name)
	return "ref-" + name, nil
}

type fakeNet struct {
	called bool
	resp   HTTPResponse
}

func (f *fakeNet) Do(_ context.Context, _ HTTPRequest) (HTTPResponse, error) {
	f.called = true
	return f.resp, nil
}

// Erroring fakes: each corresponding Deps seam returns a typed error, to
// exercise the propagation branch in the matching host_abi_*.go handler.

type errLogger struct{}

func (errLogger) Log(context.Context, string, string) error {
	return cascade.New(cascade.KindUnavailable, "fake logger failure")
}

type errEvents struct{}

func (errEvents) Emit(context.Context, string, []byte) (string, error) {
	return "", cascade.New(cascade.KindUnavailable, "fake events failure")
}

type errStream struct{}

func (errStream) Write(context.Context, string, []byte) (int, error) {
	return 0, cascade.New(cascade.KindUnavailable, "fake stream failure")
}

type errTools struct{}

func (errTools) Register(context.Context, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "fake tools failure")
}

type errSecrets struct{}

func (errSecrets) CreateRef(context.Context, string) (string, error) {
	return "", cascade.New(cascade.KindUnavailable, "fake secrets failure")
}

type errNet struct{}

func (errNet) Do(context.Context, HTTPRequest) (HTTPResponse, error) {
	return HTTPResponse{}, cascade.New(cascade.KindUnavailable, "fake net failure")
}

func testDeps() Deps {
	return Deps{
		Logger:  &fakeLogger{},
		Events:  &fakeEvents{},
		Stream:  &fakeStream{},
		Tools:   &fakeTools{},
		Storage: newFakeStorage(),
		Secrets: &fakeSecrets{},
		Net:     &fakeNet{},
	}
}

func testLimits() ResourceLimits {
	return ResourceLimits{MemoryPages: 4, FuelBudget: 1000, WallClock: 2 * time.Second}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// testMemoryLimitPages is the generous default runtime-wide memory
// ceiling most tests run under (64 pages = 4MiB); the memory-ceiling
// exhaustion test builds its own tightly-capped Runtime instead.
const testMemoryLimitPages = 64

func mustNewRuntime(ctx context.Context, t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewRuntime(ctx, testMemoryLimitPages)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	return rt
}

func TestNewRuntime(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	if rt.rt == nil {
		t.Fatal("expected non-nil underlying wazero runtime")
	}
	if HostABIVersion() != HostABIVersionV1 {
		t.Fatalf("HostABIVersion() = %d, want %d", HostABIVersion(), HostABIVersionV1)
	}
}

// TestLoadModule_RealWASMFixture proves the runtime loads a REAL WASM
// binary (fixture.wasm, provenance in testdata/README.md — not a
// self-authored Go stub, Art.2), instantiates it, and executes a real
// call across the WebAssembly boundary that returns a structured result.
func TestLoadModule_RealWASMFixture(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "fixture", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var resp LogResponse
	err = rt.Dispatch(ctx, lm, "call_host_log", "plugin-a", nil, testDeps(), WASIConfig{}, testLimits(),
		LogRequest{Level: "info", Message: "hello from a real wasm call"}, &resp)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !resp.Accepted {
		t.Fatal("expected LogResponse.Accepted = true, a real structured result")
	}
}

func TestABIVersionMismatch_HardError(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	_, err := rt.Loader().Load(ctx, "mismatch", readFixture(t, "abi_mismatch.wasm"))
	if err == nil {
		t.Fatal("expected a hard error loading an ABI-version-mismatched module")
	}
}

func TestDispatch_NilLoadedModule(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	err := rt.Dispatch(ctx, nil, "call_host_log", "p", nil, testDeps(), WASIConfig{}, testLimits(), LogRequest{}, nil)
	if err == nil {
		t.Fatal("expected error for nil loaded module")
	}
}

func TestDispatch_UnknownExport(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "fixture2", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = rt.Dispatch(ctx, lm, "does_not_exist", "p", nil, testDeps(), WASIConfig{}, testLimits(), LogRequest{}, nil)
	if err == nil {
		t.Fatal("expected error calling an unknown export")
	}
}
