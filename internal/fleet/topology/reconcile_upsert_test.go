package topology_test

// Purpose: P1-TOP-06 acceptance for Reconcile's reconcile-scoped writers,
// lane retirement and idempotent passes, over real file-backed SQLite
// registry, topology and reservation databases in t.TempDir() with a fresh
// HOME, USERPROFILE and CASCADE_HOME. Failures are injected only from this
// file (a SQLite trigger), never through a production seam. External
// package: the retirement case drives economics.Reserver.Release, and
// economics imports topology.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var t0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type rig struct {
	db    *sql.DB
	store *topology.Store
	reg   *registry.Registry
}

func openDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	must(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newRig(t *testing.T) *rig {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, home)
	}
	ctx, tdb, rdb := context.Background(), openDB(t, "topology.db"), openDB(t, "registry.db")
	must(t, topology.ApplyMigrationSchema(ctx, tdb, migrate.SQLiteEmitter{}, fixedClock{t0}, "", ""))
	must(t, registry.ApplyMigrationSchema(ctx, rdb, migrate.SQLiteEmitter{}, fixedClock{t0}, "", ""))
	return &rig{db: tdb, store: topology.NewStore(tdb), reg: registry.NewRegistry(rdb, fixedClock{t0})}
}

// seed upserts provider name (oauth -> subscription domain, key -> api)
// with one unfiltered lane per lane name.
func (r *rig) seed(t *testing.T, name string, auth registry.AuthType, authRef string, lanes ...string) {
	t.Helper()
	ctx := context.Background()
	must(t, r.reg.UpsertProvider(ctx, registry.ProviderRecord{Name: name, Driver: registry.DriverOpenAICompat, Auth: auth,
		AuthRef: registry.VaultKeyRef(authRef), AccountKind: registry.AccountPersonal, Tier: registry.TierMid}))
	for _, l := range lanes {
		must(t, r.reg.UpsertLane(ctx, registry.LaneRecord{LaneName: l, ProviderName: name,
			Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable}))
	}
}

func (r *rig) pass(t *testing.T, at time.Time) topology.Report {
	t.Helper()
	rep, err := topology.Reconcile(context.Background(), r.store, r.reg, fixedClock{at})
	if err != nil {
		t.Fatalf("Reconcile: %v (violations %+v)", err, rep.Violations)
	}
	return rep
}

// dump renders every row query returns, byte for byte.
func (r *rig) dump(t *testing.T, query string, args ...any) string {
	t.Helper()
	rows, err := r.db.Query(query, args...)
	must(t, err)
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	must(t, err)
	var b strings.Builder
	for rows.Next() {
		vals, ptrs := make([]any, len(cols)), make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		must(t, rows.Scan(ptrs...))
		fmt.Fprintf(&b, "%#v\n", vals)
	}
	must(t, rows.Err())
	return b.String()
}

func (r *rig) dumpAll(t *testing.T) string {
	t.Helper()
	out := ""
	for _, tbl := range []string{"config_account", "config_quota_domain", "config_runtime_profile", "config_credential", "config_lane"} {
		out += tbl + "\n" + r.dump(t, `SELECT * FROM `+tbl+` ORDER BY id`)
	}
	return out
}

// laneOf returns the topology lane whose CredentialRef is cred.
func (r *rig) laneOf(t *testing.T, cred string) topology.Lane {
	t.Helper()
	lanes, err := r.store.ListLanes(context.Background(), true)
	must(t, err)
	for _, l := range lanes {
		if l.CredentialRef != nil && string(*l.CredentialRef) == cred {
			return l
		}
	}
	t.Fatalf("no lane carries credential %q", cred)
	return topology.Lane{}
}

