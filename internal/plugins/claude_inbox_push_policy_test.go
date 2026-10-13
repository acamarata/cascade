package plugins

// Purpose: delivery-policy proofs for the cascade-claude inbox bridge through
// the REAL notify router: a foreign-scope item never reaches a session (the
// router's own predicate refuses it before the bridge, and the coordinator
// below has no scope check), and an error retrying cannot cure (a full or
// unreadable queue) is withheld instead of being redelivered to every other
// session on each dispatcher drain, while a provider store failure is not.
// Constraints: in-memory store, injected clock, no network.

import (
	"context"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/claude"
)

func TestClaudeInboxPushForeignScopeWithheld(t *testing.T) {
	rig := newInboxRig(t)
	session := notify.CandidateSession{SessionID: "s1", ScopeIDs: map[string]bool{"repo-a": true}}
	unwire, err := WireClaudeInboxPush(context.Background(), rig.bridge, rig.svc.Registry, session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unwire)
	scoped := func(id, target string) notify.Notification {
		return notify.Notification{ID: id, Class: notify.ClassScoped, Visibility: notify.VisibilityScoped, OriginScope: target, TargetScope: target, Payload: []byte("x")}
	}
	rig.deliver(t, scoped("foreign", "repo-b"))
	if recs, res := rig.drain(t, "s1"); len(recs) != 0 || res.Appended {
		t.Fatalf("a foreign-scope item reached the session: %+v", recs)
	}
	rig.deliver(t, scoped("mine", "repo-a"))
	if recs, _ := rig.drain(t, "s1"); len(recs) != 1 || recs[0].ID != "mine" {
		t.Fatalf("control: the in-scope item did not arrive: %+v", recs)
	}
}

func TestClaudeInboxPushPersistentErrorDoesNotRepeat(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(rig inboxRig){
		"full queue": func(rig inboxRig) {
			for i := range 256 {
				if err := rig.bridge.hydration.Enqueue(ctx, "sess-b", claude.InboxEvent{ID: fmt.Sprintf("fill-%d", i), Class: "global-critical"}); err != nil {
					t.Fatal(err)
				}
			}
		},
		"unreadable queue": func(rig inboxRig) {
			if err := rig.store.Put(ctx, claude.HydrationNamespace, "sess-b", []byte("{not json")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		rig := newInboxRig(t, "sess-a", "sess-b")
		setup(rig)
		rig.deliver(t, notify.Notification{ID: "persist-1", Class: notify.ClassGlobalCritical, Visibility: notify.VisibilityShared, Payload: []byte("x")})
		for turn := 1; turn <= 3; turn++ {
			recs, _ := rig.drain(t, "sess-a")
			want := 0
			if turn == 1 {
				want = 1
			}
			if len(recs) != want {
				t.Fatalf("%s, turn %d: sess-a hydrated %d records, want %d (the failing sess-b queue made the dispatcher redeliver)", name, turn, len(recs), want)
			}
			rig.svc.Dispatcher.Drain(ctx)
		}
	}
}

// TestPermanentRouteErrorIsHydrationOwned: only this plugin's own full and
// unreadable-queue failures and refused envelopes are withheld; a provider
// store failure of the very same Kind is returned for redelivery.
func TestPermanentRouteErrorIsHydrationOwned(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"full queue":        {claude.ErrHydrationFull, true},
		"unreadable queue":  {claude.ErrHydrationCorrupt, true},
		"refused envelope":  {claude.ErrEnvelopeTooLarge, true},
		"store quota":       {cascade.New(cascade.KindQuotaExhausted, "connection limit"), false},
		"store corruption":  {cascade.New(cascade.KindIntegrity, "database disk image is malformed"), false},
		"store unavailable": {cascade.New(cascade.KindUnavailable, "locked"), false},
	} {
		if got := permanentRouteError(tc.err); got != tc.want {
			t.Errorf("%s: permanentRouteError = %v, want %v", name, got, tc.want)
		}
	}
}
