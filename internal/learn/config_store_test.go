package learn

// Purpose: Submit as the single ingress (computed tier, one transaction,
//   privacy refusals with zero rows), the exported write surface, and the
//   source scan proving no other code writes a learned-config table.
// SPORT: learn/config_store_test (P1-LRN-01).

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestStore returns a store over a fresh migrated db and its clock.
func newTestStore(t *testing.T) (*ConfigStore, *sql.DB, *runtime.FixedClock) {
	t.Helper()
	db := openTestDB(t)
	if err := applyConfigSchema(db); err != nil {
		t.Fatalf("apply learn-config: %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	s, err := NewConfigStore(db, clock)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return s, db, clock
}

// validSubmission is a clean, safe proposal (ci.local timeout 120).
func validSubmission() Submission {
	return Submission{
		Source: SourceRef{Kind: SourceDetector, ID: "det-01"}, Target: "ci.local_timeout", Scope: "repo-a",
		Label: "timeout-tighten", Confidence: 0.8,
		Change:   Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "120"},
		Evidence: []EvidenceRef{{Kind: "telemetry_outcome", ID: "ev-01", RepoID: "repo-a", LaneID: "lane-1"}},
	}
}

// countRows runs a COUNT(*) query.
func countQuery(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// configRows counts the config and submission rows.
func configRows(t *testing.T, db *sql.DB) (int, int) {
	t.Helper()
	return countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config"),
		countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_submission")
}

func TestConfigStoreSubmitIsSingleIngress(t *testing.T) {
	isolateHome(t)
	assertNoStatedTier(t, reflect.TypeOf(Submission{}))
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	c, err := s.Submit(ctx, validSubmission())
	if err != nil || c.Tier != TierSafe || c.Status != StatusPending || c.Version != 0 {
		t.Fatalf("Submit(valid) = (%+v, %v)", c, err)
	}
	var tier, status string
	var version int
	if err := db.QueryRow(`SELECT c.tier, c.version, s.status FROM jobs_learned_config c JOIN jobs_learned_config_submission s
		ON s.config_id = c.id WHERE c.id = ?`, c.ID).Scan(&tier, &version, &status); err != nil {
		t.Fatalf("read stored row: %v", err)
	}
	if tier != "safe" || version != 0 || status != "pending" || countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_version") != 0 {
		t.Fatalf("stored (%s, v%d, %s), want (safe, v0, pending) and no version row", tier, version, status)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("Get = (%+v, %v), want %+v", got, err, c)
	}
	loose := validSubmission()
	loose.Change.Value = "900"
	lc, err := s.Submit(ctx, loose)
	if err != nil || lc.Tier != TierSecurity || !lc.LoosensBound {
		t.Fatalf("Submit(loosening) = (%+v, %v), want a stored security row", lc, err)
	}
	assertSubmitRefusals(t, s, db, 2)
	assertSubmitWithRollback(t, s, db, 2)
	assertWriteSurface(t)
}

// assertNoStatedTier walks a type and refuses any field that could carry a
// caller-stated tier, area list or bound flag.
func assertNoStatedTier(t *testing.T, typ reflect.Type) {
	t.Helper()
	seen := 0
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Slice || rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			seen++
			if f.Type == reflect.TypeOf(TierSafe) || slices.Contains([]string{"Tier", "Areas", "LoosensBound"}, f.Name) {
				t.Errorf("%s.%s lets a caller state a tier", rt.Name(), f.Name)
			}
			walk(f.Type)
		}
	}
	walk(typ)
	if seen < 10 {
		t.Fatalf("walked %d fields; the reflection check is not reading Submission", seen)
	}
}

