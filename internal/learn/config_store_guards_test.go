package learn

// Purpose: exercise value, lifecycle and stored-row guards against real SQLite.
// Inputs: valid submissions and deliberately corrupted stored rows.
// Outputs: exact refusal errors and unchanged stored state.
// Constraints: isolated homes and databases; no application of rejected values.
// SPORT: internal.learn.ConfigStore.

import (
	"context"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// applyConfigForTest moves a submitted row to applied before version writes.
func applyConfigForTest(t *testing.T, s *ConfigStore, id string) {
	t.Helper()
	if err := s.UpdateStatus(context.Background(), id, StatusPending, StatusApplied); err != nil {
		t.Fatalf("apply status: %v", err)
	}
}

func TestConfigVersionShapeGuards(t *testing.T) {
	isolateHome(t)
	cases := []struct {
		name, target, value, message string
		change                       Change
		kind                         cascade.Kind
	}{
		{"numeric", "ci.local_timeout", "99999", "learn: a value outside the target's bound is never stored (tighten-only)",
			validSubmission().Change, cascade.KindPermissionDenied},
		{"add_only_list", "ci.steps", "[]", "learn: a value outside the target's bound is never stored (tighten-only)",
			Change{Op: OpAddStep, Path: "ci.local.test", Value: `"cascade context generate --check"`}, cascade.KindPermissionDenied},
		{"add_only_changed", "ci.steps", `"go build"`, "learn: a value outside the target's bound is never stored (tighten-only)",
			Change{Op: OpAddStep, Path: "ci.local.test", Value: `"cascade context generate --check"`}, cascade.KindPermissionDenied},
		{"reorder_only", "conductor.lane_order", `"lane-a"`, "learn: change value must be a TOML array of lane names",
			Change{Op: OpReorder, Path: "conductor.lane_order", Value: `["lane-a","lane-b"]`}, cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			sub := validSubmission()
			sub.Target, sub.Change = tc.target, tc.change
			c, err := s.Submit(context.Background(), sub)
			if err != nil {
				t.Fatal(err)
			}
			applyConfigForTest(t, s, c.ID)
			_, err = s.AppendVersion(context.Background(), c.ID, tc.value, nil, "applier-1")
			wantErr(t, err, tc.kind, tc.message)
			if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_version"); n != 0 {
				t.Fatalf("refused value left %d version rows", n)
			}
			if v, err := s.AppendVersion(context.Background(), c.ID, tc.change.Value, nil, "applier-1"); err != nil || v.Version != 1 {
				t.Fatalf("valid value: %+v, %v", v, err)
			}
		})
	}
}

func TestConfigVersionStatusGuards(t *testing.T) {
	isolateHome(t)
	for _, status := range []Status{StatusPending, StatusRejected, StatusReverted} {
		t.Run(string(status), func(t *testing.T) {
			s, db, _ := newTestStore(t)
			ctx := context.Background()
			c, err := s.Submit(ctx, validSubmission())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE jobs_learned_config_submission SET status = ? WHERE config_id = ?", status, c.ID); err != nil {
				t.Fatal(err)
			}
			_, err = s.AppendVersion(ctx, c.ID, "120", nil, "applier-1")
			wantErr(t, err, cascade.KindConflict, fmt.Sprintf("learn: cannot append a version while status is %q; requires applied", status))
			if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config_version"); n != 0 || storedStatus(t, db, c.ID) != string(status) {
				t.Fatalf("refused append changed state: %d versions", n)
			}
		})
	}
	c := LearnedConfig{Status: StatusRejected, Tier: TierSafe}
	wantErr(t, versionAllowed(c, "120"), cascade.KindPermissionDenied, "learn: a rejected learned config is never versioned")
}

