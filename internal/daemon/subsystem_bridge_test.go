package daemon

// Purpose: the bridge subsystem's tests — that the poll is started only when
//   the composition root says the bridge is configured, that it OUTLIVES a
//   pairing RPC (the defect this file exists to close: the poll used to live and
//   die inside `cascade pa pair`), that Stop runs at shutdown, and that
//   pa.pair_code is bound and decodes what a client sends.
//
// Constraints: no sockets, no sqlite, no plugin import — the subsystem's whole
//   contract is bare funcs, so every test here drives it with closures and
//   observes the Manifest and the Registry.
//
// SPORT: internal/daemon:bridge-subsystem/TESTED (P1-E23-W5-S48-T1).

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// bridgeState is one test's observation of the subsystem's lifecycle.
type bridgeState struct {
	started atomic.Int64
	stopped atomic.Int64
	// stopCtxLive records whether Stop was handed a context that was still
	// usable: a drain derived from the cancelled shutdown context would expire
	// before it could drain anything.
	stopCtxLive atomic.Bool
	issued      atomic.Int64
}

// testSubsystem is a configured bridge whose Start/Stop record their calls.
func testSubsystem(st *bridgeState) BridgeSubsystem {
	return BridgeSubsystem{
		Start: func(context.Context) error { st.started.Add(1); return nil },
		Stop: func(ctx context.Context) error {
			st.stopped.Add(1)
			st.stopCtxLive.Store(ctx.Err() == nil)
			return nil
		},
		IssueCode: func(_ context.Context, subject string) (BridgePairCodeResult, error) {
			st.issued.Add(1)
			return BridgePairCodeResult{Code: "7ZQK3M9F", Subject: subject, ExpiresAt: "2026-09-21T12:10:00Z"}, nil
		},
	}
}

