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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
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

	paths := auditTestPaths(t)
	pol, err := wirePolicy(ctx, store, clock, nil, paths)
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
	pol2, err := wirePolicy(ctx, store, clock, nil, paths)
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
		testkit.NewFrozenClock(policyTestEpoch), cfg, auditTestPaths(t)); err == nil {
		t.Fatal("wirePolicy accepted an unknown autonomy profile; it must refuse rather than fall back")
	}
}

// TestWirePolicyRequiresAStore proves the store is not optional. An engine
// with nowhere to read grants or deny rows from could only answer by
// assuming both are empty.
func TestWirePolicyRequiresAStore(t *testing.T) {
	if _, err := wirePolicy(context.Background(), nil,
		testkit.NewFrozenClock(policyTestEpoch), nil, auditTestPaths(t)); err == nil {
		t.Fatal("wirePolicy built an engine with no store")
	}
}

// TestDaemonHasSingleAuditLog proves the daemon's audit writers share one
// redacting log per store. Each wiring is run on a store no log was opened
// over: the memo holding a log for it afterwards is the proof the wiring
// took its log from openAuditLog, and a second policy wiring (a reload)
// must hand back the same pointer. The policy wiring's log is the one its
// router, approval queue and supervision writers were built over.
func TestDaemonHasSingleAuditLog(t *testing.T) {
	ctx := context.Background()
	resetProcessFirewall(t)
	paths := fakeDaemonPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := testkit.NewFrozenClock(policyTestEpoch)
	memoOf := func(store *storetest.MemStore) *audit.Log {
		auditLogMemo.mu.Lock()
		defer auditLogMemo.mu.Unlock()
		return auditLogMemo.logs[store]
	}

	conductorStore := storetest.NewMemStore()
	if memoOf(conductorStore) != nil {
		t.Fatal("a log existed before any wiring ran")
	}
	if _, err := wireConductorExecute(ctx, rpc.NewRegistry(), daemon.NewManifest(nil, clock), paths, clock, conductorStore, nil); err != nil {
		t.Fatalf("wireConductorExecute: %v", err)
	}
	if memoOf(conductorStore) == nil {
		t.Fatal("the conductor wiring built its audit writer without openAuditLog")
	}

	hooksStore := storetest.NewMemStore()
	if err := wireCompletionHookPack(ctx, rpc.NewRegistry(), hooksStore, clock, events.New(hooksStore, clock), paths); err != nil {
		t.Fatalf("wireCompletionHookPack: %v", err)
	}
	if memoOf(hooksStore) == nil {
		t.Fatal("the hooks wiring built its audit writer without openAuditLog")
	}

	store := storetest.NewMemStore()
	first, err := wirePolicy(ctx, store, clock, nil, paths)
	if err != nil {
		t.Fatalf("wirePolicy: %v", err)
	}
	reloaded, err := wirePolicy(ctx, store, clock, nil, paths)
	if err != nil {
		t.Fatalf("wirePolicy (reload): %v", err)
	}
	if first.Audit == nil || first.Audit != reloaded.Audit || first.Audit != memoOf(store) {
		t.Fatalf("policy audit logs = %p, %p after a reload; memo holds %p: want one pointer", first.Audit, reloaded.Audit, memoOf(store))
	}
	shared, err := openAuditLog(store, clock, nil, paths)
	if err != nil || shared != first.Audit {
		t.Fatalf("openAuditLog over the policy store = (%p, %v), want the policy wiring's log %p", shared, err, first.Audit)
	}
}
