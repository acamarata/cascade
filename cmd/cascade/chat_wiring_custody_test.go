package main

// Purpose: the guard on chat wiring's SECRET CUSTODY seam. wireChatHandlers
//   used to select the custody itself, so the one test that ran it wrote a
//   probe item into the operator's real macOS login keychain on every run --
//   and internal/build's TestNoTestReachesTheRealKeychain could not see it,
//   because the SelectCustody call was in non-test code two indirections
//   away. These tests pin the repair from both sides: the wiring uses the
//   custody it is GIVEN (a real vault write lands in it), and nothing on
//   that path invokes /usr/bin/security at all.
// SPORT: cmd/cascade chat custody injection (ADD) -- P1-E20-W5-S44-T1.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// recordingRunner stands in for the external-program runner every platform
// custody backend reaches its store through. It records what was invoked so
// a test can assert that NOTHING was, and returns a resolvable keychain path
// for the one query that asks for one.
type recordingRunner struct {
	mu       sync.Mutex
	calls    []string
	keychain string
}

func (r *recordingRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, filepath.Base(name)+" "+strings.Join(args[:min(1, len(args))], " "))
	if len(args) > 0 && args[0] == "default-keychain" {
		return []byte(r.keychain + "\n"), nil
	}
	return nil, nil
}

func (r *recordingRunner) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// testChatCustody builds the custody every test in this package passes to
// wireChatHandlers: the encrypted file vault over a temp dir, forced
// (ForceFileVault) and belt-and-suspendered with an injected Runner, so no
// run of this package can reach a platform keychain. A nil rec gets a
// recorder nobody reads.
func testChatCustody(t *testing.T, rec *recordingRunner) secrets.Custody {
	t.Helper()
	if rec == nil {
		rec = &recordingRunner{}
	}
	custody, err := secrets.SelectCustody(secrets.Config{
		Service:        "cascade-chat-wiring-test",
		Dir:            t.TempDir(),
		ForceFileVault: true,
		Runner:         rec.run,
	})
	if err != nil {
		t.Fatalf("select a file-vault custody for the chat wiring: %v", err)
	}
	return custody
}

// chatRegistryForTest runs the real wiring over a throwaway data dir.
func chatRegistryForTest(t *testing.T) *rpc.Registry {
	t.Helper()
	clock := cascaderuntime.NewSystemClock()
	registry := rpc.NewRegistry()
	bus := events.New(storetest.NewMemStore(), clock)
	// shortCascadeHome, not t.TempDir(): wireChatHandlers holds its sqlite
	// handle for the daemon process's lifetime by design (see its header),
	// and Windows will not delete an open file — t.TempDir()'s cleanup
	// FAILED THIS TEST on the windows/amd64 lane of run 35515465618, the
	// first run in which that lane compiled at all. This helper's RemoveAll
	// is best-effort; the OS reclaims the directory.
	err := wireChatHandlers(context.Background(), registry, fakeDaemonPaths{root: shortCascadeHome(t)}, clock, bus,
		testChatCustody(t, nil))
	if err != nil {
		t.Fatalf("wireChatHandlers on this platform: %v", err)
	}
	return registry
}

// countingCustody records the names written through it, and delegates every
// call to the real custody underneath.
type countingCustody struct {
	secrets.Custody
	mu    sync.Mutex
	names []string
}

func (c *countingCustody) Set(ctx context.Context, name string, value []byte) error {
	c.mu.Lock()
	c.names = append(c.names, name)
	c.mu.Unlock()
	return c.Custody.Set(ctx, name, value)
}

func (c *countingCustody) written() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.names...)
}

