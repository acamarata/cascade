package learn

// Purpose: the status compare-and-set, the version history with its one
//   revert, and the store's tighten-only guards (a security row is never
//   applied or versioned; a value past a bound is never stored), each
//   asserting the stored state, not only the error.
// SPORT: learn/config_store_status_test (P1-LRN-01).

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// storedStatus reads a config's status straight from the table.
func storedStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow("SELECT status FROM jobs_learned_config_submission WHERE config_id = ?", id).Scan(&s); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

// securitySubmission is a denied-area proposal reached through an alias.
func securitySubmission() Submission {
	sub := validSubmission()
	sub.Target, sub.Change = "auth_rules", Change{Op: OpSet, Path: "policy.auth.mode", Value: `"open"`}
	return sub
}

func TestConfigStoreStatusCompareAndSet(t *testing.T) {
	isolateHome(t)
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	c, err := s.Submit(ctx, validSubmission())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := s.UpdateStatus(ctx, c.ID, StatusPending, StatusApplied); err != nil || storedStatus(t, db, c.ID) != "applied" {
		t.Fatalf("first pending->applied = %v (stored %s)", err, storedStatus(t, db, c.ID))
	}
	err = s.UpdateStatus(ctx, c.ID, StatusPending, StatusApplied)
	wantErr(t, err, cascade.KindConflict, `learn: learned config status is "applied", not "pending"`)
	wantErr(t, s.UpdateStatus(ctx, "no-such-id", StatusPending, StatusRejected), cascade.KindNotFound, "learn: unknown learned config")
	const badStatus = "learn: status must be pending, applied, rejected or reverted"
	wantErr(t, s.UpdateStatus(ctx, c.ID, "done", StatusApplied), cascade.KindInvalidInput, badStatus)
	wantErr(t, s.UpdateStatus(ctx, c.ID, StatusApplied, ""), cascade.KindInvalidInput, badStatus)
	wantErr(t, s.UpdateStatus(ctx, c.ID, StatusApplied, StatusApplied), cascade.KindConflict,
		`learn: status move from "applied" to "applied" is not allowed`)
	if err := s.UpdateStatus(ctx, c.ID, StatusApplied, StatusReverted); err != nil || storedStatus(t, db, c.ID) != "reverted" {
		t.Fatalf("applied->reverted = %v", err)
	}
	sec, err := s.Submit(ctx, securitySubmission())
	if err != nil || sec.Tier != TierSecurity || sec.Target != "auth.rules" {
		t.Fatalf("Submit(security) = (%+v, %v)", sec, err)
	}
	wantErr(t, s.UpdateStatus(ctx, sec.ID, StatusPending, StatusApplied), cascade.KindPermissionDenied,
		"learn: a security-tier learned config is never applied (C11)")
	if got := storedStatus(t, db, sec.ID); got != "pending" {
		t.Fatalf("refused apply changed the stored status to %s", got)
	}
	if err := s.UpdateStatus(ctx, sec.ID, StatusPending, StatusRejected); err != nil {
		t.Fatalf("a security row must still be rejectable: %v", err)
	}
	assertTamperedTierRefused(t, s, db)
}

// assertTamperedTierRefused rewrites a stored security row's tier to safe
// directly in SQL: every read and every write path refuses the row.
func assertTamperedTierRefused(t *testing.T, s *ConfigStore, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	sec, err := s.Submit(ctx, securitySubmission())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := db.Exec("UPDATE jobs_learned_config SET tier = 'safe', areas = '' WHERE id = ?", sec.ID); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	const mismatch = "learn: stated tier, areas or bound flag disagree with the computed classification"
	_, err = s.Get(ctx, sec.ID)
	wantErr(t, err, cascade.KindInvalidInput, mismatch)
	wantErr(t, s.UpdateStatus(ctx, sec.ID, StatusPending, StatusApplied), cascade.KindInvalidInput, mismatch)
	_, err = s.AppendVersion(ctx, sec.ID, `"open"`, nil, "applier-1")
	wantErr(t, err, cascade.KindInvalidInput, mismatch)
	_, err = s.List(ctx, ConfigFilter{Tier: TierSafe})
	wantErr(t, err, cascade.KindInvalidInput, mismatch)
	if got := storedStatus(t, db, sec.ID); got != "pending" {
		t.Fatalf("tampered row's status moved to %s", got)
	}
	if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_version WHERE config_id = '"+sec.ID+"'"); n != 0 {
		t.Fatalf("tampered row got %d versions", n)
	}
}

