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
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestNewModule_AssemblesAndNeverDials(t *testing.T) {
	stores := cascadepa.NewStores(fixedTestClock{t0()}, &okRegistrar{}, newMemState(), testPairKey(t))
	module := NewModule(syntheticToken, nil, nil, nodeVerbPolicy(), stores, nil)
	if module == nil {
		t.Fatal("NewModule returned nil")
	}
	if module.subject != SubjectFromToken(syntheticToken) {
		t.Fatalf("module subject = %q, want the token digest", module.subject)
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