func TestConfigStatusAllowedMoves(t *testing.T) {
	isolateHome(t)
	statuses := []Status{StatusPending, StatusApplied, StatusRejected, StatusReverted}
	for _, from := range statuses {
		for _, to := range statuses {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				s, db, _ := newTestStore(t)
				ctx := context.Background()
				c, err := s.Submit(ctx, validSubmission())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE jobs_learned_config_submission SET status = ? WHERE config_id = ?", from, c.ID); err != nil {
					t.Fatal(err)
				}
				err = s.UpdateStatus(ctx, c.ID, from, to)
				allowed := from == StatusPending && (to == StatusApplied || to == StatusRejected) ||
					from == StatusApplied && to == StatusReverted || from == StatusReverted && to == StatusApplied
				want := from
				if allowed {
					want = to
					if err != nil {
						t.Fatal(err)
					}
				} else {
					wantErr(t, err, cascade.KindConflict, fmt.Sprintf("learn: status move from %q to %q is not allowed", from, to))
				}
				if got := storedStatus(t, db, c.ID); got != string(want) {
					t.Fatalf("stored status = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestConfigSecurityStoredStateGuards(t *testing.T) {
	isolateHome(t)
	for _, stmt := range []string{
		"UPDATE jobs_learned_config_submission SET status = 'applied' WHERE config_id = ?",
		"UPDATE jobs_learned_config SET version = 1 WHERE id = ?",
	} {
		s, db, _ := newTestStore(t)
		ctx := context.Background()
		sub := validSubmission()
		sub.Change.Value = "301"
		c, err := s.Submit(ctx, sub)
		if err != nil || c.Tier != TierSecurity {
			t.Fatalf("security submission: %+v, %v", c, err)
		}
		if _, err := s.Get(ctx, c.ID); err != nil {
			t.Fatalf("valid security row: %v", err)
		}
		if _, err := db.Exec(stmt, c.ID); err != nil {
			t.Fatal(err)
		}
		const msg = "learn: a security-tier learned config cannot be applied or have versions"
		_, err = s.Get(ctx, c.ID)
		wantErr(t, err, cascade.KindInvalidInput, msg)
		_, err = s.List(ctx, ConfigFilter{})
		wantErr(t, err, cascade.KindInvalidInput, msg)
		if storedStatus(t, db, c.ID) == "applied" {
			_, err = s.List(ctx, ConfigFilter{Status: StatusApplied})
			wantErr(t, err, cascade.KindInvalidInput, msg)
		}
	}
}

func TestConfigHistoryValueGuards(t *testing.T) {
	isolateHome(t)
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	c, err := s.Submit(ctx, validSubmission())
	if err != nil {
		t.Fatal(err)
	}
	applyConfigForTest(t, s, c.ID)
	for _, value := range []string{"240", "120"} {
		if _, err := s.AppendVersion(ctx, c.ID, value, nil, "applier-1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("UPDATE jobs_learned_config_version SET value = '99999' WHERE config_id = ?", c.ID); err != nil {
		t.Fatal(err)
	}
	const msg = "learn: a value outside the target's bound is never stored (tighten-only)"
	_, ok, err := s.ActiveVersion(ctx, c.ID)
	wantErr(t, err, cascade.KindPermissionDenied, msg)
	if ok {
		t.Fatal("tampered active value was returned")
	}
	_, err = s.RevertToVersion(ctx, c.ID, 1, "regression")
	wantErr(t, err, cascade.KindPermissionDenied, msg)
	history, err := s.VersionHistory(ctx, c.ID)
	if err != nil || len(history) != 2 || history[1].RevertedAt != nil || history[1].RevertReason != "" {
		t.Fatalf("refused revert changed history: %+v, %v", history, err)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || got.Version != 2 {
		t.Fatalf("refused revert changed active pointer: %+v, %v", got, err)
	}
}

func TestConfigStoreLookupsResolveAliases(t *testing.T) {
	isolateHome(t)
	target, ok := (defaultView{}).lookup("retrieval.weights")
	if !ok || target.ID != "retrieval.fusion_weights" {
		t.Fatalf("alias lookup = %+v, %v", target, ok)
	}
	if _, ok := (defaultView{}).lookup("unknown"); ok {
		t.Fatal("unknown alias resolved")
	}
	c := LearnedConfig{Target: "ci.steps", Status: StatusApplied, Tier: TierBehavioral,
		Change: Change{Op: OpAddStep, Path: "ci.local.test", Value: `"cascade context generate --check"`}}
	if err := versionAllowed(c, c.Change.Value); err != nil {
		t.Fatalf("version alias: %v", err)
	}
	c.Target = "unknown"
	wantErr(t, versionAllowed(c, c.Change.Value), cascade.KindNotFound, "learn: unknown learned-config target or alias")
}