// hasIdentity walks err's chain for target by pointer identity
// (cascade's errors.Is matches any error of the same Kind).
func hasIdentity(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestReconcilePreservesOwnedColumns(t *testing.T) {
	ctx, r := context.Background(), newRig(t)
	r.seed(t, "prov-a", registry.AuthOAuth, "vault://prov-a/v1", "lane-a")
	r.seed(t, "prov-b", registry.AuthKey, "vault://prov-b/v1", "lane-b")
	r.seed(t, "prov-c", registry.AuthKey, "vault://prov-c/v1", "lane-c")
	r.pass(t, t0)

	cred, err := r.store.GetCredential(ctx, "lane-a")
	must(t, err)
	cred.Health, cred.QuarantineReason, cred.QuarantinedUntil = topology.CredentialQuarantined, "http_401", t0.Add(time.Hour)
	must(t, r.store.UpsertCredential(ctx, cred)) // the health owner's fixture write
	dom, err := r.store.GetQuotaDomain(ctx, "prov-b:api")
	must(t, err)
	dom.BillingTier, dom.Quarantined = topology.BillingTierPaid, true
	must(t, r.store.UpsertQuotaDomain(ctx, dom)) // fixture: PromoteDomain (P1-TOP-19) is not built
	profID := topology.RuntimeProfileID("prov-a:openai-compat")
	prof, err := r.store.GetRuntimeProfile(ctx, profID)
	must(t, err)
	prof.Persistent, prof.Runtime = true, topology.RuntimeClaudeCLI
	prof.ConfigHome, prof.EnvVar = filepath.Join(t.TempDir(), "cfg"), "SYNTH_CONFIG_DIR"
	must(t, r.store.UpsertRuntimeProfile(ctx, prof)) // fixture: HomePath refuses the derived id's ':'
	must(t, r.store.SetEndpoint(ctx, profID, topology.Endpoint{Host: "127.0.0.1", Port: 8080}))

	owned := func() string {
		return r.dump(t, `SELECT id, quota_domain_ref, health, quarantine_reason, quarantined_until FROM config_credential ORDER BY id`) +
			r.dump(t, `SELECT id, billing_tier, quarantined FROM config_quota_domain ORDER BY id`) +
			r.dump(t, `SELECT id, config_home, persistent, endpoint_host, endpoint_port, env_var FROM config_runtime_profile ORDER BY id`)
	}
	before := owned()
	r.seed(t, "prov-a", registry.AuthOAuth, "vault://prov-a/v2")
	r.pass(t, t0.Add(time.Minute))
	if after := owned(); after != before {
		t.Fatalf("owner columns changed by reconcile:\nbefore %s\nafter  %s", before, after)
	}
	if cred, err = r.store.GetCredential(ctx, "lane-a"); err != nil || cred.SecretRef != "vault://prov-a/v2" {
		t.Errorf("secret_ref not refreshed: %+v (%v)", cred, err)
	}
	if prof, err = r.store.GetRuntimeProfile(ctx, profID); err != nil || prof.Runtime != topology.RuntimeAPI {
		t.Errorf("runtime not refreshed: %+v (%v)", prof, err)
	}
	for cred, want := range map[string]topology.LaneHealth{"lane-a": topology.LaneHealthQuarantined,
		"lane-b": topology.LaneHealthQuarantined, "lane-c": topology.LaneHealthAvailable} {
		if got := r.laneOf(t, cred).Health; got != want {
			t.Errorf("lane under %s health = %q, want %q", cred, got, want)
		}
	}
}

func TestReconcileRetiresVanishedLanes(t *testing.T) {
	ctx, r := context.Background(), newRig(t)
	r.seed(t, "prov-a", registry.AuthKey, "vault://prov-a/v1", "lane-a")
	r.seed(t, "prov-b", registry.AuthKey, "vault://prov-b/v1", "lane-b")
	r.pass(t, t0)
	gone := r.laneOf(t, "lane-b")
	var released []string
	resStore, rv := heldReservation(t, gone, &released)
	retiredAt := t0.Add(time.Hour)
	must(t, r.reg.DeleteProvider(ctx, "prov-b"))
	if rep := r.pass(t, retiredAt); rep.Retired != 1 || rep.Unretired != 0 {
		t.Fatalf("Report = %+v, want exactly one retirement", rep)
	}
	got := r.laneOf(t, "lane-b")
	if got.ID != gone.ID || got.RetiredAt == nil || !got.RetiredAt.Equal(retiredAt) {
		t.Fatalf("vanished lane = %+v, want retired_at %v on the same row", got, retiredAt)
	}
	if _, err := r.store.GetCredential(ctx, "lane-b"); err != nil {
		t.Fatalf("credential of the retired lane was removed: %v", err)
	}
	err := r.store.CheckReservable(ctx, gone.ID)
	if !hasIdentity(err, topology.ErrLaneRetired) || !strings.Contains(fmt.Sprint(err), "lane is retired") {
		t.Errorf("CheckReservable(retired) = %v, want ErrLaneRetired", err)
	}
	must(t, r.store.CheckReservable(ctx, r.laneOf(t, "lane-a").ID))
	if _, err := r.store.DomainForCredential(ctx, "lane-b"); !hasIdentity(err, topology.ErrNoLiveQuotaDomain) {
		t.Errorf("DomainForCredential(retired) = %v, want ErrNoLiveQuotaDomain", err)
	}
	if _, err := r.store.DomainForCredential(ctx, "lane-a"); err != nil {
		t.Errorf("DomainForCredential(live) = %v", err)
	}
	res, err := rv.Release(ctx, "res-1")
	if err != nil || res.State != economics.ReservationReleased || len(released) != 1 {
		t.Fatalf("Release on a retired lane = %+v, %v (leases released %v)", res, err, released)
	}
	r.seed(t, "prov-b", registry.AuthKey, "vault://prov-b/v1", "lane-b")
	if rep := r.pass(t, retiredAt.Add(time.Hour)); rep.Unretired != 1 || rep.Retired != 0 {
		t.Fatalf("Report = %+v, want exactly one un-retirement", rep)
	}
	back := r.laneOf(t, "lane-b")
	if back.ID != gone.ID || back.RetiredAt != nil || !back.DiscoveredAt.Equal(gone.DiscoveredAt) {
		t.Fatalf("re-added lane = %+v, want the same LaneID %q un-retired", back, gone.ID)
	}
	must(t, r.store.CheckReservable(ctx, back.ID))
	stored, ok, err := resStore.Get(ctx, "res-1")
	if err != nil || !ok || stored.LaneID != string(back.ID) {
		t.Errorf("reservation row = %+v (%v, %v), want it to reference lane %q", stored, ok, err, back.ID)
	}
}

// heldReservation writes one held reservation (lease-1) on lane into a
// real reservation ledger and returns a Reserver over it.
func heldReservation(t *testing.T, lane topology.Lane, released *[]string) (*economics.ReservationStore, *economics.Reserver) {
	t.Helper()
	ctx := context.Background()
	resStore, err := economics.NewReservationStore(openDB(t, "res.db"), fixedClock{t0})
	must(t, err)
	must(t, economics.ApplyReservationSchema(ctx, resStore.DB(), migrate.SQLiteEmitter{}, fixedClock{t0}, "", ""))
	_, err = resStore.Insert(ctx, economics.Reservation{ID: "res-1", ExecutionID: "exec-1", LaneID: string(lane.ID),
		DomainID: string(lane.QuotaDomainRef), Kind: economics.ReservationInteractive, State: economics.ReservationHeld,
		OwnerEpoch: "epoch-1", LeaseIDs: []string{"lease-1"}})
	must(t, err)
	rv, err := economics.NewReserver(resStore, fixedClock{t0}, "epoch-1", stubSeams(released))
	must(t, err)
	return resStore, rv
}

func stubSeams(released *[]string) economics.ReserverSeams {
	return economics.ReserverSeams{
		Buckets: func(context.Context, string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
			return "", "", nil, cascade.New(cascade.KindUnavailable, "test: no buckets")
		},
		RenewLeases: func(context.Context, []string) error { return nil },
		Permit: func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
			return governor.Permit{}, cascade.New(cascade.KindUnavailable, "test: no permit")
		},
		AcquireLeases: func(context.Context, string, string, []string) ([]string, error) {
			return nil, cascade.New(cascade.KindUnavailable, "test: no leases")
		},
		ReleaseLeases:    func(_ context.Context, ids []string) error { *released = append(*released, ids...); return nil },
		AllocateWorktree: func(context.Context, string) (string, error) { return "", nil },
		RemoveWorktree:   func(context.Context, string) error { return nil },
		ValidateLeases:   func(_ context.Context, _ string, ids []string) ([]string, error) { return ids, nil },
		ReserveFraction:  func(context.Context, string) (float64, error) { return 0, nil },
		Projects:         &economics.ActiveProjectCount{},
		FindLeases:       func(context.Context, string, string, []string) ([]string, error) { return nil, nil },
		FindWorktree:     func(context.Context, string) (string, bool, error) { return "", false, nil },
		RaiseAttention:   func(context.Context, string, string) error { return nil },
	}
}