// assertSubmitRefusals: each bad submission refuses with its message and
// leaves exactly want rows beside the clean ones already stored.
func assertSubmitRefusals(t *testing.T, s *ConfigStore, db *sql.DB, want int) {
	t.Helper()
	mut := func(f func(*Submission)) Submission { sub := validSubmission(); f(&sub); return sub }
	field := func(name, rule string) string { return `learn: field "` + name + `" must not carry ` + rule }
	cases := []struct {
		sub  Submission
		kind cascade.Kind
		msg  string
	}{
		{mut(func(s *Submission) { s.Evidence = nil }), cascade.KindInvalidInput, "learn: submission needs at least one evidence ref"},
		{mut(func(s *Submission) { s.Label = "someone@host-a.invalid" }), cascade.KindInvalidInput, field("Label", "an e-mail address or user@host")},
		{mut(func(s *Submission) { s.Scope = "/srv/repo-a" }), cascade.KindInvalidInput, field("Scope", "an absolute or home path")},
		{mut(func(s *Submission) { s.Source.ID = "host-a.invalid" }), cascade.KindInvalidInput, field("Source.ID", "a host name or IP address")},
		{mut(func(s *Submission) { s.Change.Value = "https://host-a.invalid/x" }), cascade.KindInvalidInput, field("Change.Value", "a URL with a host")},
		{mut(func(s *Submission) { s.Evidence[0].ID = `C:\work` }), cascade.KindInvalidInput, field("Evidence[0].ID", "a Windows drive path")},
		{mut(func(s *Submission) { s.Evidence[0].LaneID = "git@host-a.invalid:o/r" }), cascade.KindInvalidInput, field("Evidence[0].LaneID", "an e-mail address or user@host")},
		{mut(func(s *Submission) { s.Evidence[0].ID = "" }), cascade.KindInvalidInput, "learn: evidence ref 0 needs a kind and an id"},
		{mut(func(s *Submission) { s.Source.Kind = "human" }), cascade.KindInvalidInput, "learn: unknown source kind"},
		{mut(func(s *Submission) { s.Source.ID = " " }), cascade.KindInvalidInput, "learn: submission needs a source id"},
		{mut(func(s *Submission) { s.Confidence = 1.5 }), cascade.KindInvalidInput, "learn: submission confidence must lie in [0, 1]"},
		{mut(func(s *Submission) { s.Target = "auth.rule" }), cascade.KindNotFound, "learn: unknown learned-config target or alias"},
		{mut(func(s *Submission) { s.Change.Op = "drop" }), cascade.KindInvalidInput, "learn: change op must be set, add_step or reorder"},
	}
	for _, tc := range cases {
		c, err := s.Submit(context.Background(), tc.sub)
		if c.ID != "" {
			t.Fatalf("a refused submission returned a config %+v", c)
		}
		wantErr(t, err, tc.kind, tc.msg)
		if cfg, sub := configRows(t, db); cfg != want || sub != want {
			t.Fatalf("after refusal %q: %d config / %d submission rows, want %d", tc.msg, cfg, sub, want)
		}
	}
}

// assertSubmitWithRollback: an error from extra rolls both rows back; a nil
// extra error commits both rows with extra seeing the classified config.
func assertSubmitWithRollback(t *testing.T, s *ConfigStore, db *sql.DB, want int) {
	t.Helper()
	sentinel := errors.New("extra refused")
	_, err := s.SubmitWith(context.Background(), validSubmission(), func(tx *sql.Tx, c LearnedConfig) error {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM jobs_learned_config WHERE id = ?", c.ID).Scan(&n); err != nil || n != 1 {
			t.Errorf("extra does not see its own row inside the transaction (%d, %v)", n, err)
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("SubmitWith returned %v, want the extra's own error", err)
	}
	if cfg, sub := configRows(t, db); cfg != want || sub != want {
		t.Fatalf("SubmitWith rollback left %d/%d rows, want %d", cfg, sub, want)
	}
	c, err := s.SubmitWith(context.Background(), validSubmission(), func(_ *sql.Tx, c LearnedConfig) error {
		if c.Tier != TierSafe {
			t.Errorf("extra saw tier %q", c.Tier)
		}
		return nil
	})
	if err != nil || c.ID == "" {
		t.Fatalf("SubmitWith(nil extra error) = (%+v, %v)", c, err)
	}
	if cfg, sub := configRows(t, db); cfg != want+1 || sub != want+1 {
		t.Fatalf("SubmitWith commit left %d/%d rows, want %d", cfg, sub, want+1)
	}
}

// assertWriteSurface pins ConfigStore's exported methods to the contract,
// so no second write door can appear unreviewed.
func assertWriteSurface(t *testing.T) {
	t.Helper()
	want := []string{"ActiveVersion", "AppendVersion", "Get", "List", "RecordOutcome", "RevertToVersion",
		"Submit", "SubmitWith", "UpdateStatus", "VersionHistory"}
	rt := reflect.TypeOf(&ConfigStore{})
	var got []string
	for i := 0; i < rt.NumMethod(); i++ {
		got = append(got, rt.Method(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ConfigStore methods = %v, want %v", got, want)
	}
	if _, err := NewConfigStore(nil, runtime.NewFixedClock(time.Time{})); err == nil {
		t.Fatal("NewConfigStore accepted a nil db")
	}
}
