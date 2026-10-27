//go:build !windows

package daemon

// Purpose (this file): the presence tracker over the REAL elevation
//   middleware, in the production order (elevation outer, presence inner), with
//   an in-memory test key. Every "does not count" case is run against the same
//   chain as the "counts" case, so a tracker that counted anything would fail
//   the case that names it. No host keystore, keychain or operator HOME is
//   touched; the data directory is a t.TempDir().

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// presenceStepClock is a Clock the tests move by hand; unlike FixedClock it
// is safe for the concurrent test.
type presenceStepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *presenceStepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *presenceStepClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// presenceRig is elevation (outer) -> presence (inner) -> a handler, the
// order the daemon registers them in.
type presenceRig struct {
	f       *elevFixture
	clock   *presenceStepClock
	tracker *PresenceTracker
	elev    rpc.MiddlewareFunc
	by      rpc.WitnessVerifier // the bound elevation issuer
	t       *testing.T
}

func newPresenceRig(t *testing.T) *presenceRig {
	t.Helper()
	f := newElevFixture(t)
	f.enroll(t, f.ks)
	mw, status := NewElevationMiddleware(f.dir, selectorOver(f.dir, elevation.CustodyPlatform, f.ks), f.log, f.clock)
	if !status.Bound {
		t.Fatalf("test custody not bound: %+v", status)
	}
	clock := &presenceStepClock{now: time.Unix(5000, 0)}
	tracker := NewPresenceTracker(clock)
	tracker.Bind(status.Verifier)
	return &presenceRig{f: f, clock: clock, tracker: tracker, elev: mw, by: status.Verifier, t: t}
}

// chain wraps handler for method in the production order.
func (r *presenceRig) chain(method string, handler rpc.HandlerFunc) rpc.HandlerFunc {
	return r.elev(method, PresenceMiddleware(r.tracker)(method, handler))
}

// attested runs the challenge then the signed retry for plugin.add (an
// elevated verb) over args and returns the final result and error.
func (r *presenceRig) attested(h rpc.HandlerFunc, args string) (any, error) {
	r.t.Helper()
	wrapped := r.chain("plugin.add", h)
	_, err := wrapped(context.Background(), json.RawMessage(args))
	nonce := challengeNonce(r.t, err)
	if !r.tracker.LastActivity().IsZero() {
		r.t.Fatal("an ELEVATION_REQUIRED challenge advanced presence")
	}
	return wrapped(context.Background(), r.f.attestedEnvelope(r.t, r.f.ks, args, nonce))
}

func okHandler(context.Context, json.RawMessage) (any, error) { return "ok", nil }

func TestPresenceTrackerCountsAttestedCall(t *testing.T) {
	r := newPresenceRig(t)
	start := r.clock.Now()
	var seenDuring time.Time
	handler := func(context.Context, json.RawMessage) (any, error) {
		seenDuring = r.tracker.LastActivity()
		r.clock.set(start.Add(7 * time.Second)) // the call takes seven seconds
		return "ran", nil
	}
	if _, err := r.attested(handler, `{"manifest":"x"}`); err != nil {
		t.Fatalf("attested call: %v", err)
	}
	if !seenDuring.IsZero() {
		t.Fatalf("presence was recorded before the handler returned: %v", seenDuring)
	}
	want := start.Add(7 * time.Second)
	if got := r.tracker.LastActivity(); !got.Equal(want) {
		t.Fatalf("LastActivity = %v, want %v (the clock AFTER the call)", got, want)
	}
}

