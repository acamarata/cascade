//go:build !windows

// Purpose: the composed daemon's capacity Fleet end to end over a fresh
//
//	HOME: providers seeded through the registry API, the production writers
//	topology.Reconcile and QuotaStore.UpsertBucket, then Fleet().Lease and
//	Release, and the registry-lane to quota-domain mapping DomainOf uses.
//	No topology row is written by test SQL; SQL here only counts rows.
//
// SPORT: cmd/cascade/daemon (ADD, P1-CAP-05).
package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// capDaemon is one composed daemon: its registry, mux and fleet runtime.
type capDaemon struct {
	c     composedForTest
	bus   *events.Bus
	rt    *daemon.FleetRuntime
	parts daemon.FleetCapacityParts
}

// composeCapacity composes the daemon over a fresh HOME and returns the one
// fleet runtime every registration saw.
func composeCapacity(t *testing.T) capDaemon {
	t.Helper()
	var atFleet *daemon.FleetRuntime
	addTestRegistration(t, daemonRegistration{Name: "test-capacity-fleet-phase", Phase: phaseFleet, Order: 998,
		Wire: func(w *daemonWiring) error { atFleet = w.Deps.Fleet; return nil }})
	seen := observeWiring(t)
	c := composeForTest(t)
	w := <-seen
	if w.Deps.Fleet == nil || w.Deps.Fleet != atFleet || w.Deps.Fleet.Fleet() == nil {
		t.Fatalf("Deps.Fleet = %p (fleet phase saw %p), want one runtime with a non-nil Fleet()", w.Deps.Fleet, atFleet)
	}
	return capDaemon{c: c, bus: w.Bus, rt: w.Deps.Fleet, parts: w.Deps.Fleet.CapacityParts()}
}

// seedCapProvider stores one provider and its lanes through the registry API.
func seedCapProvider(t *testing.T, reg *registry.Registry, rec registry.ProviderRecord, lanes ...registry.LaneRecord) {
	t.Helper()
	ctx := context.Background()
	if rec.AuthRef == "" {
		rec.AuthRef = registry.VaultKeyRef("provider." + rec.Name + ".key")
	}
	rec.AccountKind, rec.HealthStatus = registry.AccountPersonal, registry.HealthHealthy
	rec.Capabilities.CompliancePosture = provider.NewCompliancePosture(nil, false, true, nil, "", false)
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", rec.Name, err)
	}
	for _, l := range lanes {
		l.ProviderName, l.Weight, l.State = rec.Name, 1, registry.LaneStateAvailable
		if l.Capacity == "" {
			l.Capacity = registry.CapacityAPICredit
		}
		if err := reg.UpsertLane(ctx, l); err != nil {
			t.Fatalf("UpsertLane(%s): %v", l.LaneName, err)
		}
	}
}

// seedLocalProvider is a controller-local provider: local-only work may use it.
func seedLocalProvider(t *testing.T, d capDaemon) {
	t.Helper()
	seedCapProvider(t, d.parts.Registry, registry.ProviderRecord{Name: "local-llm", Driver: registry.DriverOllama,
		BaseURL: "http://127.0.0.1:11434", Auth: registry.AuthKey, Tier: registry.TierFree},
		registry.LaneRecord{LaneName: "local-llm-1"})
}

// reconcileAndFund runs the production writers: Reconcile, then roomy
// rpm/tpm/rpd buckets on domain.
func reconcileAndFund(t *testing.T, d capDaemon, domain topology.DomainID) {
	t.Helper()
	ctx, clock := context.Background(), runtime.SystemClock{}
	store := topology.NewStore(d.rt.JobsDB())
	if _, err := topology.Reconcile(ctx, store, d.parts.Registry, clock); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	dom, err := store.GetQuotaDomain(ctx, domain)
	if err != nil {
		t.Fatalf("GetQuotaDomain(%s): %v", domain, err)
	}
	acct, err := store.GetAccount(ctx, dom.AccountRef)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
		b := topology.Bucket{Name: dim, Limit: 1_000_000, RemainingFraction: 1, Window: topology.BucketWindowDay,
			Source: topology.SourceProviderStatus, Confidence: 1, LimitScopeID: topology.ResolveScope(acct, dom),
			CapacityObserved: 1_000_000, WindowID: "w1", Version: 1, ObservedAt: clock.Now()}
		if err := d.rt.Quota().UpsertBucket(ctx, dom.ID, dim, b); err != nil {
			t.Fatalf("UpsertBucket(%s): %v", dim, err)
		}
	}
}