func TestConfigVersionHistoryRevert(t *testing.T) {
	isolateHome(t)
	s, db, clock := newTestStore(t)
	ctx := context.Background()
	c, err := s.Submit(ctx, validSubmission())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	base := 0.75
	applyConfigForTest(t, s, c.ID)
	v1, err := s.AppendVersion(ctx, c.ID, "240", &base, "applier-1")
	if err != nil || v1.Version != 1 {
		t.Fatalf("AppendVersion v1 = (%+v, %v)", v1, err)
	}
	clock.Advance(time.Minute)
	v2, err := s.AppendVersion(ctx, c.ID, "120", nil, "applier-1")
	if err != nil || v2.Version != 2 {
		t.Fatalf("AppendVersion v2 = (%+v, %v)", v2, err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil || got.Version != 2 || got.Rollback == nil || got.Rollback.PreviousValue != "240" || !got.Rollback.SnapshotAt.Equal(v2.AppliedAt) {
		t.Fatalf("Get after v2 = (%+v, %v), want Rollback from v1", got, err)
	}
	if act, ok, err := s.ActiveVersion(ctx, c.ID); err != nil || !ok || act.Version != 2 {
		t.Fatalf("ActiveVersion = (%+v, %v, %v), want v2", act, ok, err)
	}
	clock.Advance(time.Minute)
	v3, err := s.RevertToVersion(ctx, c.ID, 1, "regression")
	if err != nil || v3.Version != 3 || v3.Value != "240" || v3.Baseline == nil || *v3.Baseline != base {
		t.Fatalf("RevertToVersion(1) = (%+v, %v), want v3 carrying v1", v3, err)
	}
	hist, err := s.VersionHistory(ctx, c.ID)
	if err != nil || len(hist) != 3 || hist[0].Version != 1 || hist[2].Version != 3 {
		t.Fatalf("VersionHistory = (%+v, %v)", hist, err)
	}
	if hist[1].RevertedAt == nil || !hist[1].RevertedAt.Equal(clock.Now()) || hist[1].RevertReason != "regression" ||
		hist[0].RevertedAt != nil || hist[2].RevertedAt != nil {
		t.Fatalf("revert stamps wrong: %+v", hist)
	}
	if got, _ := s.Get(ctx, c.ID); got.Version != 3 || got.Rollback.PreviousValue != "120" {
		t.Fatalf("Get after revert = %+v", got)
	}
	assertVersionRefusals(t, s, db, c.ID)
	assertOutcomeCounters(t, s, c.ID, clock.Now())
	assertClosedDBUnavailable(t, s, db, c.ID)
}

// assertVersionRefusals: unknown config/version, a revert onto the active
// version, a security row and a value past the bound each refuse, and the
// history is unchanged after every refusal.
func assertVersionRefusals(t *testing.T, s *ConfigStore, db *sql.DB, id string) {
	t.Helper()
	ctx := context.Background()
	const notFound = "learn: unknown learned config"
	_, err := s.AppendVersion(ctx, "no-such-id", "120", nil, "applier-1")
	wantErr(t, err, cascade.KindNotFound, notFound)
	_, err = s.VersionHistory(ctx, "no-such-id")
	wantErr(t, err, cascade.KindNotFound, notFound)
	_, _, err = s.ActiveVersion(ctx, "no-such-id")
	wantErr(t, err, cascade.KindNotFound, notFound)
	_, err = s.RevertToVersion(ctx, "no-such-id", 1, "r")
	wantErr(t, err, cascade.KindNotFound, notFound)
	for _, v := range []int{0, 4, -1} {
		_, err = s.RevertToVersion(ctx, id, v, "r")
		wantErr(t, err, cascade.KindNotFound, "learn: unknown learned-config version")
	}
	_, err = s.RevertToVersion(ctx, id, 3, "r")
	wantErr(t, err, cascade.KindConflict, "learn: revert target is already active or nothing is active")
	for _, loose := range []string{"301", "900", "59"} {
		_, err = s.AppendVersion(ctx, id, loose, nil, "applier-1")
		wantErr(t, err, cascade.KindPermissionDenied, "learn: a value outside the target's bound is never stored (tighten-only)")
	}
	sec, err := s.Submit(ctx, securitySubmission())
	if err != nil {
		t.Fatalf("Submit(security): %v", err)
	}
	_, err = s.AppendVersion(ctx, sec.ID, `"open"`, nil, "applier-1")
	wantErr(t, err, cascade.KindConflict, `learn: cannot append a version while status is "pending"; requires applied`)
	if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_version"); n != 3 {
		t.Fatalf("version rows = %d after refusals, want 3", n)
	}
}

// assertOutcomeCounters: RecordOutcome counts and stamps LastVerified.
func assertOutcomeCounters(t *testing.T, s *ConfigStore, id string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.RecordOutcome(ctx, id, true); err != nil {
		t.Fatalf("RecordOutcome(success): %v", err)
	}
	if err := s.RecordOutcome(ctx, id, false); err != nil {
		t.Fatalf("RecordOutcome(failure): %v", err)
	}
	got, err := s.Get(ctx, id)
	if err != nil || got.SuccessCount != 1 || got.FailureCount != 1 || !got.LastVerified.Equal(now) {
		t.Fatalf("after outcomes = (%+v, %v)", got, err)
	}
	wantErr(t, s.RecordOutcome(ctx, "no-such-id", true), cascade.KindNotFound, "learn: unknown learned config")
}

// assertClosedDBUnavailable: with the store's db closed, writes are
// KindUnavailable and a second handle sees no partial row.
func assertClosedDBUnavailable(t *testing.T, s *ConfigStore, db *sql.DB, id string) {
	t.Helper()
	var path string
	if err := db.QueryRow("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&path); err != nil {
		t.Fatalf("db path: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()
	_, err := s.AppendVersion(ctx, id, "120", nil, "applier-1")
	wantErr(t, err, cascade.KindUnavailable, "learn: begin learned-config transaction")
	_, err = s.Submit(ctx, validSubmission())
	wantErr(t, err, cascade.KindUnavailable, "learn: begin learned-config transaction")
	_, err = s.Get(ctx, id)
	wantErr(t, err, cascade.KindUnavailable, "learn: query learned configs")
	wantErr(t, s.RecordOutcome(ctx, id, true), cascade.KindUnavailable, "learn: record learned-config outcome")
	other := openDBAt(t, path)
	if n := countQuery(t, other, "SELECT COUNT(*) FROM jobs_learned_config_version"); n != 3 {
		t.Fatalf("version rows = %d after closed-db writes, want 3", n)
	}
}