// secretGoldenInput reads the real H/S-15.T3 detector corpus fixture rather
// than restating a credential-shaped literal in this package: the bytes come
// from internal/testdata/secrets/goldens/single_span.yaml, the same corpus
// internal/secrets/goldenfixture_test.go proves against the real detector.
func secretGoldenInput(t *testing.T) (input, canary string) {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "testdata", "secrets", "goldens", "single_span.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed in-repo fixture path
	if err != nil {
		t.Fatalf("read the detector golden corpus: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "input:"):
			input = mustUnquoteFixture(t, strings.TrimSpace(strings.TrimPrefix(line, "input:")))
		case strings.HasPrefix(trimmed, "- ") && canary == "":
			canary = mustUnquoteFixture(t, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	if input == "" || canary == "" {
		t.Fatalf("golden %s carries no input/canary", path)
	}
	return input, canary
}

func mustUnquoteFixture(t *testing.T, quoted string) string {
	t.Helper()
	out, err := strconv.Unquote(quoted)
	if err != nil {
		t.Fatalf("unquote a fixture field: %v", err)
	}
	return out
}

// TestChatWiringVaultsThroughTheInjectedCustody is the real proof: a
// chat.append_turn carrying a real detector-shaped secret, dispatched
// through the registry wireChatHandlers built, must write that secret into
// the custody this test injected -- and must invoke no external credential
// tool on the way.
//
// It fails if the wiring ever goes back to selecting its own custody: the
// injected one would then see no write at all. That is the regression
// guard. The Runner check below is weaker than it looks: the recorder
// lives inside the custody this test injected, so it proves THIS custody
// made no external call; a wiring that built its own custody would carry
// no recorder and record nothing.
func TestChatWiringVaultsThroughTheInjectedCustody(t *testing.T) {
	input, canary := secretGoldenInput(t)
	rec := &recordingRunner{}
	spy := &countingCustody{Custody: testChatCustody(t, rec)}
	clock := cascaderuntime.NewSystemClock()
	registry := rpc.NewRegistry()
	bus := events.New(storetest.NewMemStore(), clock)

	if err := wireChatHandlers(context.Background(), registry,
		fakeDaemonPaths{root: shortCascadeHome(t)}, clock, bus, spy); err != nil {
		t.Fatalf("wireChatHandlers: %v", err)
	}

	params, err := json.Marshal(map[string]any{
		"thread_id": "th-scrub-custody",
		"role":      "user",
		"segments":  []map[string]string{{"kind": "text", "content": input}},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req, parseErr := rpc.Parse([]byte(`{"jsonrpc":"2.0","id":1,"method":"chat.append_turn","params":` + string(params) + `}`))
	if parseErr != nil {
		t.Fatalf("build the append request: %+v", parseErr)
	}
	if _, errObj := registry.Dispatch(context.Background(), req); errObj != nil {
		t.Fatalf("chat.append_turn over the real wiring: %+v", errObj)
	}

	written := spy.written()
	if len(written) == 0 {
		t.Fatal("the injected custody received no write; the wiring vaulted the turn's secret somewhere else")
	}
	if written[0] != "OPENAI_API_KEY" {
		t.Fatalf("vault wrote %q, want the detector's suggested name OPENAI_API_KEY", written[0])
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Fatalf("the injected custody invoked an external credential tool: %v", calls)
	}
	if strings.Contains(strings.Join(written, " "), canary) {
		t.Fatal("a vault NAME carries the secret value")
	}
}

// TestSelectingCustodyHereWouldReachTheSecurityTool is why the parameter
// exists, as a test rather than as a comment: the call shape this wiring
// used to make -- a service label and a directory, no ForceFileVault --
// resolves the platform backend and probes it by WRITING, which on darwin
// means /usr/bin/security add-generic-password against the operator's own
// keychain. The recorder stands in for the tool here, so this test proves
// the invocation without making it.
func TestSelectingCustodyHereWouldReachTheSecurityTool(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("/usr/bin/security is the darwin backend's dependency")
	}
	dir := t.TempDir()
	fakeKeychain := filepath.Join(dir, "login.keychain-db")
	if err := os.WriteFile(fakeKeychain, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed a resolvable keychain path: %v", err)
	}
	rec := &recordingRunner{keychain: fakeKeychain}
	if _, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-chat-wiring-old-shape",
		Dir:     dir,
		Runner:  rec.run,
	}); err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	joined := strings.Join(rec.recorded(), "\n")
	for _, want := range []string{"add-generic-password", "delete-generic-password"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the old call shape did not invoke %s; recorded: %v", want, rec.recorded())
		}
	}
}
