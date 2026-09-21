package plugins

// Purpose (this file): coverage for the bridge composition root's real but
//   under-exercised branches (closeStateAfterStop's non-Closer/stop-error/
//   close-error paths, bridgeIssuer's IssueCode failure) plus this
//   ticket's own three new error returns, made reachable through test
//   doubles rather than left permanently dead: newBridgeSecretScanner's
//   Wrap (via newDetectorFn), enabledBridge's propagation of that same
//   failure, and EmitQuarantine's marshal error (via marshalQuarantineEvent).
//   Split from cascadepa_bridge_wiring_test.go/cascadepa_bridge_events_test.go,
//   both already at Art.10.3's 300-line cap.
//
// Inputs: none beyond this package's own test fixtures (bridgeTestRoot,
//   enabledBridgeDeps, syntheticBotToken, newRecordingBus — all defined
//   alongside the files this one covers).
//
// Outputs: no production behavior change — every case here drives an
//   EXISTING branch through an existing seam or a narrow, already-declared
//   test hook (randRead/newDetectorFn/marshalQuarantineEvent follow the
//   same indirection pattern this tree already uses throughout).
//
// Constraints: NO REAL KEYCHAIN, NO REAL HOME, NO NETWORK — identical to
//   this package's other bridge test files (see
//   cascadepa_bridge_fixtures_test.go's header for why).
//
// SPORT: internal/plugins:cascadepa-bridge-coverage/TEST (P1-E23-W5-S48-T3).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// fakeNonCloserState is a cascadepa.BridgeState that does NOT implement
// io.Closer, so closeStateAfterStop's `!ok` branch — untouched by
// production, which always opens the real, closeable adapter — has a
// non-production caller too.
type fakeNonCloserState struct{}

func (fakeNonCloserState) Load(context.Context, string) (cascadepa.SubjectState, bool, error) {
	return cascadepa.SubjectState{}, false, nil
}

func (fakeNonCloserState) Save(context.Context, cascadepa.SubjectState) error { return nil }

// fakeCloserState is a cascadepa.BridgeState that DOES implement io.Closer,
// with an injectable Close outcome, so closeStateAfterStop's stop-error and
// close-error branches are each independently reachable.
type fakeCloserState struct {
	fakeNonCloserState
	closeErr error
	closed   int
}

func (f *fakeCloserState) Close() error {
	f.closed++
	return f.closeErr
}

// TestCloseStateAfterStop_NonCloserStateCallsStopUnwrapped: a state that is
// not an io.Closer makes closeStateAfterStop return stop itself, unchanged.
func TestCloseStateAfterStop_NonCloserStateCallsStopUnwrapped(t *testing.T) {
	called := false
	stop := func(context.Context) error { called = true; return nil }
	wrapped := closeStateAfterStop(stop, fakeNonCloserState{})
	if err := wrapped(context.Background()); err != nil {
		t.Fatalf("wrapped stop: %v", err)
	}
	if !called {
		t.Fatal("stop was not invoked for a non-io.Closer state")
	}
}

// TestCloseStateAfterStop_StopErrorWinsOverCloseSuccess: a failing stop is
// reported even though Close is still called and succeeds.
func TestCloseStateAfterStop_StopErrorWinsOverCloseSuccess(t *testing.T) {
	wantErr := errors.New("test: stop failed")
	stop := func(context.Context) error { return wantErr }
	state := &fakeCloserState{}
	wrapped := closeStateAfterStop(stop, state)
	if err := wrapped(context.Background()); err != wantErr {
		t.Fatalf("wrapped stop err = %v, want %v", err, wantErr)
	}
	if state.closed != 1 {
		t.Fatalf("Close called %d times, want 1 (drain always closes)", state.closed)
	}
}

// TestCloseStateAfterStop_CloseErrorSurfacesWhenStopSucceeds: with stop
// clean, a Close failure is what the caller sees.
func TestCloseStateAfterStop_CloseErrorSurfacesWhenStopSucceeds(t *testing.T) {
	wantErr := errors.New("test: close failed")
	stop := func(context.Context) error { return nil }
	state := &fakeCloserState{closeErr: wantErr}
	wrapped := closeStateAfterStop(stop, state)
	if err := wrapped(context.Background()); err != wantErr {
		t.Fatalf("wrapped stop err = %v, want %v", err, wantErr)
	}
}

// TestBridgeIssuer_PropagatesIssueCodeFailure: bridgeIssuer's own error
// return (untested by the happy-path issuance tests in
// cascadepa_bridge_wiring_test.go) is exercised by handing it a
// PairCodeStore built over a nil BridgeState — cascadepa.NewPairCodeStore
// substitutes its own fail-closed default, so IssueCode's Load fails and
// the error must reach the caller rather than being swallowed.
func TestBridgeIssuer_PropagatesIssueCodeFailure(t *testing.T) {
	clock := runtime.NewSystemClock()
	key, err := cascadepa.DerivePairCodeKey(syntheticBotToken)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	stores := &cascadepa.Stores{Pairing: cascadepa.NewPairCodeStore(clock, nil, key)}
	issuer := bridgeIssuer(stores, clock, "tg-coverage-subject")
	if _, err := issuer(context.Background(), ""); err == nil {
		t.Fatal("bridgeIssuer swallowed an IssueCode failure over an unconfigured store")
	}
}

// TestNewCascadePABridge_PropagatesSecretScannerFailure exercises TWO of
// this ticket's three new, otherwise-dead error returns in one seam:
// newBridgeSecretScanner's Wrap (secrets.NewDetector never fails for the
// real DefaultRegistry/DefaultDetectionConfig, so it needs newDetectorFn's
// test hook) and enabledBridge's own propagation of that failure.
func TestNewCascadePABridge_PropagatesSecretScannerFailure(t *testing.T) {
	deps, _, _ := enabledBridgeDeps(t)
	orig := newDetectorFn
	newDetectorFn = func(secrets.Registry, secrets.DetectionConfig) (*secrets.Detector, error) {
		return nil, errors.New("test: detector misconfigured")
	}
	defer func() { newDetectorFn = orig }()
	if _, err := NewCascadePABridge(context.Background(), deps); err == nil {
		t.Fatal("a broken secret-value detector was not reported by NewCascadePABridge")
	}
}

// TestBridgeJournal_QuarantineMarshalFailureDoesNotPublish exercises this
// ticket's third new error return: EmitQuarantine's json.Marshal branch,
// unreachable for a real QuarantineEvent (plain strings and a time.Time
// cannot fail to encode), so marshalQuarantineEvent is the test hook.
func TestBridgeJournal_QuarantineMarshalFailureDoesNotPublish(t *testing.T) {
	bus := newRecordingBus()
	orig := marshalQuarantineEvent
	marshalQuarantineEvent = func(any) ([]byte, error) { return nil, errors.New("test: marshal failed") }
	defer func() { marshalQuarantineEvent = orig }()
	newBridgeJournal(bus).EmitQuarantine(context.Background(), telegram.QuarantineEvent{})
	if len(bus.events()) != 0 {
		t.Fatal("a marshal failure still published a record")
	}
}
