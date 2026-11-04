//go:build !windows

// Purpose: reviewer inputs resolve a job's family from its latest
//
//	execution's reservation lane through the registry driver, over a real
//	SQLite jobs store, reservation ledger and provider registry.
//
// SPORT: cmd/cascade/reviewer (ADD, P1-PBD-05).
package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewerRig is the seeded runtime the table cases read.
type reviewerRig struct {
	store *jobs.Store
	db    *sql.DB
	rsv   *economics.ReservationStore
	reg   *registry.Registry
}

func newReviewerRig(t *testing.T) reviewerRig {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{jobs.ApplyJobsSchema, economics.ApplyReservationSchema} {
		if err := apply(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}
	rsv, err := economics.NewReservationStore(db, clock)
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	reg, closeReg := openLaneRegistry(t, dir, clock)
	t.Cleanup(closeReg)
	rig := reviewerRig{store: jobs.NewStore(db), db: db, rsv: rsv, reg: reg}
	// Lane names differ from provider names and drivers: the family must
	// come from the driver, so two accounts of one provider are one family.
	for _, p := range []struct {
		name   string
		driver registry.DriverKind
	}{{"anthropic-acc1", registry.DriverAnthropic}, {"anthropic-acc2", registry.DriverAnthropic}, {"gem-main", registry.DriverGemini}} {
		if err := reg.AddProvider(ctx, registry.ProviderRecord{Name: p.name, Driver: p.driver, Auth: registry.AuthKey,
			AuthRef: registry.VaultKeyRef(p.name + "-ref"), KnownModels: []string{"m"}, AccountKind: registry.AccountPersonal,
			Tier: registry.TierFree, HealthStatus: registry.HealthHealthy}); err != nil {
			t.Fatalf("AddProvider(%s): %v", p.name, err)
		}
	}
	for lane, prov := range map[string]string{"lane-a1": "anthropic-acc1", "lane-a2": "anthropic-acc2", "lane-g": "gem-main"} {
		if err := reg.UpsertLane(ctx, registry.LaneRecord{LaneName: lane, ProviderName: prov, Weight: 1,
			Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable}); err != nil {
			t.Fatalf("UpsertLane(%s): %v", lane, err)
		}
	}
	return rig
}

// job seeds a job, its executions (attempt order) and the reservation lane
// of each execution ("" = no reservation).
func (r reviewerRig) job(t *testing.T, id, class string, lanes ...string) {
	t.Helper()
	ctx := context.Background()
	if err := r.store.PutJob(ctx, jobs.Job{ID: id, State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "src/a.txt", RiskClass: "low", MinTaskClass: class,
		NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1, Priority: 1, ConsequenceClass: jobs.ConsequenceNormal,
		DataClass: new(provider.SensitivityInternal)}); err != nil {
		t.Fatalf("PutJob(%s): %v", id, err)
	}
	for i, lane := range lanes {
		exec := id + "-exec-" + string(rune('a'+i))
		if err := r.store.PutExecution(ctx, jobs.Execution{ID: exec, JobID: id, Attempt: i + 1, State: jobs.ExecutionRunning}); err != nil {
			t.Fatalf("PutExecution(%s): %v", exec, err)
		}
		if lane == "" {
			continue
		}
		if _, err := r.rsv.Insert(ctx, economics.Reservation{ID: "rsv-" + exec, ExecutionID: exec, JobID: id, LaneID: lane,
			Kind: economics.ReservationInteractive, State: economics.ReservationHeld}); err != nil {
			t.Fatalf("Insert(%s): %v", exec, err)
		}
	}
}

func (r reviewerRig) inputs(lanes func(context.Context) ([]registry.LaneRecord, error)) reviewerInputs {
	if lanes == nil {
		lanes = r.reg.ListLanes
	}
	return reviewerInputs{jobs: r.store, jobsDB: r.db, fleet: ledgerLookup{r.rsv}, lanes: lanes, reg: registry.NewReader(r.reg)}
}

// ledgerLookup reads the real ledger under the Fleet method name.
type ledgerLookup struct{ s *economics.ReservationStore }

func (l ledgerLookup) ReservationByExecution(ctx context.Context, id string) (economics.Reservation, bool, error) {
	return l.s.GetByExecution(ctx, id)
}

func TestReviewerInputsFamilyFromReservation(t *testing.T) {
	rig := newReviewerRig(t)
	rig.job(t, "j-name-vs-driver", "code", "lane-a1")
	rig.job(t, "j-latest", "code", "lane-g", "lane-a2")
	rig.job(t, "j-noexec", "code")
	rig.job(t, "j-noresv", "code", "")
	rig.job(t, "j-unknown-lane", "code", "lane-missing")
	lanesErr := cascade.New(cascade.KindUnavailable, "lanes down")
	cases := []struct {
		name  string
		job   string
		lanes func(context.Context) ([]registry.LaneRecord, error)
		want  reviewerFamily
		err   error
	}{
		{"lane name differs from driver", "j-name-vs-driver", nil, reviewerFamily{Family: "anthropic", Resolved: true}, nil},
		{"latest execution wins", "j-latest", nil, reviewerFamily{Family: "anthropic", Resolved: true}, nil},
		{"no execution", "j-noexec", nil, reviewerFamily{}, nil},
		{"no reservation", "j-noresv", nil, reviewerFamily{}, nil},
		{"unknown lane", "j-unknown-lane", nil, reviewerFamily{}, nil},
		{"lane read error", "j-name-vs-driver", func(context.Context) ([]registry.LaneRecord, error) { return nil, lanesErr }, reviewerFamily{}, lanesErr},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := rig.inputs(c.lanes)
			got, err := in.ReviewerFamily(context.Background(), jobs.Job{ID: c.job})
			if c.err != nil {
				if err != c.err { //nolint:errorlint // identity: the read error itself, not an unresolved family
					t.Fatalf("err = %v, want %v", err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("ReviewerFamily = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
	t.Run("author families", func(t *testing.T) { checkAuthorFamilies(t, rig) })
}

// checkAuthorFamilies: a review job resolves the families of the jobs it
// depends on, and a job with no dependencies is KindNotFound.
func checkAuthorFamilies(t *testing.T, rig reviewerRig) {
	t.Helper()
	rig.job(t, "j-review", "review", "lane-g")
	for _, dep := range []string{"j-name-vs-driver", "j-latest"} {
		if err := rig.store.PutDependency(context.Background(), jobs.TaskDependency{JobID: "j-review", DependsOnJobID: dep}); err != nil {
			t.Fatalf("PutDependency: %v", err)
		}
	}
	in := rig.inputs(nil)
	got, err := in.AuthorFamilies(context.Background(), jobs.Job{ID: "j-review"})
	if err != nil || len(got) != 2 || got[0].Family != "anthropic" || got[1].Family != "anthropic" {
		t.Fatalf("AuthorFamilies = %+v, %v", got, err)
	}
	rev, err := in.ReviewerFamily(context.Background(), jobs.Job{ID: "j-review"})
	if err != nil || rev != (reviewerFamily{Family: "gemini", Resolved: true}) {
		t.Fatalf("ReviewerFamily(review) = %+v, %v", rev, err)
	}
	_, err = in.AuthorFamilies(context.Background(), jobs.Job{ID: "j-noexec"})
	if !errors.Is(err, cascade.New(cascade.KindNotFound, "")) {
		t.Fatalf("AuthorFamilies(no deps) err = %v, want KindNotFound", err)
	}
}
