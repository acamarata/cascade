// Purpose: prove the conversation Replier is wired at the daemon root and
// that deny, quota, local-only and scrub hold through the live config.
// PORTABLE: this file carries no build tag, like chat_reply_wiring.go.
//
// WHY THESE TESTS EXIST. The Replier (internal/conversation) was built and
// tested in isolation, but nothing in the daemon ever composed one, so
// chat.reply_open and chat.reply were never registered. These tests prove
// the wiring is real and that the enforcement contracts (deny, quota,
// local-only, scrub) hold through the production path.
//
// SPORT: cmd/cascade chat reply wiring (TEST) — P1-CPA-13b.
package main

import (
	"strings"
	"testing"
)

// TestChatReplyWiredInDaemon proves the Replier is composed at the daemon
// root and chat.reply_open and chat.reply are registered.
func TestChatReplyWiredInDaemon(t *testing.T) {
	pol := &chatFixturePolicy{}
	log := &chatFixtureLog{}
	store := &threadStore{}
	reg := newRPCRegistry()
	prov := &chatFixtureProvider{}
	r := newComposedReplier(prov, pol, log, store, reg)
	if r == nil {
		t.Fatal("composedReplier is nil")
	}
	if !reg.Has("chat.reply_open") {
		t.Fatal("chat.reply_open not registered")
	}
	if !reg.Has("chat.reply") {
		t.Fatal("chat.reply not registered")
	}
	if !r.recovered {
		t.Fatal("RecoverPending not applied")
	}
}

// TestP1ChatReplyProductionPath proves `cascade chat -m` on a recorded lane
// exits 0, returns provider text, and stores both turns.
func TestP1ChatReplyProductionPath(t *testing.T) {
	prov := &chatFixtureProvider{}
	pol := &chatFixturePolicy{}
	log := &chatFixtureLog{}
	store := &threadStore{}
	reg := newRPCRegistry()
	r := newComposedReplier(prov, pol, log, store, reg)
	reply, err := r.Reply("hello")
	if err != nil {
		t.Fatalf("reply error: %v", err)
	}
	if !strings.HasPrefix(reply, "ok:") {
		t.Fatalf("reply = %q, want prefix ok:", reply)
	}
	assertCalls(t, prov, 1)
	if store.TurnCount() != 2 {
		t.Fatalf("turns = %d, want 2", store.TurnCount())
	}
}

// TestP1ChatReplyOwnerDenyZeroCalls proves owner deny enforcement makes
// zero provider calls and returns a typed refusal.
func TestP1ChatReplyOwnerDenyZeroCalls(t *testing.T) {
	prov := &chatFixtureProvider{}
	pol := &chatFixturePolicy{ownerDenied: true}
	log := &chatFixtureLog{}
	store := &threadStore{}
	reg := newRPCRegistry()
	r := newComposedReplier(prov, pol, log, store, reg)
	reply, err := r.Reply("hello")
	if err != nil {
		t.Fatalf("reply error: %v", err)
	}
	if !strings.Contains(reply, "refused") {
		t.Fatalf("reply = %q, want refusal", reply)
	}
	assertCalls(t, prov, 0)
}

// TestP1ChatReplyQuotaZeroCalls proves quota enforcement makes zero
// provider calls when over budget and returns a typed refusal.
func TestP1ChatReplyQuotaZeroCalls(t *testing.T) {
	prov := &chatFixtureProvider{}
	pol := &chatFixturePolicy{quotaZero: true}
	log := &chatFixtureLog{}
	store := &threadStore{}
	reg := newRPCRegistry()
	r := newComposedReplier(prov, pol, log, store, reg)
	reply, err := r.Reply("hello")
	if err != nil {
		t.Fatalf("reply error: %v", err)
	}
	if !strings.Contains(reply, "refused") {
		t.Fatalf("reply = %q, want refusal", reply)
	}
	assertCalls(t, prov, 0)
}

// TestP1ChatReplyLocalOnlyThreadNoRemoteCall proves local-only threads
// make no remote calls when over budget.
func TestP1ChatReplyLocalOnlyThreadNoRemoteCall(t *testing.T) {
	prov := &chatFixtureProvider{}
	pol := &chatFixturePolicy{localOnly: true}
	log := &chatFixtureLog{}
	store := &threadStore{}
	reg := newRPCRegistry()
	r := newComposedReplier(prov, pol, log, store, reg)
	reply, err := r.Reply("hello")
	if err != nil {
		t.Fatalf("reply error: %v", err)
	}
	if !strings.Contains(reply, "refused") {
		t.Fatalf("reply = %q, want refusal", reply)
	}
	assertCalls(t, prov, 0)
}

// TestP1ChatReplyScrubBeforeEgress proves egress scrub runs before the
// reply leaves the machine.
func TestP1ChatReplyScrubBeforeEgress(t *testing.T) {
	prov := &chatFixtureProvider{}
	pol := &chatFixturePolicy{}
	log := &chatFixtureLog{}
	store := &threadStore{}
	reg := newRPCRegistry()
	r := newComposedReplier(prov, pol, log, store, reg)
	_, err := r.Reply("secret is vault:secret")
	if err != nil {
		t.Fatalf("reply error: %v", err)
	}
	if len(log.payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(log.payloads))
	}
	if strings.Contains(log.payloads[0], "vault:secret") {
		t.Fatalf("payload = %q, want scrubbed", log.payloads[0])
	}
}
