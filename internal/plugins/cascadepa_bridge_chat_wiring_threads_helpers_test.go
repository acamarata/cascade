package plugins

// Purpose: read-side helpers of the thread-map bridge rig (the rig itself is
// cascadepa_bridge_chat_wiring_threads_test.go): wait for a batch, read the
// durable map, count a thread's stored turns, wait for a settled state.
// SPORT: internal/plugins (TEST).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// settled polls (up to 5s) until chat is bound, wantMints threads were minted
// and the bound thread holds one turn per mint, then returns what it saw. The
// bridge reports a batch as polled before its appends finish, so a test that
// reads the stores right after await can see a half-written state.
func (r *mapRig) settled(t *testing.T, chat int64, wantMints int) (string, bool, []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		thread, ok := r.mapped(t, chat)
		minted := r.daemon.mintedIDs()
		if ok && len(minted) == wantMints && r.turns(t, thread) == wantMints || time.Now().After(deadline) {
			return thread, ok, minted
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// mapped returns the thread the durable map binds chat to.
func (r *mapRig) mapped(t *testing.T, chat int64) (string, bool) {
	t.Helper()
	store, err := OpenBridgeThreadMap(context.Background(), r.db, r.deps.DataDir, r.deps.Clock)
	if err != nil {
		t.Fatalf("OpenBridgeThreadMap: %v", err)
	}
	thread, ok, err := store.ThreadFor(context.Background(), telegram.SubjectFromToken(syntheticBotToken), chat)
	if err != nil {
		t.Fatalf("ThreadFor: %v", err)
	}
	return thread, ok
}

// turns is threadID's stored turn count.
func (r *mapRig) turns(t *testing.T, threadID string) int {
	t.Helper()
	got, err := r.store.ListTurns(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ListTurns(%s): %v", threadID, err)
	}
	return len(got)
}
