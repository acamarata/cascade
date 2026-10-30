package capacity_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type scheduleRequest struct {
	TaskClass     conductor.TaskClass  `yaml:"task_class"`
	MinQuality    capacity.QualityEnum `yaml:"min_quality"`
	PreferredTier capacity.Tier        `yaml:"preferred_tier"`
	ReserveTier0  bool                 `yaml:"reserve_tier0"`
	RiskClass     jobs.RiskClass       `yaml:"risk_class"`
}
type scheduleCase struct {
	Name            string          `yaml:"name"`
	Request         scheduleRequest `yaml:"request"`
	Attempt         int             `yaml:"attempt"`
	ExpectedTier    capacity.Tier   `yaml:"expected_tier"`
	ExpectedJump    bool            `yaml:"expected_jump_rule_fired"`
	ExpectedReserve bool            `yaml:"expected_reserve_tier0_flag"`
}

// requireFields checks presence separately from zero values, including false.
func requireFields(n *yaml.Node, typ reflect.Type) error {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		var value *yaml.Node
		for j := 0; j < len(n.Content); j += 2 {
			if n.Content[j].Value == f.Tag.Get("yaml") {
				value = n.Content[j+1]
			}
		}
		if value == nil || value.Tag == "!!null" {
			return fmt.Errorf("missing %s", f.Tag.Get("yaml"))
		}
		if f.Type.Kind() == reflect.Struct {
			if err := requireFields(value, f.Type); err != nil {
				return err
			}
		}
	}
	return nil
}
func decodeSchedule(data []byte) ([]scheduleCase, error) {
	var cases []scheduleCase
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&cases); err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	for i, c := range cases {
		if err := requireFields(root.Content[0].Content[i], reflect.TypeOf(c)); err != nil {
			return nil, err
		}
		if c.Name == "" || c.Attempt < 1 || !c.ExpectedTier.Valid() {
			return nil, fmt.Errorf("invalid case %q", c.Name)
		}
	}
	return cases, nil
}

type acceptanceDecisions struct {
	writer learn.SQLiteSchedulerDecisionWriter
}

func (a acceptanceDecisions) Record(ctx context.Context, q capacity.DecisionExecer, d capacity.DecisionRecord) error {
	return a.writer.Record(ctx, q, learn.SchedulerDecision{
		ID: d.ID, JobID: d.JobID, ExecutionID: d.ExecutionID, TaskClass: string(d.TaskClass),
		SelectedTier: string(d.SelectedTier), SelectedLaneID: string(d.LaneID), SelectedNodeID: d.NodeID,
		ScoreAtSelection: d.ScoreAtSelection, FallbackLevel: learn.FallbackLevel(d.FallbackLevel),
		JumpRuleFired: d.JumpRuleFired, JumpReasonCode: d.JumpReasonCode, ReserveTier0: d.ReserveTier0, DecidedAt: d.DecidedAt,
	})
}
func acceptanceDB(t *testing.T, clock *runtime.FixedClock) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "schedule.db"))
	mustAcceptance(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { mustAcceptance(t, db.Close()) })
	ctx, dialect := t.Context(), migrate.SQLiteEmitter{}
	mustAcceptance(t, jobs.ApplyJobsSchema(ctx, db, dialect, clock, "", ""))
	mustAcceptance(t, economics.ApplyReservationSchema(ctx, db, dialect, clock, "", ""))
	mustAcceptance(t, learn.ApplyLearnSchema(ctx, db, dialect, clock))
	mustAcceptance(t, topology.ApplyMigrationSchema(ctx, db, dialect, clock, "", ""))
	mustAcceptance(t, registry.ApplyMigrationSchema(ctx, db, dialect, clock, "", ""))
	return db
}
func acceptanceLanes(t *testing.T, db *sql.DB, clock *runtime.FixedClock) capacity.FleetSnapshot {
	t.Helper()
	reg := registry.NewRegistry(db, clock)
	snap := capacity.FleetSnapshot{GeneratedAt: clock.Now(), Lanes: map[conductor.LaneID]capacity.ProviderSlot{}}
	for _, tier := range []capacity.Tier{capacity.TierZero, capacity.TierOne, capacity.TierTwo} {
		name := string(tier)
		mustAcceptance(t, reg.UpsertProvider(t.Context(), registry.ProviderRecord{Name: name, Driver: registry.DriverAnthropic,
			Auth: registry.AuthKey, AuthRef: "vault://fixture/reference", AccountKind: registry.AccountPersonal, Tier: registry.TierMid}))
		mustAcceptance(t, reg.UpsertLane(t.Context(), registry.LaneRecord{LaneName: name, ProviderName: name,
			Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable}))
		snap.Lanes[conductor.LaneID(name)] = capacity.ProviderSlot{LaneID: conductor.LaneID(name), Tier: tier,
			LaneType: conductor.LaneExternalAPI, ProfileRef: name, State: capacity.StateAvailable, UpdatedAt: clock.Now(),
			Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}
	}
	ts, qs := topology.NewStore(db), topology.NewQuotaStore(db)
	_, err := topology.Reconcile(t.Context(), ts, reg, clock)
	mustAcceptance(t, err)
	domains, err := ts.ListQuotaDomains(t.Context())
	mustAcceptance(t, err)
	for _, domain := range domains {
		for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
			mustAcceptance(t, qs.UpsertBucket(t.Context(), domain.ID, dim, topology.Bucket{Name: dim, Limit: 1000000,
				RemainingFraction: 1, CapacityObserved: 1000000, LimitScopeID: topology.LimitScopeID(domain.ID),
				Window: topology.BucketWindowRolling, Source: topology.SourceProviderStatus, Confidence: 1, ObservedAt: clock.Now()}))
		}
	}
	return snap
}