// findStatus returns one subsystem's snapshot row.
func findStatus(t *testing.T, m *Manifest, name string) SubsystemStatus {
	t.Helper()
	for _, row := range m.Snapshot() {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("subsystem %q was never declared; absence of a row is exactly what R-14.87 forbids", name)
	return SubsystemStatus{}
}

// TestRegisterBridgeModule_StartsThePollAndBindsTheVerb is the shipped path.
func TestRegisterBridgeModule_StartsThePollAndBindsTheVerb(t *testing.T) {
	m := NewManifest(nil, nil)
	registry := rpc.NewRegistry()
	st := &bridgeState{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.RegisterBridgeModule(ctx, registry, testSubsystem(st))

	if st.started.Load() != 1 {
		t.Fatalf("Start was called %d times, want 1", st.started.Load())
	}
	if row := findStatus(t, m, bridgeSubsystem); row.State != SubsystemRunning {
		t.Fatalf("subsystem state = %s (%q), want running", row.State, row.Detail)
	}
	if !registry.Registered(MethodBridgePairCode) {
		t.Fatalf("%s was not bound; `cascade pa pair` would get method-not-found", MethodBridgePairCode)
	}
}

// TestRegisterBridgeModule_PollOutlivesAPairingRPC is the regression that made
// this file necessary: issuance must not be what keeps the poll alive, and must
// not end it either.
func TestRegisterBridgeModule_PollOutlivesAPairingRPC(t *testing.T) {
	m := NewManifest(nil, nil)
	registry := rpc.NewRegistry()
	st := &bridgeState{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.RegisterBridgeModule(ctx, registry, testSubsystem(st))

	// Dispatched through the registry the daemon really serves, so this is the
	// same path a `cascade pa pair` request takes.
	out, rpcErr := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: MethodBridgePairCode, Params: json.RawMessage(`{"subject":"tg-abc"}`),
	})
	if rpcErr != nil {
		t.Fatalf("pa.pair_code: %+v", rpcErr)
	}
	res, isResult := out.(BridgePairCodeResult)
	if !isResult || res.Code != "7ZQK3M9F" || res.Subject != "tg-abc" {
		t.Fatalf("pa.pair_code returned %#v", out)
	}
	if st.issued.Load() != 1 {
		t.Fatalf("the composition root's issuer ran %d times, want 1", st.issued.Load())
	}
	if st.stopped.Load() != 0 {
		t.Fatal("the poll was stopped by a pairing RPC")
	}
	if row := findStatus(t, m, bridgeSubsystem); row.State != SubsystemRunning {
		t.Fatalf("after the RPC the subsystem is %s, want still running", row.State)
	}
}

// TestRegisterBridgeModule_StopsAtShutdown is the drain: cancelling the
// subsystems' context stops the poll, and Wait joins the drain rather than
// returning while it is still in flight.
func TestRegisterBridgeModule_StopsAtShutdown(t *testing.T) {
	m := NewManifest(nil, nil)
	st := &bridgeState{}
	ctx, cancel := context.WithCancel(context.Background())
	m.RegisterBridgeModule(ctx, rpc.NewRegistry(), testSubsystem(st))

	if st.stopped.Load() != 0 {
		t.Fatal("Stop ran before shutdown")
	}
	cancel()
	m.Wait()

	if st.stopped.Load() != 1 {
		t.Fatalf("Stop ran %d times at shutdown, want exactly 1; without it the poll goroutine "+
			"and its in-flight request outlive the daemon", st.stopped.Load())
	}
	if !st.stopCtxLive.Load() {
		t.Fatal("Stop was handed an already-cancelled context, so its drain could not wait for anything")
	}
}

// TestRegisterBridgeModule_DisabledIsLoudAndStillIssues: the shipped state. No
// poll, a reason an operator can read, and pa.pair_code still ANSWERS.
func TestRegisterBridgeModule_DisabledIsLoudAndStillIssues(t *testing.T) {
	m := NewManifest(nil, nil)
	registry := rpc.NewRegistry()
	issued := 0
	m.RegisterBridgeModule(context.Background(), registry, BridgeSubsystem{
		DisabledReason: "the telegram module is disabled in the cascade-pa module manifest",
		IssueCode: func(context.Context, string) (BridgePairCodeResult, error) {
			issued++
			return BridgePairCodeResult{}, cascade.New(cascade.KindUnavailable, "no bridge module is enabled")
		},
	})
	row := findStatus(t, m, bridgeSubsystem)
	if row.State != SubsystemDisabled {
		t.Fatalf("state = %s, want disabled", row.State)
	}
	if row.Detail == "" {
		t.Fatal("a disabled subsystem with no reason is the silence R-14.87 forbids")
	}
	if !registry.Registered(MethodBridgePairCode) {
		t.Fatal("pa.pair_code vanished with the module; a verb that disappears cannot be diagnosed")
	}
	if _, rpcErr := registry.Dispatch(context.Background(),
		&rpc.Request{JSONRPC: "2.0", Method: MethodBridgePairCode}); rpcErr == nil {
		t.Fatal("a disabled bridge issued a code")
	}
	if issued != 1 {
		t.Fatalf("the composition root's refusal ran %d times, want 1", issued)
	}
}

// TestRegisterBridgeModule_NoIssuerLeavesTheVerbUnbound: a registry must never
// gain a verb with nothing behind it.
func TestRegisterBridgeModule_NoIssuerLeavesTheVerbUnbound(t *testing.T) {
	m := NewManifest(nil, nil)
	registry := rpc.NewRegistry()
	m.RegisterBridgeModule(context.Background(), registry, BridgeSubsystem{})
	if registry.Registered(MethodBridgePairCode) {
		t.Fatal("pa.pair_code was bound with no issuer behind it")
	}
	if row := findStatus(t, m, bridgeSubsystem); row.Detail != "no bridge module is enabled" {
		t.Fatalf("detail = %q, want the default reason", row.Detail)
	}
}

// TestRegisterBridgeModule_StartFailureIsRecordedNotFatal: a bridge that cannot
// start is a failed subsystem, not a dead daemon — and nothing is left waiting.
func TestRegisterBridgeModule_StartFailureIsRecordedNotFatal(t *testing.T) {
	m := NewManifest(nil, nil)
	st := &bridgeState{}
	m.RegisterBridgeModule(context.Background(), rpc.NewRegistry(), BridgeSubsystem{
		Start: func(context.Context) error { return errors.New("test: the transport refused") },
		Stop:  func(context.Context) error { st.stopped.Add(1); return nil },
	})
	row := findStatus(t, m, bridgeSubsystem)
	if row.State != SubsystemError {
		t.Fatalf("state = %s, want error", row.State)
	}
	m.Wait() // must not block: no drain goroutine was started for a poll that never ran
	if st.stopped.Load() != 0 {
		t.Fatal("Stop was called for a module that never started")
	}
}

// TestBridgePairCodeHandler_ParamDecoding: an absent body is a valid request
// (issue for the bridge's own subject); a malformed one is refused, and refused
// as invalid input rather than swallowed into a default.
func TestBridgePairCodeHandler_ParamDecoding(t *testing.T) {
	var got string
	handler := bridgePairCodeHandler(func(_ context.Context, subject string) (BridgePairCodeResult, error) {
		got = subject
		return BridgePairCodeResult{Code: "7ZQK3M9F", Subject: subject}, nil
	})
	if _, err := handler(context.Background(), nil); err != nil {
		t.Fatalf("a nil params body was refused: %v", err)
	}
	if got != "" {
		t.Fatalf("a nil body produced subject %q, want the empty ask-the-bridge value", got)
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"subject":`)); err == nil {
		t.Fatal("malformed params were accepted")
	} else if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("malformed params gave %v, want invalid input", err)
	}
}

// TestRegisterBridgeModule_DrainFailureIsRecorded: a Stop that reports a problem
// must not disappear, or a wedged poll at shutdown leaves no trace at all.
func TestRegisterBridgeModule_DrainFailureIsRecorded(t *testing.T) {
	m := NewManifest(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	m.RegisterBridgeModule(ctx, nil, BridgeSubsystem{
		Start: func(context.Context) error { return nil },
		Stop:  func(context.Context) error { return errors.New("test: the poll would not drain") },
	})
	cancel()
	m.Wait()
	row := findStatus(t, m, bridgeSubsystem)
	if row.State != SubsystemError {
		t.Fatalf("state = %s, want the drain failure recorded", row.State)
	}
	if row.Detail == "" || row.Detail == "polling for bridge updates" {
		t.Fatalf("detail = %q, want the drain failure", row.Detail)
	}
}

// TestBridgeDrainTimeoutIsBounded keeps the drain budget honest: a wedged Stop
// must not hold shutdown open forever, and the bound has to be long enough for a
// long poll to notice its cancellation.
func TestBridgeDrainTimeoutIsBounded(t *testing.T) {
	if bridgeDrainTimeout < time.Second || bridgeDrainTimeout > time.Minute {
		t.Fatalf("bridgeDrainTimeout = %s, want a bound between 1s and 1m", bridgeDrainTimeout)
	}
}
