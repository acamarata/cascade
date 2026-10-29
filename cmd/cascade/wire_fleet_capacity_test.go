//go:build !windows

// Purpose: the fleet-capacity wiring: fleet.capacity and its topic are
//
//	owner-only on the composed daemon, the composed FleetDeps are the
//	production learn, selector, domain and placement pieces, a registry change
//	reaches an owner subscriber of the topic within a refresh tick, and the
//	refresh loop is joined at shutdown before the runtime closes.
//
// SPORT: cmd/cascade/daemon (ADD, P1-CAP-05).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sessionPrincipal is a non-owner caller.
var sessionPrincipal = principal.Principal{ID: "cap-test", Kind: principal.KindSession, Scope: "project:test"}

func TestFleetCapacityAccessClass(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	d := composeCapacity(t)
	req := &rpc.Request{JSONRPC: "2.0", Method: capacity.MethodFleetCapacity}
	p := sessionPrincipal
	_, eo := d.c.Registry.Dispatch(principal.WithCaller(context.Background(), principal.Caller{Principal: &p}), req)
	if eo == nil || fmt.Sprint(eo.Data) != fmt.Sprint(map[string]string{"kind": cascade.KindPermissionDenied.String()}) {
		t.Fatalf("principal call = %+v, want KindPermissionDenied", eo)
	}
	res, eo := d.c.Registry.Dispatch(principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), req)
	if eo != nil {
		t.Fatalf("owner call = %+v, want served", eo)
	}
	if snap, ok := res.(capacity.FleetSnapshot); !ok || snap.GeneratedAt.IsZero() {
		t.Fatalf("owner result = %T %+v, want a snapshot with generated_at", res, res)
	}
}

func TestFleetCapacityProductionDeps(t *testing.T) {
	d := composeCapacity(t)
	p, deps := d.parts, d.parts.Deps
	if p.Scorer == nil || deps.Scorer == nil || deps.Scorer("global") != p.Scorer.For("global") {
		t.Fatal("FleetDeps.Scorer is not learn.(*SQLiteCapabilityScorer).For")
	}
	if est, ok := p.Policy.Estimator.(*learn.SQLiteEstimateSource); !ok || est != p.Estimates {
		t.Fatalf("policy estimator = %T, want the learn SQLiteEstimateSource", p.Policy.Estimator)
	}
	if _, ok := p.Policy.Probes.(*capacity.DefaultProbeAdmission); !ok || deps.Selector != capacity.NewTierPolicySelector(p.Policy) {
		t.Fatal("FleetDeps.Selector is not NewTierPolicySelector over NewTierPolicy(estimate, DefaultProbeAdmission, clock)")
	}
	if rec, ok := deps.Decisions.(daemon.LearnDecisionRecorder); !ok {
		t.Fatalf("FleetDeps.Decisions = %T, want daemon.LearnDecisionRecorder", deps.Decisions)
	} else if _, ok := rec.Writer.(learn.SQLiteSchedulerDecisionWriter); !ok {
		t.Fatalf("decision writer = %T, want learn.SQLiteSchedulerDecisionWriter", rec.Writer)
	}
	if p.Domains.Topology == nil || p.Placer.Records != p.Nodes || deps.ControllerNodeID != p.ControllerNodeID || p.ControllerNodeID == "" {
		t.Fatalf("domains %v placer %+v controller %q/%q: want the topology lookup, the enrolled store and the binding's controller", p.Domains, p.Placer, deps.ControllerNodeID, p.ControllerNodeID)
	}
	if deps.Reserver != d.rt.Reserver() || deps.Jobs != d.rt.Jobs() {
		t.Fatal("FleetDeps does not share the runtime's Reserver and jobs store")
	}
	for _, table := range []string{"jobs_scheduler_decisions", "jobs_capability_score_observations"} {
		if n := rowCount(t, d, table); n != 0 {
			t.Fatalf("%s has %d rows on a fresh daemon", table, n)
		}
	}
	info, err := os.Stat(p.SaltPath)
	if err != nil || info.Mode().Perm() != 0o600 || !strings.HasSuffix(p.SaltPath, filepath.Join("data", "learn", "aggregate_salt")) {
		t.Fatalf("aggregate salt = %v, %v; want a 0600 file at <data>/learn/aggregate_salt", info, err)
	}
}

// TestFleetCapacityTopic checks the mux carries fleet.capacity and that a registry change
// reaches the "fleet" namespace as a fleet.capacity_changed delta naming the new provider
// (the owner-only socket path is TestFleetCapacityTopicOwnerOnly).
func TestFleetCapacityTopic(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	d := composeCapacity(t)
	if h, ok := d.c.Events.Topics[capacity.FleetCapacityTopic]; !ok || h == nil {
		t.Fatalf("SSEMux.Topics[%q] missing: %v", capacity.FleetCapacityTopic, d.c.Events.Topics)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testDaemonBound)
	defer cancel()
	sub, err := d.bus.SubscribeFromHead(ctx, "fleet", "cap-topic-test", 16)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	for i := 0; ; i++ {
		seedCapProvider(t, d.parts.Registry, registry.ProviderRecord{Name: fmt.Sprintf("topic-prov-%d", i), Driver: registry.DriverOpenAICompat,
			BaseURL: "https://example.invalid/v1", Auth: registry.AuthKey, Tier: registry.TierMid})
		select {
		case ev := <-sub.Events:
			if ev.Kind != capacity.FleetCapacityChangedKind {
				t.Fatalf("event kind %s on the fleet namespace, want fleet.capacity_changed", ev.Kind)
			}
			if !strings.Contains(string(ev.Payload), "topic-prov-") {
				continue // a refresh tick before the seed (a load jitter delta): keep reading
			}
			return
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			t.Fatal("no fleet.capacity_changed reached the bus")
		}
	}
}

func TestFleetCapacityRefreshJoined(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w, cancel, done := startTestDaemon(t, newRunTestDeps(t, nil), observeWiring(t))
	parts := w.Deps.Fleet.CapacityParts()
	row := func() daemon.SubsystemStatus {
		for _, s := range w.Manifest.Snapshot() {
			if s.Name == "fleet-capacity-refresh" {
				return s
			}
		}
		return daemon.SubsystemStatus{}
	}
	if r := row(); r.State != daemon.SubsystemRunning {
		t.Fatalf("fleet-capacity-refresh = %+v, want running", r)
	}
	seedCapProvider(t, parts.Registry, registry.ProviderRecord{Name: "joined-prov", Driver: registry.DriverOpenAICompat,
		BaseURL: "https://example.invalid/v1", Auth: registry.AuthKey, Tier: registry.TierMid})
	deadline := time.Now().Add(testDaemonBound)
	for snap := parts.Compositor.Snapshot(); snap.Providers["joined-prov"].ProfileRef == "" || len(snap.Nodes) == 0; snap = parts.Compositor.Snapshot() {
		if time.Now().After(deadline) {
			t.Fatal("the refresh never read the registry and the sampler into the snapshot")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	awaitDaemonReturn(t, done)
	if r := row(); r.State != daemon.SubsystemSkipped || r.Detail != "stopped" {
		t.Fatalf("fleet-capacity-refresh after the run = %+v, want skipped/stopped (joined)", r)
	}
	if _, err := parts.Registry.ListProviders(context.Background()); err == nil {
		t.Fatal("the registry handle is still open after shutdown: the runtime was not closed after the join")
	}
}