// awaitLane waits, bounded, until a refresh tick put lane in the snapshot,
// and until the admission sampler has a fresh sample.
func awaitLane(t *testing.T, d capDaemon, lane conductor.LaneID) {
	t.Helper()
	awaitFreshAdmissionRow(t, d.c.Opts.Manifest)
	deadline := time.Now().Add(testDaemonBound)
	for time.Now().Before(deadline) {
		if _, ok := d.parts.Compositor.Snapshot().Lanes[lane]; ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("lane %q never reached the capacity snapshot", lane)
}

// localLease leases local-only work (no job, so the data class floor is local-only).
func localLease(d capDaemon) (capacity.Lease, error) {
	req := capacity.ResourceRequest{Intent: "p1-cap-05 test", TaskClass: conductor.TaskClassCode, MinQuality: capacity.QualityStandard,
		Deadline: capacity.DeadlineInteractive, Sensitivity: provider.SensitivityLocalOnly, RiskClass: jobs.RiskClassLow}
	return d.rt.Fleet().Lease(context.Background(), req, capacity.DispatchRef{ProjectID: "project-cap", HydratedTokens: 100, Requests: 1}, 1)
}

// rowCount counts rows of table on the runtime handle (a read only).
func rowCount(t *testing.T, d capDaemon, table string) int {
	t.Helper()
	var n int
	if err := d.rt.JobsDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// isNoQuotaDomain reports capacity.ErrNoQuotaDomain in err's chain by identity.
func isNoQuotaDomain(err error) bool {
	for err != nil {
		if err == capacity.ErrNoQuotaDomain {
			return true
		}
		if multi, ok := err.(interface{ Unwrap() []error }); ok {
			return slices.ContainsFunc(multi.Unwrap(), isNoQuotaDomain)
		}
		err = errors.Unwrap(err)
	}
	return false
}

func TestFleetComposedAtDaemonStart(t *testing.T) {
	d := composeCapacity(t)
	methods := d.c.Registry.Methods()
	if !slices.Contains(methods, capacity.MethodFleetCapacity) || slices.Contains(methods, "fleet.lease") {
		t.Fatalf("methods contain fleet.capacity=%v fleet.lease=%v; want true, false",
			slices.Contains(methods, capacity.MethodFleetCapacity), slices.Contains(methods, "fleet.lease"))
	}
	seedLocalProvider(t, d)
	reconcileAndFund(t, d, "local-llm:api")
	awaitLane(t, d, "local-llm-1")
	lease, err := localLease(d)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if lease.LaneID != "local-llm-1" || lease.NodeID != d.parts.ControllerNodeID {
		t.Fatalf("lease = %+v, want lane local-llm-1 on the controller", lease)
	}
	if err := d.rt.Fleet().Release(context.Background(), lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
	rsv, found, err := d.rt.Fleet().ReservationByExecution(context.Background(), lease.ExecutionID)
	if err != nil || !found || rsv.State == economics.ReservationHeld {
		t.Fatalf("reservation after Release = %+v (found %v, %v), want a non-held row", rsv, found, err)
	}
}

func TestLeaseRefusesOnUnreconciledDaemon(t *testing.T) {
	d := composeCapacity(t)
	seedLocalProvider(t, d)
	awaitLane(t, d, "local-llm-1")
	if _, err := localLease(d); !isNoQuotaDomain(err) {
		t.Fatalf("Lease before Reconcile = %v, want capacity.ErrNoQuotaDomain", err)
	}
	for _, table := range []string{"jobs_reservation", "jobs_scheduler_decisions"} {
		if n := rowCount(t, d, table); n != 0 {
			t.Fatalf("%s holds %d rows after a refused lease, want 0", table, n)
		}
	}
	reconcileAndFund(t, d, "local-llm:api")
	lease, err := localLease(d)
	if err != nil {
		t.Fatalf("Lease after Reconcile: %v", err)
	}
	if err := d.rt.Fleet().Release(context.Background(), lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestDomainOfRegistryLaneMappingTable(t *testing.T) {
	d := composeCapacity(t)
	reg, ctx := d.parts.Registry, context.Background()
	api := func(name string) registry.ProviderRecord {
		return registry.ProviderRecord{Name: name, Driver: registry.DriverOpenAICompat, BaseURL: "https://example.invalid/v1", Auth: registry.AuthKey, Tier: registry.TierMid}
	}
	sub := api("claude-sub")
	sub.Driver, sub.Auth = registry.DriverAnthropic, registry.AuthOAuth
	seedCapProvider(t, reg, api("anthropic"), registry.LaneRecord{LaneName: "anthropic-main"})
	seedCapProvider(t, reg, sub, registry.LaneRecord{LaneName: "claude-sub-1"})
	seedCapProvider(t, reg, api("teamprov"), registry.LaneRecord{LaneName: "team-lane", PoolMembership: "team"})
	seedCapProvider(t, reg, api("openai"), registry.LaneRecord{LaneName: "openai-a"}, registry.LaneRecord{LaneName: "openai-b"})
	seedCapProvider(t, reg, api("quarprov"), registry.LaneRecord{LaneName: "quar-lane"})
	seedCapProvider(t, reg, api("retprov"), registry.LaneRecord{LaneName: "ret-lane"})
	store := topology.NewStore(d.rt.JobsDB())
	if _, err := topology.Reconcile(ctx, store, reg, runtime.SystemClock{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	quarantineCredential(t, store, "quar-lane")
	retireDomainLanes(t, store, "retprov:api")
	seedCapProvider(t, reg, api("lateprov"), registry.LaneRecord{LaneName: "late-lane"}) // never reconciled

	domainOf := d.parts.Deps.DomainOf
	for lane, want := range map[conductor.LaneID]string{
		"anthropic-main": "anthropic:api", "claude-sub-1": "claude-sub:subscription",
		"team-lane": "pool:team", "openai-a": "openai:api", "openai-b": "openai:api",
	} {
		if got, err := domainOf(ctx, lane); err != nil || got != want {
			t.Errorf("DomainOf(%s) = %q, %v; want %q", lane, got, err, want)
		}
	}
	kinds := map[topology.DomainID]topology.QuotaDomainKind{"anthropic:api": topology.QuotaDomainAPIProject,
		"claude-sub:subscription": topology.QuotaDomainSubscriptionWindow, "pool:team": topology.QuotaDomainSharedPool}
	for id, want := range kinds {
		if dom, err := store.GetQuotaDomain(ctx, id); err != nil || dom.Kind != want {
			t.Errorf("domain %s = %+v, %v; want kind %s", id, dom, err, want)
		}
	}
	for _, lane := range []conductor.LaneID{"late-lane", "quar-lane", "ret-lane"} {
		if got, err := domainOf(ctx, lane); got != "" || !isNoQuotaDomain(err) {
			t.Errorf("DomainOf(%s) = %q, %v; want capacity.ErrNoQuotaDomain", lane, got, err)
		}
	}
}

// quarantineCredential marks a reconciled credential quarantined through the store API.
func quarantineCredential(t *testing.T, store *topology.Store, id topology.CredentialID) {
	t.Helper()
	cred, err := store.GetCredential(context.Background(), id)
	if err != nil {
		t.Fatalf("GetCredential(%s): %v", id, err)
	}
	cred.Health, cred.QuarantineReason = topology.CredentialQuarantined, "test"
	if err := store.UpsertCredential(context.Background(), cred); err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}
}

// retireDomainLanes retires every live topology lane of domain through the store API.
func retireDomainLanes(t *testing.T, store *topology.Store, domain topology.DomainID) {
	t.Helper()
	lanes, err := store.ListLanes(context.Background(), false)
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	for _, l := range lanes {
		if l.QuotaDomainRef == domain {
			if err := store.RetireLane(context.Background(), l.ID, time.Now()); err != nil {
				t.Fatalf("RetireLane(%s): %v", l.ID, err)
			}
		}
	}
}
