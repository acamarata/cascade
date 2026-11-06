package daemon

// Purpose: proves steps 4 and 5 of the topology pass: quota-window
// observations become provider-status buckets keyed per subscription
// domain, stale or unknown readings and failed reads write nothing, and the
// WindowAccountant never overwrites an authoritative bucket. Also holds the
// shared topology rig. Real stores in t.TempDir; no network.
// SPORT: internal/daemon (ADD, P1-TOP-06e).

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

var topoEpoch = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type topoRig struct {
	g      *fleetRig
	r      *FleetRuntime
	ctx    context.Context
	usage  *usage.Manager
	reader *usage.Reader
	deps   TopologyPassDeps
}

// newTopoRig starts a fleet runtime over configTOML with a fresh HOME and
// CASCADE_HOME, fake capacity tickers and a migrated provider usage store.
func newTopoRig(t *testing.T, configTOML string) *topoRig {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	capT, aggT := newFleetFakeTicker(), newFleetFakeTicker()
	savedCap, savedAgg := newCapacityTicker, newAggregateTicker
	newCapacityTicker = func(time.Duration) runtime.Ticker { return capT }
	newAggregateTicker = func(time.Duration) runtime.Ticker { return aggT }
	t.Cleanup(func() { newCapacityTicker, newAggregateTicker = savedCap, savedAgg })
	g := newFleetRig(t, configTOML)
	g.clock.Set(topoEpoch)
	cfg, err := runtime.Load(g.ctx, runtime.LoadOptions{Path: g.deps.Paths.ConfigPath(), Getenv: func(string) string { return "" },
		Environ: func() []string { return nil }, Warn: func(string, ...interface{}) {}})
	if err != nil {
		t.Fatalf("runtime.Load: %v", err)
	}
	g.deps.Config = cfg
	r := g.start(t)
	capT.awaitPoll(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "provider-usage.db"))
	if err != nil {
		t.Fatalf("open usage db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := usage.ApplyMigrationSchema(g.ctx, db, migrate.SQLiteEmitter{}, g.clock, "", ""); err != nil {
		t.Fatalf("usage migration: %v", err)
	}
	m := usage.NewManager(db, g.clock)
	rd := usage.NewReader(m)
	return &topoRig{g: g, r: r, ctx: g.ctx, usage: m, reader: rd, deps: TopologyPassDeps{
		Registry: r.CapacityParts().Registry, AgentLanes: func() []topology.AgentLaneRecord { return nil },
		Usage: rd, QuotaWindows: rd.QuotaWindows, Bus: g.deps.Bus, Clock: g.clock,
	}}
}

// seedSubscription adds an oauth registry provider with one lane: its domain
// is "<name>:subscription".
func (k *topoRig) seedSubscription(t *testing.T, name string) topology.DomainID {
	t.Helper()
	reg := k.deps.Registry
	p := registry.ProviderRecord{Name: name, Driver: registry.DriverAnthropic, Auth: registry.AuthOAuth,
		AuthRef: registry.VaultKeyRef("vault://" + name + "/oauth"), AccountKind: registry.AccountPersonal, Tier: registry.TierMid}
	if err := reg.UpsertProvider(k.ctx, p); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	l := registry.LaneRecord{LaneName: name, ProviderName: name, ModelFilter: []string{"claude-opus"},
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable}
	if err := reg.UpsertLane(k.ctx, l); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	return topology.DomainID(name + ":subscription")
}

func (k *topoRig) bind(t *testing.T) {
	t.Helper()
	if err := k.r.BindTopologyPass(k.deps); err != nil {
		t.Fatalf("BindTopologyPass: %v", err)
	}
}

func (k *topoRig) pass(t *testing.T) topology.Report {
	t.Helper()
	rep, err := k.r.ReconcileNow(k.ctx)
	if err != nil {
		t.Fatalf("ReconcileNow: %v", err)
	}
	return rep
}

func (k *topoRig) bucket(t *testing.T, id topology.DomainID, dim string) topology.Bucket {
	t.Helper()
	b, err := k.r.Quota().GetBucket(k.ctx, id, dim)
	if err != nil {
		t.Fatalf("GetBucket(%s, %s): %v", id, dim, err)
	}
	return b
}

func (k *topoRig) record(t *testing.T, provider string, w topology.BucketWindow, pct float64) usage.QuotaObservation {
	t.Helper()
	obs := usage.QuotaObservation{Provider: provider, Window: w, Used: usage.QuotaUnknown, Limit: usage.QuotaUnknown,
		UtilizationPct: pct, ResetsAt: topoEpoch.Add(3 * time.Hour), ObservedAt: k.g.clock.Now()}
	if err := k.usage.RecordQuota(k.ctx, obs); err != nil {
		t.Fatalf("RecordQuota: %v", err)
	}
	return obs
}