// acceptanceSeams reads quota observations from the same database as the ledger.
// These requests carry no mutable scopes; filesystem operations must never run.
func acceptanceSeams(db *sql.DB) economics.ReserverSeams {
	unexpected := errors.New("unexpected mutable-scope operation")
	return economics.ReserverSeams{
		Buckets: func(ctx context.Context, id string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
			d, err := topology.NewStore(db).GetQuotaDomain(ctx, topology.DomainID(id))
			if err != nil {
				return "", "", nil, err
			}
			bs, err := topology.NewQuotaStore(db).ListBuckets(ctx, d.ID)
			out := map[string]topology.Bucket{}
			for _, b := range bs {
				out[b.Name] = b
			}
			return d.Kind, topology.LimitScopeID(id), out, err
		},
		Projects:        &economics.ActiveProjectCount{},
		ReserveFraction: func(context.Context, string) (float64, error) { return 0, nil },
		Permit: func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
			return governor.Permit{}, unexpected
		},
		AcquireLeases:    func(context.Context, string, string, []string) ([]string, error) { return nil, unexpected },
		ReleaseLeases:    func(context.Context, []string) error { return unexpected },
		AllocateWorktree: func(context.Context, string) (string, error) { return "", unexpected }, RemoveWorktree: func(context.Context, string) error { return unexpected },
		ValidateLeases: func(context.Context, string, []string) ([]string, error) { return nil, unexpected }, RenewLeases: func(context.Context, []string) error { return unexpected },
		FindLeases: func(context.Context, string, string, []string) ([]string, error) { return nil, unexpected }, FindWorktree: func(context.Context, string) (string, bool, error) { return "", false, unexpected },
		RaiseAttention: func(context.Context, string, string) error { return unexpected },
	}
}
func mustAcceptance(t *testing.T, err error) {
	if t.Helper(); err != nil {
		t.Fatal(err)
	}
}

