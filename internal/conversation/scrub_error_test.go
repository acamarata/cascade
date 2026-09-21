package conversation

// Purpose: scrub.go's ordering proof, the two collaborator-failure
//   refusals (the quarantine ledger's own write, a vault refusal), and the
//   shared divergence assertion every refusal test uses. The remaining
//   refusals live in scrub_refusal_test.go; TestScrubErrorPath below
//   registers them all. Every refusal is checked for BOTH halves of its
//   contract: nothing stored, and a divergence event on the right topic
//   naming the phase. Split from scrub_test.go under Art.10.3's 300-line
//   file cap.
// SPORT: internal.conversation.scrub/ADDED (tests) (P1-E20-W5-S44-T1).

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// orderSpyStore/orderSpyBus/orderSpyScrub each append their own tag to a
// shared order log before delegating to the collaborator they wrap -- the
// instrumentation TestScrubOrdering needs to prove scrub runs before
// EITHER of the two downstream consumers this tree has today
// (Store.AppendTurn, the SSE mirror's Publish). Each spy calls through:
// the store underneath is the real sqlite store and the pipeline
// underneath is the real one.
type orderSpyStore struct {
	Store
	order *[]string
}

func (s orderSpyStore) AppendTurn(ctx context.Context, turn Turn) error {
	*s.order = append(*s.order, "store")
	return s.Store.AppendTurn(ctx, turn)
}

type orderSpyBus struct {
	EventBus
	order *[]string
}

func (b orderSpyBus) Publish(ctx context.Context, ns string, kind events.EventKind, src string, payload []byte) (events.Event, error) {
	*b.order = append(*b.order, "sse")
	return b.EventBus.Publish(ctx, ns, kind, src, payload)
}

type orderSpyScrub struct {
	ScrubPipeline
	order *[]string
}

func (p orderSpyScrub) ScrubTurn(ctx context.Context, segs []ScrubSegment) ([][]byte, error) {
	*p.order = append(*p.order, "scrub")
	return p.ScrubPipeline.ScrubTurn(ctx, segs)
}

// assertDivergence asserts bus carries exactly one divergence event, on
// the security namespace and scrub kind, whose payload names phase and
// carries a non-empty reason -- and no turn content.
func assertDivergence(t *testing.T, bus *fakeBus, phase string, canaries []string) {
	t.Helper()
	if len(bus.events) != 1 {
		t.Fatalf("published %d events, want exactly 1 (the divergence event)", len(bus.events))
	}
	ev := bus.events[0]
	if ev.Namespace != divergenceNamespace || ev.Kind != divergenceKind {
		t.Fatalf("divergence event published on %s/%s, want %s/%s",
			ev.Namespace, ev.Kind, divergenceNamespace, divergenceKind)
	}
	var payload scrubDivergencePayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("divergence payload does not decode: %v", err)
	}
	if payload.Phase != phase {
		t.Fatalf("divergence payload phase = %q, want %q", payload.Phase, phase)
	}
	if payload.Reason == "" {
		t.Fatal("divergence payload carries no reason")
	}
	assertNoCanary(t, "divergence payload", string(ev.Payload), canaries)
}

// TestScrubOrdering proves scrub completes before Store's real commit and
// before the SSE mirror's real publish -- the contract's "synchronously
// BEFORE the SSE mirror emits and BEFORE the segmenter handoff". No
// production segmenter caller exists in this tree yet (verified: no
// import of internal/conversation/topics from this package or
// cmd/cascade); stubSegmenterCh stands in for that future subscriber,
// fed from the exact bytes Store received, so it can only ever observe
// post-scrub content -- proof by construction, not by a fake call site.
func TestScrubOrdering(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, _ := newTestScrubPipeline(t, bus)
	var order []string
	registry := newScrubAdapter(t, orderSpyStore{Store: store, order: &order},
		orderSpyBus{EventBus: bus, order: &order}, orderSpyScrub{ScrubPipeline: pipeline, order: &order})

	result, errObj := appendOneSegment(t, registry, "th1", golden.Input)
	if errObj != nil {
		t.Fatalf("chat.append_turn errored: %+v", errObj)
	}
	appended := result.(appendTurnResult)

	stubSegmenterCh := make(chan string, 4)
	segs, _ := store.ListSegments(context.Background(), appended.TurnID)
	for _, s := range segs {
		stubSegmenterCh <- s.Content
	}
	close(stubSegmenterCh)
	for content := range stubSegmenterCh {
		assertNoCanary(t, "segmenter-stub", content, golden.Canaries)
	}

	want := []string{"scrub", "store", "sse"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v (scrub must run first)", order, want)
		}
	}
}

