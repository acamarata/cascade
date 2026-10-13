package cascadepa

// Purpose (this file): ThreadMapStore over the compare-and-swap memState
//   fake: create-only binding in both directions, the first binding of a
//   chat winning every race, a thread never answering for two chats (also
//   when the other chat wins between the pre-check and the thread row),
//   self-healing of a half-written binding, fail-closed reads, and map rows
//   that never pass the pairing gate.
//
// SPORT: plugins/cascade-pa ThreadMapStore/TEST.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

const mapSubject = "tg-mapsubject000"

func TestThreadMapStoreBindAndRead(t *testing.T) {
	ctx := context.Background()
	s := NewThreadMapStore(newMemState())
	if _, ok, err := s.ThreadFor(ctx, mapSubject, 41); err != nil || ok {
		t.Fatalf("ThreadFor on an empty map = %v, %v; want a miss", ok, err)
	}
	bound, err := s.Bind(ctx, mapSubject, 41, "thread-41")
	if err != nil || bound != "thread-41" {
		t.Fatalf("Bind = %q, %v; want thread-41", bound, err)
	}
	if got, ok, err := s.ThreadFor(ctx, mapSubject, 41); err != nil || !ok || got != "thread-41" {
		t.Fatalf("ThreadFor = %q, %v, %v", got, ok, err)
	}
	if got, ok, err := s.ChatFor(ctx, mapSubject, "thread-41"); err != nil || !ok || got != 41 {
		t.Fatalf("ChatFor = %d, %v, %v", got, ok, err)
	}
	if _, ok, err := s.ChatFor(ctx, "tg-othersubject0", "thread-41"); err != nil || ok {
		t.Fatalf("another subject sees the binding: %v, %v", ok, err)
	}
	if _, ok, err := s.ChatFor(ctx, mapSubject, ""); err != nil || ok {
		t.Fatalf("ChatFor(\"\") = %v, %v; want a miss", ok, err)
	}
}

// TestThreadMapStoreFirstBindWins: a second Bind of the chat returns the
// first thread and writes nothing; racing Binds all end on one thread, and
// the losers' threads stay unbound.
func TestThreadMapStoreFirstBindWins(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	s := NewThreadMapStore(state)
	if _, err := s.Bind(ctx, mapSubject, 42, "thread-first"); err != nil {
		t.Fatal(err)
	}
	writes := state.saveCall
	if got, err := s.Bind(ctx, mapSubject, 42, "thread-second"); err != nil || got != "thread-first" {
		t.Fatalf("second Bind = %q, %v; want the existing thread-first", got, err)
	}
	if state.saveCall != writes {
		t.Fatalf("a losing Bind wrote %d rows", state.saveCall-writes)
	}
	if _, ok, _ := s.ChatFor(ctx, mapSubject, "thread-second"); ok {
		t.Fatal("the losing thread became a bridge thread")
	}

	results := make([]string, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = s.Bind(ctx, mapSubject, 43, "thread-race-"+string(rune('a'+i)))
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if r == "" || r != results[0] {
			t.Fatalf("racing binds ended on %v, want one thread for all", results)
		}
	}
}

// TestThreadMapStoreThreadBoundElsewhere: a thread bound to one chat is
// refused for another, with the second chat left unbound.
func TestThreadMapStoreThreadBoundElsewhere(t *testing.T) {
	ctx := context.Background()
	s := NewThreadMapStore(newMemState())
	if _, err := s.Bind(ctx, mapSubject, 44, "thread-44"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(ctx, mapSubject, 45, "thread-44"); !errors.Is(err, ErrThreadBoundElsewhere) ||
		err.Error() != ErrThreadBoundElsewhere.Error() {
		t.Fatalf("Bind of a taken thread = %v, want ErrThreadBoundElsewhere", err)
	}
	if _, ok, _ := s.ThreadFor(ctx, mapSubject, 45); ok {
		t.Fatal("chat 45 was bound to another chat's thread")
	}
}

// TestThreadMapStoreHealsHalfBinding: a chat row whose thread row is
// missing (a crash between the two writes) reads as no bridge thread until
// the next Bind re-asserts the binding and writes the missing row.
func TestThreadMapStoreHealsHalfBinding(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	state.seed(SubjectState{Subject: chatMapKey(mapSubject, 46), TrustTier: threadMapTier, CodeDigest: "thread-46"})
	s := NewThreadMapStore(state)
	if _, ok, _ := s.ChatFor(ctx, mapSubject, "thread-46"); ok {
		t.Fatal("a half-written binding already reads as a bridge thread")
	}
	if got, err := s.Bind(ctx, mapSubject, 46, "thread-other"); err != nil || got != "thread-46" {
		t.Fatalf("Bind = %q, %v; want the existing thread-46", got, err)
	}
	if got, ok, err := s.ChatFor(ctx, mapSubject, "thread-46"); err != nil || !ok || got != 46 {
		t.Fatalf("ChatFor after heal = %d, %v, %v", got, ok, err)
	}
}

// TestThreadMapStoreFailsClosed: an unwired store, a store error and a row
// of the wrong shape all refuse; none reads as a binding.
func TestThreadMapStoreFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, err := NewThreadMapStore(nil).Bind(ctx, mapSubject, 1, "t"); err != ErrNoBridgeState {
		t.Fatalf("unwired Bind = %v, want ErrNoBridgeState", err)
	}
	down := newMemState()
	down.loadErr = errStoreDown
	if _, _, err := NewThreadMapStore(down).ThreadFor(ctx, mapSubject, 1); err != errStoreDown {
		t.Fatalf("ThreadFor on a dead store = %v", err)
	}
	broken := newMemState()
	broken.seed(SubjectState{Subject: chatMapKey(mapSubject, 2), TrustTier: "paired-device", CodeDigest: "thread-2"})
	broken.seed(SubjectState{Subject: threadMapKey(mapSubject, "thread-3"), TrustTier: threadMapTier, CodeDigest: "+3"})
	broken.seed(SubjectState{Subject: chatMapKey(mapSubject, 4), TrustTier: threadMapTier, CodeDigest: "thread-4",
		CodeExpiresAt: time.Unix(1_700_000_000, 0)})
	s := NewThreadMapStore(broken)
	if _, _, err := s.ThreadFor(ctx, mapSubject, 4); err != ErrThreadMapCorrupt {
		t.Fatalf("ThreadFor on a row a pairing write touched = %v, want ErrThreadMapCorrupt", err)
	}
	if _, _, err := s.ThreadFor(ctx, mapSubject, 2); err != ErrThreadMapCorrupt {
		t.Fatalf("ThreadFor on a foreign-shaped row = %v, want ErrThreadMapCorrupt", err)
	}
	if _, _, err := s.ChatFor(ctx, mapSubject, "thread-3"); err != ErrThreadMapCorrupt {
		t.Fatalf("ChatFor on a non-canonical chat id = %v, want ErrThreadMapCorrupt", err)
	}
	if _, err := s.Bind(ctx, "", 1, "t"); err == nil {
		t.Fatal("Bind with no subject succeeded")
	}
}

