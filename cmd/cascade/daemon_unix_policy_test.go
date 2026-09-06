//go:build !windows

// Purpose: the proof that wirePolicy — the daemon's policy composition
//
//	root — builds an engine over the B-layer seams and not over the
//	package defaults. The distinction is invisible to a construction
//	check: DefaultDenyList and a StoreDenyList with no rows answer
//	identically until a row exists, so each assertion below drives a
//	decision the default could not have produced.
//
// SPORT: cmd/cascade/daemon (CHANGED — policy composition root
//
//	verification).
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// policyTestEpoch is the instant the frozen clock in this file starts at.
var policyTestEpoch = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

// routedReadAction is a read-class action against the registered
// scheduler capability. Its command text classifies at L0 and the
// capability's own class raises it to L1, which the shipped baseline
// profile allows.
func routedReadAction() routing.Action {
	return routing.Action{
		Subject:    schedulerSubject(),
		Capability: schedulerCapability,
		Verb:       "scheduler.fired",
		Command:    "cat notes.txt",
		Params:     []byte("{}"),
		Origin:     routing.OriginScheduler,
		Ref:        "job-1",
		Summary:    "policy composition root probe",
	}
}

// TestDaemonPolicyEngineUsesBLayerSeams drives three decisions through the
// router wirePolicy returns.
//
// The first proves the registry, the profile and the engine are live at
// all. The second proves the deny-list is the STORE-backed one: the row is
// written through the B-layer store and the very next decision refuses on
// it, naming the configured row rather than the unconditional rung
// sentence. DefaultDenyList holds no rows and could never produce that
// answer, so this assertion fails if the engine falls back to it.
func TestDaemonPolicyEngineUsesBLayerSeams(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(policyTestEpoch)

	pol, err := wirePolicy(ctx, store, clock, nil)
	if err != nil {
		t.Fatalf("wirePolicy: %v", err)
	}
	verdict, _, err := pol.Router.RouteAction(ctx, routedReadAction())
	if err != nil {
		t.Fatalf("route read action: %v", err)
	}
	if verdict != policy.VerdictAllow {
		t.Fatalf("verdict = %v, want allow; the boot capability registry or the profile is not live", verdict)
	}

	denyList, err := policy.NewStoreDenyList(store)
	if err != nil {
		t.Fatalf("deny-list: %v", err)
	}
	if err := denyList.Add(ctx, "cat *", policy.ClassRead); err != nil {
		t.Fatalf("add deny row: %v", err)
	}
	// A SECOND wiring over the same store, so the row is read at boot the
	// way a restarted daemon reads it.
	pol2, err := wirePolicy(ctx, store, clock, nil)
	if err != nil {
		t.Fatalf("wirePolicy (second): %v", err)
	}
	verdict, _, err = pol2.Router.RouteAction(ctx, routedReadAction())
	if verdict != policy.VerdictDeny {
		t.Fatalf("verdict = %v, want deny: the engine is not reading the store-backed deny-list", verdict)
	}
	if err == nil || !strings.Contains(err.Error(), "the configured deny-list names this action") {
		t.Fatalf("refusal = %v, want the configured deny-list row; a Default* deny-list holds no rows", err)
	}
}

// TestWirePolicyRefusesAMalformedPolicySection proves the composition root
// fails closed on a policy section it cannot read. A daemon that started
// with an unreadable [policy] section would be running on a profile nobody
// chose, which is the one config error that must stop a boot.
func TestWirePolicyRefusesAMalformedPolicySection(t *testing.T) {
	cfg := &runtime.Config{Extra: map[string]any{
		"policy": map[string]any{"autonomy_profile": "unlimited"},
	}}
	if _, err := wirePolicy(context.Background(), storetest.NewMemStore(),
		testkit.NewFrozenClock(policyTestEpoch), cfg); err == nil {
		t.Fatal("wirePolicy accepted an unknown autonomy profile; it must refuse rather than fall back")
	}
}

// TestWirePolicyRequiresAStore proves the store is not optional. An engine
// with nowhere to read grants or deny rows from could only answer by
// assuming both are empty.
func TestWirePolicyRequiresAStore(t *testing.T) {
	if _, err := wirePolicy(context.Background(), nil,
		testkit.NewFrozenClock(policyTestEpoch), nil); err == nil {
		t.Fatal("wirePolicy built an engine with no store")
	}
}
