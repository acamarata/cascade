package conversation

// Purpose: real-RPC-path proof for scrub.go -- every test here drives
//   registry.Dispatch (adapter_test.go's own dispatch helper), never a
//   bare call to ScrubTurn, and every collaborator is the real
//   internal/secrets component over a real t.TempDir() custody/ledger
//   (ForceFileVault so the suite never touches a live OS keychain). The
//   detector, quarantine store, broker and rewriter are never replaced by
//   a stand-in here -- R-14.283. (Two things in this package's tests ARE
//   fakes and say so: fakeBus, sse_test.go's recording event bus, and
//   scrub_error_test.go's single rewriter double, which exists because the
//   real rewriter cannot be made to leave a span behind.)
// SPORT: internal.conversation.scrub/ADDED (tests) (P1-E20-W5-S44-T1).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
)

// scrubTestService is the vault service label every test custody here is
// built under: never the operator's own, so nothing in this package can
// read or write a real vault entry.
const scrubTestService = "cascade-scrub-test"

// noRealKeychainRunner is the failing Runner internal/build's own
// TestNoTestReachesTheRealKeychain (R-14.206) requires on every test
// SelectCustody call: a Runner that always errors is what makes
// platformCustody report unavailable, so this is what actually keeps a
// run on a host with a login keychain from writing into it -- belt and
// suspenders alongside ForceFileVault below. Matches
// internal/mcp/firewall_test.go's tempVault's identical pattern.
func noRealKeychainRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("scrub_test.go: no platform keychain in this test")
}

// alwaysAllowGate is TEST-ONLY verification tooling: it lets a test read
// back what a real Broker.Set actually wrote, over the SAME real custody
// the pipeline used. It never appears in the pipeline itself -- the
// production wiring passes nil, matching
// cmd/cascade/daemon_unix_conductor_security.go's identical precedent
// that Set/Exists need no gate at all.
type alwaysAllowGate struct{}

func (alwaysAllowGate) Authorize(context.Context, string) error { return nil }

// newScrubTestCustody selects the encrypted file vault over dir. Forced
// (ForceFileVault) AND given a failing Runner, so neither the selector's
// platform branch nor a backend probe can reach a real keychain.
func newScrubTestCustody(t testing.TB, dir string) secrets.Custody {
	t.Helper()
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: scrubTestService, Dir: dir, ForceFileVault: true, Runner: noRealKeychainRunner,
	})
	if err != nil {
		t.Fatalf("select custody: %v", err)
	}
	return custody
}

// newTestScrubPipeline builds a real Detector/QuarantineStore/Broker/
// Rewriter pipeline over a fresh t.TempDir().
func newTestScrubPipeline(t testing.TB, bus EventBus) (pipeline ScrubPipeline, quarantine *secrets.QuarantineStore, dir string) {
	t.Helper()
	dir = t.TempDir()
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	quarantine, err = secrets.NewQuarantineStore(filepath.Join(dir, scrubQuarantineSubdir), newAdapterTestClock())
	if err != nil {
		t.Fatalf("new quarantine store: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	return NewDefaultScrubPipelineFrom(detector, quarantine, broker, secrets.NewRewriter(), bus), quarantine, dir
}

// newScrubAdapter wires pipeline into a real Adapter over a real store and
// returns the registry a test dispatches against.
func newScrubAdapter(t testing.TB, store Store, bus EventBus, pipeline ScrubPipeline) *rpc.Registry {
	t.Helper()
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	adapter.SetScrub(pipeline)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	return registry
}

// appendOneSegment dispatches a real chat.append_turn carrying a single
// text segment.
func appendOneSegment(t *testing.T, registry *rpc.Registry, threadID, content string) (any, *rpc.ErrorObject) {
	t.Helper()
	return dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: threadID, Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: content}},
	})
}

// TestNewDefaultScrubPipelineOverVault_Constructs covers the composition-
// root constructor: given an already-selected real vault (over a safe
// ForceFileVault + failing-Runner custody), it builds a working pipeline
// and that pipeline actually scrubs. Selecting the custody is
// cmd/cascade/chat_wiring.go's job and is tested there
// (chat_wiring_custody_test.go) -- this package has no SelectCustody call
// in production code at all any more.
func TestNewDefaultScrubPipelineOverVault_Constructs(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	dir := t.TempDir()
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	pipeline, err := NewDefaultScrubPipelineOverVault(dir, newAdapterTestClock(), broker, &fakeBus{})
	if err != nil {
		t.Fatalf("NewDefaultScrubPipelineOverVault: %v", err)
	}
	out, err := pipeline.ScrubTurn(context.Background(), []ScrubSegment{{Ref: "seg-1", Content: []byte(golden.Input)}})
	if err != nil {
		t.Fatalf("ScrubTurn: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("ScrubTurn returned %d segments for 1 input", len(out))
	}
	assertGoldenOutput(t, golden, string(out[0]))
}

// TestNewDefaultScrubPipelineOverVault_QuarantineStoreFails proves the
// constructor's own fail-closed path: pointing dataDir at a plain FILE
// (not a directory) makes secrets.NewQuarantineStore's real
// os.MkdirAll(dataDir/quarantine) fail (ENOTDIR), a real refusal
// propagated rather than swallowed.
func TestNewDefaultScrubPipelineOverVault_QuarantineStoreFails(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed a plain file: %v", err)
	}
	if _, err := NewDefaultScrubPipelineOverVault(notADir, newAdapterTestClock(), failingVault{}, &fakeBus{}); err == nil {
		t.Fatal("NewDefaultScrubPipelineOverVault over a file, not a dir, succeeded; want the real NewQuarantineStore refusal")
	}
}