// hookState is a BridgeState whose beforeSave runs ahead of every Save, so a
// test can make another writer win a specific step of a Bind.
type hookState struct {
	BridgeState
	beforeSave func(SubjectState)
}

func (h *hookState) Save(ctx context.Context, st SubjectState) error {
	if h.beforeSave != nil {
		h.beforeSave(st)
	}
	return h.BridgeState.Save(ctx, st)
}

// TestThreadMapStoreBindLosesThreadRowToAnotherChat: another chat claims the
// thread's reverse row after Bind's pre-check and before Bind's own thread
// row. Bind must refuse with ErrThreadBoundElsewhere and the thread must
// still answer for the other chat. Mutation: drop the back != chat guard
// and Bind reports success for a thread that belongs to the other chat.
func TestThreadMapStoreBindLosesThreadRowToAnotherChat(t *testing.T) {
	ctx := context.Background()
	mem := newMemState()
	hook := &hookState{BridgeState: mem}
	s := NewThreadMapStore(hook)
	const thread, mine, other = "thread-contested", int64(61), int64(62)
	hook.beforeSave = func(st SubjectState) {
		if st.Subject != chatMapKey(mapSubject, mine) {
			return
		}
		hook.beforeSave = nil
		mem.seed(SubjectState{Subject: threadMapKey(mapSubject, thread), TrustTier: threadMapTier,
			CodeDigest: strconv.FormatInt(other, 10)})
	}
	_, err := s.Bind(ctx, mapSubject, mine, thread)
	if err == nil || err != ErrThreadBoundElsewhere || err.Error() != ErrThreadBoundElsewhere.Error() {
		t.Fatalf("Bind = %v, want ErrThreadBoundElsewhere", err)
	}
	if got, ok, err := s.ChatFor(ctx, mapSubject, thread); err != nil || !ok || got != other {
		t.Fatalf("ChatFor after the lost race = %d, %v, %v; want the other chat %d", got, ok, err, other)
	}
}

// TestThreadMapRowsNeverPassThePairingGate: a map row carries its value in
// CodeDigest and no allowlist, so the pairing questions answer false for
// every sender on both reserved keys; a row with the right tier but a
// populated allowlist is corrupt, never a binding.
func TestThreadMapRowsNeverPassThePairingGate(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	s := NewThreadMapStore(state)
	if _, err := s.Bind(ctx, mapSubject, 71, "thread-71"); err != nil {
		t.Fatal(err)
	}
	binding := NewBindingStore(fixedTestClock{at: t0()}, nil, state)
	for _, key := range []string{chatMapKey(mapSubject, 71), threadMapKey(mapSubject, "thread-71")} {
		if _, ok, err := state.Load(ctx, key); err != nil || !ok {
			t.Fatalf("map row %q missing: %v, %v", key, ok, err)
		}
		if bound, err := binding.Bound(ctx, key); err != nil || bound {
			t.Fatalf("Bound(%q) = %v, %v; want false", key, bound, err)
		}
		for _, sender := range []string{"71", "thread-71", "", mapSubject} {
			if ok, err := binding.IsAllowed(ctx, key, sender); err != nil || ok {
				t.Fatalf("IsAllowed(%q, %q) = %v, %v; want false", key, sender, ok, err)
			}
		}
	}
	state.seed(SubjectState{Subject: chatMapKey(mapSubject, 72), TrustTier: threadMapTier,
		CodeDigest: "thread-72", AllowedFrom: []string{"72"}})
	if _, _, err := s.ThreadFor(ctx, mapSubject, 72); err != ErrThreadMapCorrupt || err.Error() != ErrThreadMapCorrupt.Error() {
		t.Fatalf("ThreadFor on a row with an allowlist = %v, want ErrThreadMapCorrupt", err)
	}
}
