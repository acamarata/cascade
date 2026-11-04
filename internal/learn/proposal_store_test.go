package learn

// Purpose: ProposalStore: Create writes the config and its trigger in one
//   transaction (a failed trigger insert keeps no row), Get/List/
//   LatestBySignature read them back, and malformed triggers are refused
//   before any SQL runs.
// SPORT: learn/proposal_store_test (P1-LRN-02).

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestProposalStore returns a proposal store over a fresh v2 db.
func newTestProposalStore(t *testing.T) (*ProposalStore, *ConfigStore, *sql.DB) {
	t.Helper()
	c, db, _ := newTestStore(t)
	s, err := NewProposalStore(c, db)
	if err != nil {
		t.Fatalf("NewProposalStore: %v", err)
	}
	return s, c, db
}

// validTrigger is a well-formed trigger for signature sig ending at end.
func validTrigger(sig string, end time.Time) Trigger {
	return Trigger{
		Signature: sig, Occurrences: 4, DistinctSources: 3,
		Beneficiary: LaneIdentity{LaneTier: "tier-2", NodeID: "node-7"},
		WindowStart: end.Add(-testWindow), WindowEnd: end, Kind: ProposedCI,
	}
}

// testWindow mirrors the detection window without depending on the engine.
const testWindow = 14 * 24 * time.Hour

var (
	sigA = strings.Repeat("a", 64)
	sigB = strings.Repeat("b", 64)
	tEnd = time.Date(2026, 10, 1, 9, 30, 0, 123, time.UTC)
)

func TestProposalStoreRoundTrip(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	if _, err := NewProposalStore(nil, nil); err == nil {
		t.Fatal("NewProposalStore(nil, nil) accepted")
	}
	s, cs, db := newTestProposalStore(t)
	p, err := s.Create(ctx, validSubmission(), validTrigger(sigA, tEnd))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Config.Status != StatusPending || p.Config.Tier != TierSafe || p.Config.ID == "" || len(p.Config.Evidence) != 1 {
		t.Fatalf("Create returned config %+v, want a pending safe config with its evidence", p.Config)
	}
	if !reflect.DeepEqual(p.Trigger, validTrigger(sigA, tEnd)) {
		t.Fatalf("Create returned trigger %+v", p.Trigger)
	}
	got, err := s.Get(ctx, p.Config.ID)
	if err != nil || !reflect.DeepEqual(got.Trigger, p.Trigger) || got.Config.ID != p.Config.ID ||
		got.Config.Change != p.Config.Change || got.Config.Status != StatusPending {
		t.Fatalf("Get = (%+v, %v), want %+v", got, err, p)
	}
	var sig, kind, tier, node string
	var occ, distinct int
	var start, end int64
	if err := db.QueryRow(`SELECT trigger_signature, occurrence_count, distinct_source_count, beneficiary_lane_tier,
		beneficiary_node_id, window_start, window_end, change_kind FROM jobs_learn_proposal_trigger WHERE config_id = ?`,
		p.Config.ID).Scan(&sig, &occ, &distinct, &tier, &node, &start, &end, &kind); err != nil {
		t.Fatalf("read trigger row: %v", err)
	}
	if sig != sigA || occ != 4 || distinct != 3 || tier != "tier-2" || node != "node-7" || kind != "ci" ||
		end != tEnd.UnixNano() || start != tEnd.Add(-testWindow).UnixNano() {
		t.Fatalf("stored trigger (%s %d %d %s %s %d %d %s)", sig, occ, distinct, tier, node, start, end, kind)
	}
	plain, err := cs.Submit(ctx, validSubmission())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_, err = s.Get(ctx, plain.ID)
	wantErr(t, err, cascade.KindNotFound, "learn: unknown proposal")
	list, err := s.List(ctx, ConfigFilter{Status: StatusPending})
	if err != nil || len(list) != 1 || list[0].Config.ID != p.Config.ID || !reflect.DeepEqual(list[0].Trigger, p.Trigger) {
		t.Fatalf("List = (%+v, %v), want only the proposal", list, err)
	}
	if list, err := s.List(ctx, ConfigFilter{Status: StatusApplied}); err != nil || len(list) != 0 {
		t.Fatalf("List(applied) = (%+v, %v), want none", list, err)
	}
	if _, err := s.List(ctx, ConfigFilter{Status: "bogus"}); err == nil {
		t.Fatal("List accepted an unknown status")
	}
}