// verifyVaulted re-opens the SAME real custody with a test-only
// always-allow gate and returns the value stored under name -- proving
// the vault phase actually wrote the secret's bytes, not merely that
// Exists says so.
func verifyVaulted(t *testing.T, dir, name string) []byte {
	t.Helper()
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), alwaysAllowGate{})
	if err != nil {
		t.Fatalf("reopen broker: %v", err)
	}
	value, err := broker.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("vault Get(%q) after a successful scrub: %v", name, err)
	}
	return value
}

// TestScrub_RealRPCPath_DetectsVaultsRewrites is R-14.283's own proof:
// dispatch a real chat.append_turn carrying a real detector-shaped
// secret (single_span.golden, harvested from internal/secrets's own
// H/S-15.T3 golden corpus) through the real RPC registry, and assert the
// STORED segment and the SSE mirror both carry exactly the fixture's
// expected output, never the raw value -- and that the value actually
// reached the vault.
func TestScrub_RealRPCPath_DetectsVaultsRewrites(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, dir := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	result, errObj := appendOneSegment(t, registry, "th1", golden.Input)
	if errObj != nil {
		t.Fatalf("chat.append_turn errored on a scrubbable turn: %+v", errObj)
	}
	appended := result.(appendTurnResult)

	segs, err := store.ListSegments(context.Background(), appended.TurnID)
	if err != nil || len(segs) != 1 {
		t.Fatalf("ListSegments = %v, %v", segs, err)
	}
	assertGoldenOutput(t, golden, segs[0].Content)
	if len(bus.published) != 1 {
		t.Fatalf("SSE mirror published %d events, want 1", len(bus.published))
	}
	// Decode the payload rather than substring-matching it: the wire form
	// is JSON, which escapes a tag's angle brackets.
	var echoed turnAppendedPayload
	if err := json.Unmarshal(bus.published[0], &echoed); err != nil {
		t.Fatalf("SSE payload does not decode: %v", err)
	}
	if len(echoed.Segments) != 1 {
		t.Fatalf("SSE payload carries %d segments, want 1", len(echoed.Segments))
	}
	assertGoldenOutput(t, golden, echoed.Segments[0].Content)
	assertNoCanary(t, "SSE payload", string(bus.published[0]), golden.Canaries)

	if got := string(verifyVaulted(t, dir, "OPENAI_API_KEY")); got != golden.Canaries[0] {
		t.Fatalf("vaulted value = %q, want the original secret %q", got, golden.Canaries[0])
	}
}

// TestScrub_RealRPCPath_MultiSpan is the same proof over the three-span
// fixture: three classes, three tag types, three vault entries, and the
// stored segment equal to the corpus's own expected_output.
func TestScrub_RealRPCPath_MultiSpan(t *testing.T) {
	golden := loadScrubGolden(t, "multi_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, dir := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	result, errObj := appendOneSegment(t, registry, "th1", golden.Input)
	if errObj != nil {
		t.Fatalf("chat.append_turn errored on the three-span turn: %+v", errObj)
	}
	segs, err := store.ListSegments(context.Background(), result.(appendTurnResult).TurnID)
	if err != nil || len(segs) != 1 {
		t.Fatalf("ListSegments = %v, %v", segs, err)
	}
	assertGoldenOutput(t, golden, segs[0].Content)

	for i, name := range []string{"OPENAI_API_KEY", "AUTHORIZATION_HEADER_TOKEN", "CONNECTION_STRING"} {
		if got := string(verifyVaulted(t, dir, name)); got != golden.Canaries[i] {
			t.Fatalf("vaulted %s = %q, want canary %d", name, got, i)
		}
	}
}

// TestScrubNoSecrets_Passthrough proves a clean turn is untouched: no
// quarantine entry, no vault write, content byte-identical.
func TestScrubNoSecrets_Passthrough(t *testing.T) {
	const clean = "hello, nothing sensitive here"
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, quarantine, _ := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	result, errObj := appendOneSegment(t, registry, "th1", clean)
	if errObj != nil {
		t.Fatalf("chat.append_turn errored on a clean turn: %+v", errObj)
	}
	segs, _ := store.ListSegments(context.Background(), result.(appendTurnResult).TurnID)
	if len(segs) != 1 || segs[0].Content != clean {
		t.Fatalf("clean turn was altered: %+v", segs)
	}
	pending, err := quarantine.List()
	if err != nil || len(pending) != 0 {
		t.Fatalf("quarantine.List() = %v, %v, want empty for a clean turn", pending, err)
	}
}