// TestFleetCapacityAcceptance proves scheduling and persisted decisions together.
func TestFleetCapacityAcceptance(t *testing.T) {
	data, err := os.ReadFile("testdata/schedule_cases.yaml")
	mustAcceptance(t, err)
	cases, err := decodeSchedule(data)
	mustAcceptance(t, err)
	for _, bad := range []string{strings.Replace(string(data), "  expected_reserve_tier0_flag: false\n", "", 1), strings.Replace(string(data), "  attempt: 2\n", "  attempt: 2\n  extra: 1\n", 1)} {
		if _, err := decodeSchedule([]byte(bad)); err == nil || bad == string(data) {
			t.Fatalf("a schedule with a missing field or an unknown key decoded: err=%v unchanged=%v", err, bad == string(data))
		}
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) { runScheduleCase(t, c, "") })
	}
	for _, refusal := range []string{"sensitivity", "tier-0-reserved"} {
		t.Run(refusal, func(t *testing.T) { runScheduleCase(t, cases[2], refusal) })
	}
}
func runScheduleCase(t *testing.T, c scheduleCase, refusal string) {
	t.Helper()
	ctx, clock := t.Context(), runtime.NewFixedClock(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	db := acceptanceDB(t, clock)
	snap := acceptanceLanes(t, db, clock)
	if refusal == "tier-0-reserved" {
		delete(snap.Lanes, "tier-1")
		delete(snap.Lanes, "tier-2")
	}
	store, class := jobs.NewStore(db), provider.SensitivityInternal
	mustAcceptance(t, store.PutJob(ctx, jobs.Job{ID: "job-1", State: jobs.JobStatePending, CreatedAt: clock.Now().Unix(), UpdatedAt: clock.Now().Unix(),
		Capabilities: []string{"code"}, MutableScope: "repo:x", RiskClass: "normal", MinTaskClass: "code", NodeRequirements: "{}",
		TimeoutSeconds: 60, CostCeiling: 1, Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: &class}))
	ledger, err := economics.NewReservationStore(db, clock)
	mustAcceptance(t, err)
	reserver, err := economics.NewReserver(ledger, clock, "acceptance", acceptanceSeams(db))
	mustAcceptance(t, err)
	fleet, err := capacity.NewFleet(capacity.FleetDeps{
		Snapshot: func(context.Context) (capacity.FleetSnapshot, error) { return snap, nil },
		Selector: capacity.NewTierPolicySelector(capacity.NewTierPolicy(learn.NewSQLiteEstimateSource(db), capacity.NewDefaultProbeAdmission(), clock)),
		Scorer:   learn.NewSQLiteCapabilityScorer(db, clock).For, Reserver: reserver, Reservations: ledger, Jobs: store, Decisions: acceptanceDecisions{},
		DomainOf: func(ctx context.Context, id conductor.LaneID) (string, error) {
			d, err := topology.NewStore(db).DomainForCredential(ctx, topology.CredentialID(id))
			return string(d), err
		},
		Place:            func(context.Context, nodes.Requirement) (string, error) { return "controller", nil },
		ControllerNodeID: "controller", Clock: clock, TTL: time.Minute,
	})
	mustAcceptance(t, err)
	req := capacity.ResourceRequest{Intent: "build", TaskClass: c.Request.TaskClass, MinQuality: c.Request.MinQuality,
		PreferredTier: c.Request.PreferredTier, ReserveTier0: c.Request.ReserveTier0, RiskClass: c.Request.RiskClass,
		Deadline: capacity.DeadlineInteractive, Sensitivity: class}
	if refusal == "sensitivity" {
		req.Sensitivity = provider.SensitivityLocalOnly
	}
	l, err := fleet.Lease(ctx, req, capacity.DispatchRef{JobID: "job-1", RepoID: "repo-1", ProjectID: "project-1", Requests: 1, HydratedTokens: 1000}, c.Attempt)
	if refusal != "" {
		checkRefusal(t, db, err, refusal)
	} else {
		mustAcceptance(t, err)
		defer func() { mustAcceptance(t, fleet.Release(context.Background(), l)) }()
		checkScheduleDecision(t, db, l, c)
		mustAcceptance(t, fleet.Release(ctx, l))
		if _, live, err := fleet.RepoOfLease(ctx, l.ID); live || err == nil {
			t.Fatalf("released lease remains live: %v", err)
		}
	}
	assertAcceptanceCount(t, db, "jobs_reservation WHERE state = 'held'", 0)
	assertAcceptanceCount(t, db, "jobs_resource_lease WHERE state = 'held'", 0)
	checkDecisionSchema(t, db)
}
func assertAcceptanceCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
		t.Fatalf("%s has %d rows, want %d: %v", table, n, want, err)
	}
}
func checkScheduleDecision(t *testing.T, db *sql.DB, l capacity.Lease, c scheduleCase) {
	t.Helper()
	var tier, reason, execution, lane string
	var jump, reserve bool
	var score float64
	mustAcceptance(t, db.QueryRow(`SELECT selected_tier, jump_rule_fired, jump_reason_code, reserve_tier0_flag, execution_id, selected_lane_id, score_at_selection FROM jobs_scheduler_decisions WHERE id = ?`, l.DecisionID).Scan(&tier, &jump, &reason, &reserve, &execution, &lane, &score))
	wantReason := string(capacity.JumpReasonNone)
	if c.ExpectedJump {
		wantReason = "attempt"
	}
	if l.SelectedTier != c.ExpectedTier || tier != string(c.ExpectedTier) || jump != c.ExpectedJump || reserve != c.ExpectedReserve || reason != wantReason || execution != l.ExecutionID || lane != string(l.LaneID) || score <= 0 {
		t.Fatalf("lease %+v; decision tier=%s jump=%v reason=%s reserve=%v execution=%s lane=%s score=%v; want %+v", l, tier, jump, reason, reserve, execution, lane, score, c)
	}
	assertAcceptanceCount(t, db, "jobs_scheduler_decisions", 1)
}
func checkDecisionSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	forbidden, names := regexp.MustCompile(`(?i)(prompt|text|content|message|input|query|response)`), ""
	// A table with no columns aggregates to NULL, which fails the scan: the check cannot pass vacuously.
	mustAcceptance(t, db.QueryRow("SELECT group_concat(name, ' ') FROM pragma_table_info('jobs_scheduler_decisions')").Scan(&names))
	for _, name := range strings.Fields(names) {
		if forbidden.MatchString(name) {
			t.Errorf("forbidden decision column %s", name)
		}
	}
}

// checkRefusal asserts the refusal's sentinel by identity and message and that it left no rows.
func checkRefusal(t *testing.T, db *sql.DB, err error, refusal string) {
	t.Helper()
	want := capacity.ErrTier0Reserved
	if refusal == "sensitivity" {
		want = capacity.ErrSensitivityConstraint
	}
	chain, ok := err.(interface{ Unwrap() []error })
	if !ok || len(chain.Unwrap()) == 0 || chain.Unwrap()[0] != want || !strings.Contains(err.Error(), want.Msg) {
		t.Fatalf("refusal = %v, want identity and message of %v", err, want)
	}
	for _, table := range []string{"jobs_reservation", "jobs_execution", "jobs_scheduler_decisions"} {
		assertAcceptanceCount(t, db, table, 0)
	}
}