func TestObservationBucketsFromUsageStore(t *testing.T) {
	k := newTopoRig(t, "")
	one, two := k.seedSubscription(t, "anthropic-1"), k.seedSubscription(t, "anthropic-2")
	injected := errors.New("injected quota read failure")
	fail := ""
	k.deps.QuotaWindows = func(ctx context.Context, p string) ([]usage.QuotaObservation, error) {
		if p == fail {
			return nil, injected
		}
		return k.reader.QuotaWindows(ctx, p)
	}
	k.bind(t)
	five := k.record(t, string(one), topology.BucketWindow5h, 40)
	k.record(t, string(one), topology.BucketWindowWeek, 90)
	k.pass(t)
	s5 := k.bucket(t, one, topology.DimensionSession5h)
	if math.Abs(s5.RemainingFraction-0.60) > 1e-9 || s5.Source != topology.SourceProviderStatus || s5.Confidence != 1.0 ||
		!s5.ResetAt.Equal(five.ResetsAt) || !s5.ObservedAt.Equal(five.ObservedAt) {
		t.Fatalf("session_5h = %+v, want 0.60 provider-status at confidence 1.0 with the observation's times", s5)
	}
	if wk := k.bucket(t, one, topology.DimensionWeeklyShared); math.Abs(wk.RemainingFraction-0.10) > 1e-9 || wk.Source != topology.SourceProviderStatus {
		t.Fatalf("weekly_shared = %+v, want 0.10 provider-status", wk)
	}
	for _, dim := range []string{topology.DimensionSession5h, topology.DimensionWeeklyShared} {
		if b := k.bucket(t, two, dim); b.Source == topology.SourceProviderStatus {
			t.Fatalf("%s of %s = %+v: two subscription domains shared a row", dim, two, b)
		}
	}
	before5, beforeWk := k.bucket(t, one, topology.DimensionSession5h), k.bucket(t, one, topology.DimensionWeeklyShared)
	if err := k.usage.RecordQuotaFailure(k.ctx, string(one), topology.BucketWindow5h, "timeout"); err != nil {
		t.Fatalf("RecordQuotaFailure: %v", err)
	}
	k.record(t, string(one), topology.BucketWindowWeek, usage.QuotaUnknown)
	k.pass(t)
	if a5, aWk := k.bucket(t, one, topology.DimensionSession5h), k.bucket(t, one, topology.DimensionWeeklyShared); !reflect.DeepEqual(a5, before5) || !reflect.DeepEqual(aWk, beforeWk) {
		t.Fatalf("stale or unknown readings changed buckets:\n%+v -> %+v\n%+v -> %+v", before5, a5, beforeWk, aWk)
	}
	beforeTwo := k.bucket(t, two, topology.DimensionSession5h)
	k.record(t, string(two), topology.BucketWindow5h, 10)
	fail = string(two)
	_, err := k.r.ReconcileNow(k.ctx)
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), `step "observation-buckets"`) || !strings.Contains(err.Error(), injected.Error()) {
		t.Fatalf("a QuotaWindows error = %v, want the injected error named with its step", err)
	}
	if got := k.bucket(t, two, topology.DimensionSession5h); !reflect.DeepEqual(got, beforeTwo) {
		t.Fatalf("a failed read wrote the domain: %+v -> %+v", beforeTwo, got)
	}
}

func TestWindowAccountantPassNeverOverwritesAuthoritative(t *testing.T) {
	k := newTopoRig(t, "")
	dom := k.seedSubscription(t, "anthropic-1")
	k.bind(t)
	k.record(t, string(dom), topology.BucketWindow5h, 25)
	k.pass(t)
	cli := k.bucket(t, dom, topology.DimensionWeeklyModelFraction)
	cli.Source, cli.Confidence, cli.RemainingFraction, cli.ObservedAt = topology.SourceCLIObservation, 0.9, 0.5, k.g.clock.Now()
	if err := k.r.Quota().UpsertBucket(k.ctx, dom, topology.DimensionWeeklyModelFraction, cli); err != nil {
		t.Fatalf("UpsertBucket(cli-observation): %v", err)
	}
	authCLI, authStatus := k.bucket(t, dom, topology.DimensionWeeklyModelFraction), k.bucket(t, dom, topology.DimensionSession5h)
	beforeMonthly := k.bucket(t, dom, topology.DimensionMonthly)
	k.g.clock.Advance(time.Hour)
	k.pass(t)
	if got := k.bucket(t, dom, topology.DimensionWeeklyModelFraction); !reflect.DeepEqual(got, authCLI) {
		t.Fatalf("cli-observation bucket overwritten: %+v -> %+v", authCLI, got)
	}
	if got := k.bucket(t, dom, topology.DimensionSession5h); !reflect.DeepEqual(got, authStatus) {
		t.Fatalf("provider-status bucket overwritten: %+v -> %+v", authStatus, got)
	}
	monthly := k.bucket(t, dom, topology.DimensionMonthly)
	if monthly.Source != topology.SourceUnknown && monthly.Source != topology.SourceUserEstimate {
		t.Fatalf("monthly = %+v, want an estimate or unknown bucket", monthly)
	}
	if !monthly.ObservedAt.After(beforeMonthly.ObservedAt) {
		t.Fatalf("monthly was not rewritten by the accountant: %+v -> %+v", beforeMonthly, monthly)
	}
}
