package telegram

// Purpose (this file): NewModule's own test — the assembly point every host
//   calls, exercised without a single byte reaching the network.
//
// Constraints: Start is given an ALREADY-CANCELLED ctx, so the poll loop
//   returns at its first ctx check and the real httpPoster this assembly
//   builds never dials api.telegram.org. The egress gate is nil as well
//   (fail-closed), so even a racing iteration would refuse before the
//   transport. Two independent reasons no test here can reach the network.
//
// SPORT: plugins/cascade-pa/telegram assemble-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"runtime"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestNewModule_AssemblesAndNeverDials(t *testing.T) {
	stores := cascadepa.NewStores(fixedTestClock{t0()}, &okRegistrar{}, newMemState(), testPairKey(t))
	// secretScanner/quarantine are nil here deliberately: Start is given an
	// already-cancelled ctx (see this file's header) so the poll loop never
	// dispatches, meaning the fail-closed scanner default (T0 D1) is never
	// exercised on this path — this test is NewModule's assembly proof, not
	// a dispatch test (see refuse_test.go for those).
	module := NewModule(syntheticToken, nil, nil, nodeVerbPolicy(), stores, nil, nil, nil)
	if module == nil {
		t.Fatal("NewModule returned nil")
	}
	if module.subject != SubjectFromToken(syntheticToken) {
		t.Fatalf("module subject = %q, want the token digest", module.subject)
	}
	if runtime.GOOS == "windows" {
		t.Skip("Start is refused on Windows tier-2; module_windows_test.go proves the refusal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := module.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := module.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestNewModule_WiresScannerAndQuarantineForDispatch is the production-path
// proof the confirming review's mutation exposed: NewModule is this
// package's ONE front door (this file's own header), and nothing else ever
// builds a module through it and dispatches. Assemble one with a fake
// scanner and a recording sink — exactly the shapes a real host wires —
// dispatch the witness secret, and assert BOTH the refusal and that
// exactly one quarantine event reaches the sink NewModule wired in.
// Deleting assemble.go's `module.secretScanner = secretScanner` drops the
// event count to 0 (the unwired default has nothing to quarantine, only a
// missing capability — ErrNoSecretScanner); deleting its
// `module.quarantine = quarantine` twin drops it to 0 too, since nothing
// was ever handed to the sink this test reads. Either mutation turns this
// red without touching the refusal assertion at all.
func TestNewModule_WiresScannerAndQuarantineForDispatch(t *testing.T) {
	stores := cascadepa.NewStores(fixedTestClock{t0()}, &okRegistrar{}, newMemState(), testPairKey(t))
	scanner := secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	module := NewModule(syntheticToken, nil, nil, nodeVerbPolicy(), stores, nil, scanner, sink)

	called := false
	module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))

	if called {
		t.Fatal("a handler ran for a secret-shaped message assembled through NewModule")
	}
	if len(sink.events) != 1 {
		t.Fatalf("quarantine events on the sink NewModule wired = %d, want 1", len(sink.events))
	}
}

// TestNewBotClient_NilLedgerFailsClosed: a client built with no ledger cannot
// resolve an offset, so it delivers nothing rather than polling from zero.
func TestNewBotClient_NilLedgerFailsClosed(t *testing.T) {
	doer := &fakeDoer{}
	doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	c := NewBotClient(testSubject, doer, &tierGate{}, nil)
	sleep, _ := instantSleep()
	c.sleep = sleep
	if got := pollBriefly(t, c); len(got) != 0 {
		t.Fatalf("delivered %d updates with no ledger wired", len(got))
	}
}