func TestProposalStoreAtomicCreate(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	s, _, db := newTestProposalStore(t)
	bad := map[string]func(*Trigger){
		"learn: trigger signature must be a sha256 hex digest":    func(tr *Trigger) { tr.Signature = "free text signature" },
		"learn: trigger counts must be positive":                  func(tr *Trigger) { tr.Occurrences = 0 },
		"learn: trigger window must be set and ordered":           func(tr *Trigger) { tr.WindowStart = tr.WindowEnd.Add(time.Hour) },
		"learn: change kind must be rule, policy, workflow or ci": func(tr *Trigger) { tr.Kind = "remove" },
	}
	for msg, mutate := range bad {
		tr := validTrigger(sigA, tEnd)
		mutate(&tr)
		_, err := s.Create(ctx, validSubmission(), tr)
		wantErr(t, err, cascade.KindInvalidInput, msg)
	}
	tr := validTrigger(sigA, tEnd)
	tr.Beneficiary.NodeID = "someone@example.com"
	if _, err := s.Create(ctx, validSubmission(), tr); err == nil {
		t.Fatal("Create accepted an identifying beneficiary node id")
	}
	sub := validSubmission()
	sub.Evidence = nil
	if _, err := s.Create(ctx, sub, validTrigger(sigA, tEnd)); err == nil {
		t.Fatal("Create accepted a submission without evidence")
	}
	if c, sb := configRows(t, db); c != 0 || sb != 0 || countQuery(t, db, "SELECT COUNT(*) FROM jobs_learn_proposal_trigger") != 0 {
		t.Fatalf("refused creates kept rows: %d configs, %d submissions", c, sb)
	}
	if _, err := db.Exec("DROP TABLE jobs_learn_proposal_trigger"); err != nil {
		t.Fatalf("drop trigger table: %v", err)
	}
	_, err := s.Create(ctx, validSubmission(), validTrigger(sigA, tEnd))
	wantErr(t, err, cascade.KindUnavailable, "learn: insert proposal trigger")
	if c, sb := configRows(t, db); c != 0 || sb != 0 {
		t.Fatalf("failed trigger insert kept %d configs and %d submissions, want 0 (one transaction)", c, sb)
	}
}

func TestProposalStoreLatestBySignature(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	s, _, _ := newTestProposalStore(t)
	if _, ok, err := s.LatestBySignature(ctx, sigA); ok || err != nil {
		t.Fatalf("LatestBySignature on an empty store = (%v, %v), want (false, nil)", ok, err)
	}
	_, _, err := s.LatestBySignature(ctx, "not-a-digest")
	wantErr(t, err, cascade.KindInvalidInput, "learn: trigger signature must be a sha256 hex digest")
	older, err := s.Create(ctx, validSubmission(), validTrigger(sigA, tEnd))
	if err != nil {
		t.Fatalf("Create older: %v", err)
	}
	newer, err := s.Create(ctx, validSubmission(), validTrigger(sigA, tEnd.Add(48*time.Hour)))
	if err != nil {
		t.Fatalf("Create newer: %v", err)
	}
	other, err := s.Create(ctx, validSubmission(), validTrigger(sigB, tEnd.Add(96*time.Hour)))
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if older.Config.ID == newer.Config.ID {
		t.Fatal("two proposals share an id")
	}
	got, ok, err := s.LatestBySignature(ctx, sigA)
	if err != nil || !ok || got.Config.ID != newer.Config.ID || !got.Trigger.WindowEnd.Equal(tEnd.Add(48*time.Hour)) {
		t.Fatalf("LatestBySignature(A) = (%+v, %v, %v), want the newer proposal %s", got, ok, err, newer.Config.ID)
	}
	got, ok, err = s.LatestBySignature(ctx, sigB)
	if err != nil || !ok || got.Config.ID != other.Config.ID {
		t.Fatalf("LatestBySignature(B) = (%+v, %v, %v), want %s", got, ok, err, other.Config.ID)
	}
	if _, ok, err := s.LatestBySignature(ctx, strings.Repeat("c", 64)); ok || err != nil {
		t.Fatalf("LatestBySignature(unknown) = (%v, %v), want (false, nil)", ok, err)
	}
}
