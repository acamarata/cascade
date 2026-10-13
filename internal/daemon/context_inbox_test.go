package daemon

// Purpose: proves context.slice's inbox hydration drain (context_inbox.go,
// P1-E37-W8-S73-T1) through the REAL registry.Dispatch: a known live
// session's envelopes come back on Inbox; an unknown, closed or
// absent session drains NOTHING and never touches the source (its
// envelopes stay queued); a drain error leaves the slice unaffected.
// Constraints: Art.7.1 -- real git repo and SQLite under t.TempDir(), a
// memory provider.Store for the sessions domain, no network.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// drainLog is an InboxHydrationSource that records which sessions were
// drained and hands back one envelope per drain (or fails).
type drainLog struct {
	drained []string
	fail    bool
}

func (d *drainLog) Drain(_ context.Context, sessionID string) ([]InboxEnvelopeView, error) {
	d.drained = append(d.drained, sessionID)
	if d.fail {
		return nil, errors.New("hydration store offline")
	}
	return []InboxEnvelopeView{{ID: "env-" + sessionID, Class: "addressed", DataClass: "restricted", Provenance: "untrusted-source"}}, nil
}

// sliceWithInbox registers context.slice with the live-session-gated
// source over a sessions store holding live-1 (active) and gone-1 (closed),
// and returns a dispatcher for one session id.
func sliceWithInbox(t *testing.T, src InboxHydrationSource) func(sessionID string) ContextSliceResult {
	t.Helper()
	as := sliceWithInboxAs(t, src)
	return func(sessionID string) ContextSliceResult { return as(ownerCLI(), sessionID) }
}

// sliceWithInboxAs is sliceWithInbox with the request's context chosen by
// the caller, so a test can present any identity (or none).
func sliceWithInboxAs(t *testing.T, src InboxHydrationSource) func(ctx context.Context, sessionID string) ContextSliceResult {
	t.Helper()
	return sliceWithOption(t, func(live *sessions.Store) ContextOption {
		return WithInboxHydration(LiveSessionInboxSource(live, src))
	})
}

// sliceWithOption registers context.slice with the option build returns,
// over a sessions store holding live-1 (active) and gone-1 (closed).
func sliceWithOption(t *testing.T, build func(live *sessions.Store) ContextOption) func(ctx context.Context, sessionID string) ContextSliceResult {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	seedRepo(t, dir)
	chdir(t, dir)
	paths := fakePaths{root: dir}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewSystemClock()
	live := sessions.New(storetest.NewMemStore(), clock, nil)
	for id, state := range map[string]string{"live-1": sessions.StateActive.String(), "gone-1": sessions.StateClosed.String(), "odd-1": "zombie", "blank-1": ""} {
		if err := live.Upsert(context.Background(), sessions.SessionRecord{SessionID: id, Harness: "cc", State: state}); err != nil {
			t.Fatal(err)
		}
	}
	reg := rpc.NewRegistry()
	db, err := RegisterContextAssembleHandler(reg, paths, clock, build(live))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return func(ctx context.Context, sessionID string) ContextSliceResult {
		params, _ := json.Marshal(ContextAssembleParams{Cwd: dir, SessionID: sessionID})
		res, errObj := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: ContextSliceMethod, ID: []byte("1"), Params: params})
		if errObj != nil {
			t.Fatalf("context.slice(%q) failed: %+v", sessionID, errObj)
		}
		return res.(ContextSliceResult)
	}
}

func TestContextSliceDrainsInboxEnvelopes(t *testing.T) {
	src := &drainLog{}
	slice := sliceWithInbox(t, src)
	got := slice("live-1")
	if len(got.Inbox) != 1 || got.Inbox[0].ID != "env-live-1" || got.Inbox[0].Provenance != "untrusted-source" {
		t.Fatalf("live session's envelopes = %+v", got.Inbox)
	}
	if len(src.drained) != 1 || src.drained[0] != "live-1" {
		t.Fatalf("drained %v, want exactly [live-1]", src.drained)
	}
	// A source that hands back an envelope for ANY id must still yield none
	// for a session the sessions store does not know.
	if unknown := slice("never-seen"); unknown.Inbox != nil || len(src.drained) != 1 {
		t.Fatalf("an unknown session received %+v (source drained %v)", unknown.Inbox, src.drained)
	}
	src.fail = true
	if failed := slice("live-1"); failed.Inbox != nil || failed.Counts != got.Counts {
		t.Fatalf("a drain error changed the slice: %+v", failed)
	}
}

func TestContextSliceUnknownSessionNoEnvelopes(t *testing.T) {
	src := &drainLog{}
	slice := sliceWithInbox(t, src)
	for _, sid := range []string{"never-seen", "gone-1", "odd-1", "blank-1", ""} {
		if got := slice(sid); got.Inbox != nil {
			t.Fatalf("session %q received envelopes %+v", sid, got.Inbox)
		}
	}
	if len(src.drained) != 0 {
		t.Fatalf("an unknown/closed/empty session drained the source: %v (its envelopes would be lost)", src.drained)
	}
	if views, err := LiveSessionInboxSource(nil, src).Drain(context.Background(), "live-1"); views != nil || err != nil {
		t.Fatalf("a source without a sessions store drained: (%v, %v)", views, err)
	}
}

// TestContextSliceInboxCallerRule: the drain follows the directive rule. A
// request with no caller, or a principal bound to another session, drains
// nothing and never touches the source; the owner CLI and the session's own
// principal do.
func TestContextSliceInboxCallerRule(t *testing.T) {
	src := &drainLog{}
	slice := sliceWithInboxAs(t, src)
	for name, ctx := range map[string]context.Context{
		"no caller":         context.Background(),
		"another session's": sessionPrincipal("other-session"),
	} {
		if got := slice(ctx, "live-1"); got.Inbox != nil {
			t.Fatalf("%s request received envelopes %+v", name, got.Inbox)
		}
	}
	if len(src.drained) != 0 {
		t.Fatalf("a refused caller drained the source: %v", src.drained)
	}
	for name, ctx := range map[string]context.Context{"owner CLI": ownerCLI(), "own principal": sessionPrincipal("live-1")} {
		if got := slice(ctx, "live-1"); len(got.Inbox) != 1 {
			t.Fatalf("%s did not receive the session's envelope: %+v", name, got.Inbox)
		}
	}
}

// TestHydrationProductionFlagOff: the nil-source option the daemon registers
// while the hydration flag is off switches the drain off. A spy source set
// first is never asked, and a context.slice for a live session returns no
// Inbox.
func TestHydrationProductionFlagOff(t *testing.T) {
	spy := &drainLog{}
	slice := sliceWithOption(t, func(live *sessions.Store) ContextOption {
		return func(o *contextOptions) {
			WithInboxHydration(LiveSessionInboxSource(live, spy))(o)
			WithInboxHydration(nil)(o)
		}
	})
	if got := slice(ownerCLI(), "live-1"); got.Inbox != nil || len(spy.drained) != 0 {
		t.Fatalf("flag-off slice returned Inbox %+v and drained %v", got.Inbox, spy.drained)
	}
	// Control: the same spy, left on, is drained for the same request.
	on := sliceWithInbox(t, spy)
	if got := on("live-1"); len(got.Inbox) != 1 || len(spy.drained) != 1 {
		t.Fatalf("control: the source was not drained when left on: %+v %v", got.Inbox, spy.drained)
	}
}
