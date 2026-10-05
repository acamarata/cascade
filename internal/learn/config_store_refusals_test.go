package learn

// Purpose: the store's fail-closed edges: List filters, stored rows that are
//   unreadable or carry unknown enums (refused, never defaulted), version
//   input refusals, and write failures injected through the real SQLite
//   engine (a dropped table, a reader holding the commit lock), each
//   checked against stored state.
// SPORT: learn/config_store_refusals_test (P1-LRN-01).

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestConfigStoreListFilters(t *testing.T) {
	isolateHome(t)
	s, _, clock := newTestStore(t)
	ctx := context.Background()
	safe, err := s.Submit(ctx, validSubmission())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	clock.Advance(time.Second)
	sub := securitySubmission()
	sub.Scope = "repo-b"
	sec, err := s.Submit(ctx, sub)
	if err != nil {
		t.Fatalf("Submit(security): %v", err)
	}
	all, err := s.List(ctx, ConfigFilter{})
	if err != nil || len(all) != 2 || all[0].ID != sec.ID || all[1].ID != safe.ID {
		t.Fatalf("List(any) = (%d rows, %v), want newest first", len(all), err)
	}
	for _, tc := range []struct {
		f    ConfigFilter
		want string
	}{
		{ConfigFilter{Tier: TierSecurity}, sec.ID}, {ConfigFilter{Scope: "repo-a"}, safe.ID},
		{ConfigFilter{Status: StatusPending, Tier: TierSafe}, safe.ID},
	} {
		got, err := s.List(ctx, tc.f)
		if err != nil || len(got) != 1 || got[0].ID != tc.want {
			t.Fatalf("List(%+v) = (%+v, %v)", tc.f, got, err)
		}
	}
	if got, err := s.List(ctx, ConfigFilter{Status: StatusApplied}); err != nil || len(got) != 0 {
		t.Fatalf("List(applied) = (%d, %v), want none", len(got), err)
	}
	_, err = s.List(ctx, ConfigFilter{Tier: "SAFE"})
	wantErr(t, err, cascade.KindInvalidInput, "learn: config tier must be safe, behavioral or security")
	_, err = s.List(ctx, ConfigFilter{Status: "done"})
	wantErr(t, err, cascade.KindInvalidInput, "learn: status must be pending, applied, rejected or reverted")
}

func TestConfigStoreRefusesUnreadableRows(t *testing.T) {
	isolateHome(t)
	cases := []struct {
		stmt string
		msg  string
	}{
		{"UPDATE jobs_learned_config SET tier = 'unknown'", "learn: config tier must be safe, behavioral or security"},
		{"UPDATE jobs_learned_config SET areas = 'network'", "learn: unknown denylist area"},
		{"UPDATE jobs_learned_config SET source_kind = 'human'", "learn: unknown source kind"},
		{"UPDATE jobs_learned_config_submission SET status = 'done'", "learn: status must be pending, applied, rejected or reverted"},
		{"UPDATE jobs_learned_config SET loosens_bound = 2", "learn: stored bound flag is not 0 or 1"},
		{"UPDATE jobs_learned_config SET target_id = 'auth.rules'", "learn: change path is outside the resolved target's paths"},
		{`UPDATE jobs_learned_config_submission SET change_json = '{"Op":"set"} {}'`, "learn: stored learned-config json has trailing data"},
	}
	for _, tc := range cases {
		s, db, _ := newTestStore(t)
		c, err := s.Submit(context.Background(), validSubmission())
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if _, err := db.Exec(tc.stmt); err != nil {
			t.Fatalf("tamper %q: %v", tc.stmt, err)
		}
		_, err = s.Get(context.Background(), c.ID)
		wantErr(t, err, cascade.KindInvalidInput, tc.msg)
	}
	for _, raw := range []string{`{"Op":"set","Extra":1}`, `not json`} {
		s, db, _ := newTestStore(t)
		c, _ := s.Submit(context.Background(), validSubmission())
		if _, err := db.Exec("UPDATE jobs_learned_config_submission SET evidence_json = ?", raw); err != nil {
			t.Fatalf("tamper: %v", err)
		}
		_, err := s.Get(context.Background(), c.ID)
		wantErr(t, err, cascade.KindInvalidInput, "learn: stored learned-config json is unreadable")
	}
}