func TestPresenceIgnoresUnattestedInboxRead(t *testing.T) {
	r := newPresenceRig(t)
	for _, method := range []string{"inbox.read", "inbox.ack", "status.get", "doctor.run", "plugin.list"} {
		res, err := r.chain(method, okHandler)(context.Background(), json.RawMessage(`{}`))
		if err != nil || res != "ok" {
			t.Fatalf("%s: res=%v err=%v, want the call to succeed", method, res, err)
		}
	}
	if got := r.tracker.LastActivity(); !got.IsZero() {
		t.Fatalf("unattested successful calls advanced presence to %v", got)
	}
	// A client-supplied envelope on a verb that needs no elevation is not a
	// witness: nothing verified it.
	forged := json.RawMessage(`{"_attestation":{"nonce":"n","sig":"s"},"_args":{}}`)
	if _, err := r.chain("inbox.read", okHandler)(context.Background(), forged); err != nil {
		t.Fatalf("envelope on a non-elevated verb: %v", err)
	}
	if got := r.tracker.LastActivity(); !got.IsZero() {
		t.Fatalf("a forged envelope advanced presence to %v", got)
	}
}

func TestPresenceIgnoresAgentAppendTurn(t *testing.T) {
	r := newPresenceRig(t)
	// An agent turn append is a plain call: no challenge, no witness.
	if _, err := r.chain("chat.append_turn", okHandler)(context.Background(), json.RawMessage(`{"role":"agent"}`)); err != nil {
		t.Fatal(err)
	}
	// Even the context of a genuinely attested call does not carry presence
	// to a call for another verb or other params: capture the witness context
	// of one attested call and replay it.
	var captured context.Context
	if _, err := r.attested(func(ctx context.Context, _ json.RawMessage) (any, error) { captured = ctx; return "ran", nil }, `{"manifest":"x"}`); err != nil {
		t.Fatal(err)
	}
	before := r.tracker.LastActivity()
	if before.IsZero() {
		t.Fatal("setup: the attested call did not count")
	}
	r.clock.set(r.clock.Now().Add(time.Hour))
	pm := PresenceMiddleware(r.tracker)
	if _, err := pm("chat.append_turn", okHandler)(captured, json.RawMessage(`{"manifest":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := pm("plugin.add", okHandler)(captured, json.RawMessage(`{"manifest":"other"}`)); err != nil {
		t.Fatal(err)
	}
	if got := r.tracker.LastActivity(); !got.Equal(before) {
		t.Fatalf("a witness for another verb or other params advanced presence: %v -> %v", before, got)
	}
}

func TestPresenceTrackerIgnoresFailedCalls(t *testing.T) {
	r := newPresenceRig(t)
	boom := errors.New("boom")
	if _, err := r.attested(func(context.Context, json.RawMessage) (any, error) { return "partial", boom }, `{"manifest":"x"}`); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the handler's error returned unchanged", err)
	}
	if got := r.tracker.LastActivity(); !got.IsZero() {
		t.Fatalf("a failed attested call advanced presence to %v", got)
	}
	// A refused attestation (replayed nonce) never reaches the handler.
	wrapped := r.chain("plugin.add", okHandler)
	const args = `{"manifest":"y"}`
	_, err := wrapped(context.Background(), json.RawMessage(args))
	nonce := challengeNonce(t, err)
	env := r.f.attestedEnvelope(t, r.f.ks, args, nonce)
	if _, err := wrapped(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	counted := r.tracker.LastActivity()
	r.clock.set(r.clock.Now().Add(time.Minute))
	if _, err := wrapped(context.Background(), env); err == nil {
		t.Fatal("a replayed attestation ran")
	}
	if got := r.tracker.LastActivity(); !got.Equal(counted) {
		t.Fatalf("a refused replay moved presence: %v -> %v", counted, got)
	}
}

func TestPresenceTrackerZeroUntilFirstInput(t *testing.T) {
	r := newPresenceRig(t)
	if got := r.tracker.LastActivity(); !got.IsZero() {
		t.Fatalf("fresh tracker reports %v, want the zero time", got)
	}
	var nilTracker *PresenceTracker
	if !nilTracker.LastActivity().IsZero() {
		t.Fatal("a nil tracker reports activity")
	}
	// A tracker with no clock, or whose clock reports the zero time, cannot
	// invent an instant: it stays at "never".
	for name, tr := range map[string]*PresenceTracker{
		"nil clock":  NewPresenceTracker(nil),
		"zero clock": NewPresenceTracker(runtime.NewFixedClock(time.Time{})),
		"pre-epoch":  NewPresenceTracker(runtime.NewFixedClock(time.Unix(-5, 0))),
	} {
		rig := *r
		tr.Bind(r.by) // bound, so only the clock stops it from counting
		rig.tracker = tr
		if _, err := rig.attested(okHandler, `{"manifest":"z"}`); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := tr.LastActivity(); !got.IsZero() {
			t.Fatalf("%s recorded %v", name, got)
		}
	}
	// A nil tracker mounts as a pass-through, not a panic.
	if res, err := PresenceMiddleware(nil)("inbox.read", okHandler)(context.Background(), nil); err != nil || res != "ok" {
		t.Fatalf("nil tracker middleware: res=%v err=%v", res, err)
	}
}

func TestPresenceNeverMovesBackward(t *testing.T) {
	r := newPresenceRig(t)
	var captured context.Context
	if _, err := r.attested(func(ctx context.Context, _ json.RawMessage) (any, error) { captured = ctx; return "ran", nil }, `{"manifest":"x"}`); err != nil {
		t.Fatal(err)
	}
	high := r.clock.Now().Add(time.Hour)
	r.clock.set(high)
	pm := PresenceMiddleware(r.tracker)("plugin.add", okHandler)
	if _, err := pm(captured, json.RawMessage(`{"manifest":"x"}`)); err != nil {
		t.Fatal(err)
	}
	r.clock.set(high.Add(-30 * time.Minute)) // a clock step backward
	if _, err := pm(captured, json.RawMessage(`{"manifest":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got := r.tracker.LastActivity(); !got.Equal(high) {
		t.Fatalf("LastActivity = %v, want it held at %v", got, high)
	}
}

func TestPresenceConcurrentAttestedCalls(t *testing.T) {
	r := newPresenceRig(t)
	var captured context.Context
	if _, err := r.attested(func(ctx context.Context, _ json.RawMessage) (any, error) { captured = ctx; return "ran", nil }, `{"manifest":"x"}`); err != nil {
		t.Fatal(err)
	}
	pm := PresenceMiddleware(r.tracker)("plugin.add", okHandler)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.clock.set(time.Unix(6000+int64(i), 0))
			if _, err := pm(captured, json.RawMessage(`{"manifest":"x"}`)); err != nil {
				t.Errorf("attested call %d: %v", i, err)
			}
			if r.tracker.LastActivity().IsZero() {
				t.Errorf("call %d: LastActivity reported zero after an attested success", i)
			}
		}(i)
	}
	wg.Wait()
	if got := r.tracker.LastActivity(); got.Before(time.Unix(6000, 0)) {
		t.Fatalf("LastActivity = %v after 32 concurrent attested calls", got)
	}
}

// forgedKey is a context key declared outside internal/rpc. The real witness
// key is an unexported type in that package, so no value stored under a key
// from here can be read back by rpc.VerifiedElevationFrom.
type forgedKey struct{}

// otherForgedKey is a second foreign key type, so the test does not rely on
// one particular key shape.
type otherForgedKey struct{}

func TestPresenceIgnoresForgedWitnessShapedContext(t *testing.T) {
	r := newPresenceRig(t)
	ctx := context.WithValue(context.Background(), forgedKey{}, rpc.VerifiedElevation{})
	ctx = context.WithValue(ctx, otherForgedKey{}, rpc.VerifiedElevation{})
	pm := PresenceMiddleware(r.tracker)("plugin.add", okHandler)
	if _, err := pm(ctx, json.RawMessage(`{"manifest":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got := r.tracker.LastActivity(); !got.IsZero() {
		t.Fatalf("a forged witness-shaped context value advanced presence to %v", got)
	}
}