// failingVault always refuses Set -- the vault phase's collaborator-
// failure injection, matching sse_test.go's fakeBus{failWith:...}
// precedent. It never stands in for the detector, quarantine store or
// rewriter, all of which stay real in TestScrubErrorPath/BrokerError.
type failingVault struct{}

func (failingVault) Set(context.Context, string, []byte, secrets.SetMode) (secrets.SetResult, error) {
	return secrets.SetResult{}, cascade.New(cascade.KindUnavailable, "test: vault refuses every write")
}

// TestScrubErrorPath covers the fail-closed refusals, one per phase.
func TestScrubErrorPath(t *testing.T) {
	t.Run("QuarantineWriteError", testScrubQuarantineWriteError)
	t.Run("BrokerError", testScrubBrokerError)
	t.Run("ResidualMatch", testScrubResidualMatch)
	t.Run("RewriteRefusal", testScrubRewriteRefusal)
	t.Run("StraddlesSegments", testScrubStraddlesSegments)
}

// testScrubQuarantineWriteError is the quarantine phase's real failure
// mode: the real Detector.Scan/ScanCertain (internal/secrets/detector.go)
// are pure, total functions with no error return (verified by reading the
// shipped code, not assumed) -- so this proves the fail-closed behaviour
// over the real I/O failure that phase CAN exhibit, its own ledger write,
// rather than a "detector error" the real component cannot produce. See
// scrub.go's FALSE PREMISE doc comment.
func testScrubQuarantineWriteError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-bit refusal needs a non-root unix process")
	}
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, dir := newTestScrubPipeline(t, bus)
	qdir := dir + "/" + scrubQuarantineSubdir
	if err := os.Chmod(qdir, 0o500); err != nil {
		t.Fatalf("chmod quarantine dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(qdir, 0o700) })

	registry := newScrubAdapter(t, store, bus, pipeline)
	if _, errObj := appendOneSegment(t, registry, "th1", golden.Input); errObj == nil {
		t.Fatal("chat.append_turn succeeded despite a quarantine-ledger write failure")
	}
	if turns, _ := store.ListTurns(context.Background(), "th1"); len(turns) != 0 {
		t.Fatalf("turn was committed despite the quarantine phase failing: %v", turns)
	}
	assertDivergence(t, bus, scrubPhaseQuarantine, golden.Canaries)
}

// testScrubBrokerError is the contract's named acceptance case: the vault
// phase fails, the turn stays quarantined, a divergence event fires,
// nothing is forwarded.
func testScrubBrokerError(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	_, quarantine, _ := newTestScrubPipeline(t, bus)
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	pipeline := NewDefaultScrubPipelineFrom(detector, quarantine, failingVault{}, secrets.NewRewriter(), bus)

	registry := newScrubAdapter(t, store, bus, pipeline)
	if _, errObj := appendOneSegment(t, registry, "th1", golden.Input); errObj == nil {
		t.Fatal("chat.append_turn succeeded despite a vault broker failure")
	}
	pending, err := quarantine.List()
	if err != nil || len(pending) != 1 {
		t.Fatalf("quarantine.List() = %v, %v, want exactly one entry STILL quarantined", pending, err)
	}
	if turns, _ := store.ListTurns(context.Background(), "th1"); len(turns) != 0 {
		t.Fatalf("turn was committed despite the vault phase failing: %v", turns)
	}
	assertDivergence(t, bus, scrubPhaseVault, golden.Canaries)
}