func TestConfigVersionInputRefusals(t *testing.T) {
	isolateHome(t)
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	c, _ := s.Submit(ctx, validSubmission())
	applyConfigForTest(t, s, c.ID)
	nan := math.NaN()
	_, err := s.AppendVersion(ctx, c.ID, "120", &nan, "applier-1")
	wantErr(t, err, cascade.KindInvalidInput, "learn: version baseline must be finite")
	_, err = s.AppendVersion(ctx, c.ID, "120", nil, " ")
	wantErr(t, err, cascade.KindInvalidInput, "learn: version needs an applied-by id")
	_, err = s.AppendVersion(ctx, c.ID, "120", nil, "ops@host-a.invalid")
	wantErr(t, err, cascade.KindInvalidInput, `learn: field "AppliedBy" must not carry an e-mail address or user@host`)
	_, err = s.AppendVersion(ctx, c.ID, "/etc/x", nil, "applier-1")
	wantErr(t, err, cascade.KindInvalidInput, `learn: field "Value" must not carry an absolute or home path`)
	_, err = s.AppendVersion(ctx, c.ID, "abc", nil, "applier-1")
	wantErr(t, err, cascade.KindInvalidInput, "learn: change value must be a TOML decimal number")
	if _, ok, err := s.ActiveVersion(ctx, c.ID); ok || err != nil {
		t.Fatalf("ActiveVersion with no versions = (%v, %v), want (false, nil)", ok, err)
	}
	_, err = s.RevertToVersion(ctx, c.ID, 1, "")
	wantErr(t, err, cascade.KindInvalidInput, "learn: a revert needs a reason")
	_, err = s.RevertToVersion(ctx, c.ID, 1, "see https://host-a.invalid/x")
	wantErr(t, err, cascade.KindInvalidInput, `learn: field "RevertReason" must not carry a URL with a host`)
	_, err = s.RevertToVersion(ctx, c.ID, 1, "r")
	wantErr(t, err, cascade.KindNotFound, "learn: unknown learned-config version")
	steps := validSubmission()
	steps.Target, steps.Change = "ci.steps", Change{Op: OpAddStep, Path: "ci.local.lint", Value: `"cascade context generate --check"`}
	b, err := s.Submit(ctx, steps)
	if err != nil || b.Tier != TierBehavioral {
		t.Fatalf("Submit(behavioral) = (%+v, %v)", b, err)
	}
	applyConfigForTest(t, s, b.ID)
	if v, err := s.AppendVersion(ctx, b.ID, steps.Change.Value, nil, "applier-1"); err != nil || v.Version != 1 {
		t.Fatalf("AppendVersion(behavioral step) = (%+v, %v)", v, err)
	}
	sec, _ := s.Submit(ctx, securitySubmission())
	_, err = s.RevertToVersion(ctx, sec.ID, 1, "r")
	wantErr(t, err, cascade.KindPermissionDenied, "learn: a security-tier learned config is never applied (C11)")
	if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_version"); n != 1 {
		t.Fatalf("version rows = %d, want only the behavioral one", n)
	}
}

func TestConfigStoreInjectedWriteFailures(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	for table, msg := range map[string]string{
		"jobs_learned_config_submission": "learn: insert learned-config submission",
		"jobs_learned_config_version":    "learn: read next version",
	} {
		s, db, _ := newTestStore(t)
		c, err := s.Submit(ctx, validSubmission())
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		applyConfigForTest(t, s, c.ID)
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
		if table == "jobs_learned_config_submission" {
			_, err = s.Submit(ctx, validSubmission())
		} else {
			_, err = s.AppendVersion(ctx, c.ID, "120", nil, "applier-1")
		}
		wantErr(t, err, cascade.KindUnavailable, msg)
		if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config"); n != 1 {
			t.Fatalf("after injected %s failure: %d config rows, want 1 (rolled back)", table, n)
		}
	}
	assertCommitFailureRollsBack(t)
	assertLookupMisses(t)
}

// assertCommitFailureRollsBack holds a read lock from a second connection so
// the store's COMMIT cannot take the write lock: Submit is KindUnavailable
// and, once the reader lets go, no row was kept.
func assertCommitFailureRollsBack(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "commit.db")
	db := openDBAt(t, path)
	if err := applyConfigSchema(db); err != nil {
		t.Fatalf("apply: %v", err)
	}
	s, _ := NewConfigStore(db, runtime.NewFixedClock(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)))
	reader := openDBAt(t, path)
	rtx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin reader: %v", err)
	}
	if n := 0; rtx.QueryRow("SELECT COUNT(*) FROM jobs_learned_config").Scan(&n) != nil {
		t.Fatal("reader could not take its shared lock")
	}
	_, err = s.Submit(context.Background(), validSubmission())
	_ = rtx.Rollback()
	if err == nil {
		t.Fatal("Submit committed while a reader held the shared lock; the commit failure was not injected")
	}
	wantErr(t, err, cascade.KindUnavailable, "learn: commit learned-config transaction")
	if n := countQuery(t, reader, "SELECT COUNT(*) FROM jobs_learned_config"); n != 0 {
		t.Fatalf("a failed commit kept %d rows", n)
	}
}

// assertLookupMisses: a registry miss is absence, and a resolved id with no
// row refuses rather than classifying.
func assertLookupMisses(t *testing.T) {
	t.Helper()
	if _, ok := defaultRegistry.lookup("no.such"); ok {
		t.Fatal("lookup found an unregistered id")
	}
	_, err := classifyWith(ghostView{}, "ghost", Change{Op: OpSet, Path: "a.b", Value: "1"})
	wantErr(t, err, cascade.KindNotFound, "learn: resolved target has no registry row")
	_, err = newRegistry([]Target{{ID: "x.knob", Paths: []FieldPath{""}, Tier: TierSafe, Shape: ShapeAddOnly,
		Direction: DirectionNotALimit}}, nil)
	wantErr(t, err, cascade.KindInvalidInput, `learn: target "x.knob": a path is not a valid matcher`)
}

// ghostView resolves every name to an id the registry does not hold.
type ghostView struct{ defaultView }

func (ghostView) resolve(string) (TargetID, error) { return "ghost.target", nil }
