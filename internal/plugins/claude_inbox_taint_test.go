package plugins

// Purpose: proves the bridge's sticky session taint: the first untrusted
// turn marks the session before its records leave the queue, later turns
// keep the mark, and a mark error or unknown session returns nothing.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// newInboxRigOn wires a bridge over base and subscribes sids. Each of sids,
// and s1 (which some tests wire themselves), is a registered session, as the
// hook pack registers one in production; the setup writes are not recorded.
func newInboxRigOn(t *testing.T, base provider.Store, clock runtime.Clock, sids ...string) inboxRig {
	t.Helper()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	store := &nsRecorder{Store: base, written: map[string]bool{}}
	attn := supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0)
	bridge, err := NewClaudeInboxBridge(context.Background(), store, clock, attn)
	if err != nil {
		t.Fatalf("NewClaudeInboxBridge: %v", err)
	}
	svc := notify.NewService(notify.DefaultConfig(), clock, nil)
	taint := sessions.New(store, clock, nil)
	for i, sid := range append([]string{"s1"}, sids...) {
		if err := taint.Upsert(context.Background(), sessions.SessionRecord{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			unwire, err := WireClaudeInboxPush(context.Background(), bridge, svc.Registry, notify.CandidateSession{SessionID: sid})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(unwire)
		}
	}
	clear(store.written)
	return inboxRig{store: store, bridge: bridge, attn: attn, svc: svc, taint: taint}
}

// tickClock is a clock a test advances between turns.
type tickClock struct{ ms atomic.Int64 }

func (c *tickClock) Now() time.Time { return time.UnixMilli(c.ms.Load()).UTC() }

func addressedTo(sid, id string) notify.Notification {
	return notify.Notification{ID: id, Class: notify.ClassAddressed, Visibility: notify.VisibilityPrivate, TargetSession: sid, Payload: []byte("x")}
}

// TestTaintPersistsAcrossLaterTurns: the first untrusted turn marks the
// session; a later trusted turn and an Appended=false turn keep the mark as
// it was; a mark error or an unknown session returns nothing and keeps the
// queue for the next turn.
func TestTaintPersistsAcrossLaterTurns(t *testing.T) {
	ctx := context.Background()
	clock := &tickClock{}
	clock.ms.Store(inboxNow.now.UnixMilli())
	rig := newInboxRigOn(t, storetest.NewMemStore(), clock, "s1", "s2", "ghost")
	rig.deliver(t, addressedTo("s1", "t1"))
	if recs, _ := rig.drain(t, "s1"); len(recs) != 1 {
		t.Fatalf("first turn hydrated %+v", recs)
	}
	want := sessions.TaintState{Tainted: true, SinceMs: inboxNow.now.UnixMilli(), ProvenanceRef: "t1"}
	trusted := addressedTo("s1", "t2")
	trusted.Origin = notify.Origin{Source: "supervision.stall", RecordRef: "r", Tier: provider.SensitivityPublic, Trust: provider.ProvenanceTrusted}
	rig.deliver(t, trusted)
	for _, n := range []int{1, 0} { // a trusted turn, then an empty one
		clock.ms.Add(60_000)
		recs, res := rig.drain(t, "s1")
		if st, err := rig.taint.Taint(ctx, "s1"); len(recs) != n || res.Appended != (n == 1) || err != nil || st != want {
			t.Fatalf("later turn (%+v, %+v) left taint (%+v, %v), want %+v", recs, res, st, err, want)
		}
	}
	rig.store.failNS = sessions.DefaultNamespace
	rig.deliver(t, addressedTo("s2", "t3"))
	if recs, _, err := rig.bridge.ReadNextTurnForSession(ctx, "s2"); recs != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "store offline") {
		t.Fatalf("a failed mark returned (%+v, %v)", recs, err)
	}
	rig.store.failNS = ""
	if recs, _ := rig.drain(t, "s2"); len(recs) != 1 || recs[0].ID != "t3" {
		t.Fatalf("after a failed mark the queue lost its record: %+v", recs)
	}
	if err := rig.store.Delete(ctx, sessions.DefaultNamespace, "session:ghost"); err != nil {
		t.Fatal(err)
	}
	rig.deliver(t, addressedTo("ghost", "t4"))
	if recs, _, err := rig.bridge.ReadNextTurnForSession(ctx, "ghost"); recs != nil || !errors.Is(err, sessions.ErrNotFound) || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("an unknown session drained (%+v, %v)", recs, err)
	}
}

// TestClaudeInboxBridgeRealWiring: over a real SQLite store, the production
// constructor, push wiring and drain mark a registered session and hand its
// untrusted record over exactly once.
func TestClaudeInboxBridgeRealWiring(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rig := newInboxRigOn(t, db, inboxNow, "cc-1")
	rig.deliver(t, addressedTo("cc-1", "w1"))
	recs, res := rig.drain(t, "cc-1")
	st, err := rig.taint.Taint(ctx, "cc-1")
	if len(recs) != 1 || !res.Appended || !recs[0].Untrusted() || err != nil || !st.Tainted || st.ProvenanceRef != "w1" || st.SinceMs != inboxNow.now.UnixMilli() {
		t.Fatalf("drain (%+v, %+v) left taint (%+v, %v)", recs, res, st, err)
	}
	if again, _ := rig.drain(t, "cc-1"); len(again) != 0 {
		t.Fatalf("a second turn re-delivered %+v", again)
	}
}
