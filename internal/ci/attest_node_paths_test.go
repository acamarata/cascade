// Purpose: error-path tests for the node attestor: constructor and store
// opening refusals, Execute refusals, and every Sign refusal that comes from
// a missing or broken row, key or checkout of the node's own run.
// SPORT: ci tests (EXTEND) - P1-CI-13.
package ci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wantCIFailure asserts the exact Kind and that the message names the cause.
func wantCIFailure(t *testing.T, err error, kind cascade.Kind, text string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v containing %q, got nil", kind, text)
	}
	wantCIKind(t, err, kind)
	if !strings.Contains(err.Error(), text) {
		t.Fatalf("error %q does not contain %q", err, text)
	}
}

func TestNodeAttestorConstructionRefusals(t *testing.T) {
	r := newNodeAttestRig(t)
	_, err := NewNodeAttestor(nodes.Identity{}, r.a.keys, r.db, r.clock)
	wantCIFailure(t, err, cascade.KindInvalidInput, "node attestor requires")
	ctx := context.Background()
	t.Run("opens the store and binds an attestor", func(t *testing.T) {
		a, closeDB, err := OpenNodeAttestor(ctx, r.self, r.a.keys, t.TempDir(), r.clock)
		if err != nil || a == nil {
			t.Fatalf("open: %v", err)
		}
		if err := closeDB(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a data directory that is a regular file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := OpenNodeAttestor(ctx, r.self, r.a.keys, file, r.clock)
		wantCIFailure(t, err, cascade.KindUnavailable, "create data directory")
	})
	t.Run("an identity the attestor refuses releases the store", func(t *testing.T) {
		_, _, err := OpenNodeAttestor(ctx, nodes.Identity{}, r.a.keys, t.TempDir(), r.clock)
		wantCIFailure(t, err, cascade.KindInvalidInput, "node attestor requires")
	})
}

func TestNodeAttestorExecuteRefusals(t *testing.T) {
	r := newNodeAttestRig(t)
	ctx := context.Background()
	cfg := RunnerConfig{RepoRoot: r.repo, Steps: []RunnerStep{{Kind: StepLint, Command: "lint"}}}
	deps := Deps{Clock: r.clock, Exec: r.passExec}
	t.Run("no execution id", func(t *testing.T) {
		_, err := r.a.Execute(ctx, deps, cfg, "", -51, 7, "repo")
		wantCIFailure(t, err, cascade.KindInvalidInput, "needs an execution id")
	})
	t.Run("no checkout", func(t *testing.T) {
		_, err := r.a.Execute(ctx, deps, RunnerConfig{}, "exec-1", -51, 7, "repo")
		wantCIFailure(t, err, cascade.KindInvalidInput, "needs an execution id")
	})
	t.Run("a checkout that is not a git repository", func(t *testing.T) {
		bad := cfg
		bad.RepoRoot = t.TempDir()
		_, err := r.a.Execute(ctx, deps, bad, "exec-1", -52, 7, "repo")
		wantCIFailure(t, err, cascade.KindUnavailable, "resolve the checkout commit")
	})
	t.Run("a run the runner refuses", func(t *testing.T) {
		_, err := r.a.Execute(ctx, Deps{Clock: r.clock}, cfg, "exec-1", -53, 7, "repo")
		wantCIFailure(t, err, cascade.KindInvalidInput, "non-nil Executor")
	})
}

type signBreakCase struct {
	name    string
	breakIt func(t *testing.T, r nodeAttestRig)
	kind    cascade.Kind
	text    string
}

// rowBreakCases damage the node's own tables after a real run.
func rowBreakCases() []signBreakCase {
	return []signBreakCase{
		{"run binding table gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DROP TABLE ci_attest_run`) },
			cascade.KindUnavailable, "read ci_attest_run"},
		{"run row gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DELETE FROM ci_run`) },
			cascade.KindPermissionDenied, "has no result on this node"},
		{"run table gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DROP TABLE ci_run`) },
			cascade.KindUnavailable, "read node run"},
		{"run source table gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DROP TABLE ci_run_source`) },
			cascade.KindUnavailable, "reading run source"},
		{"job table gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DROP TABLE ci_job`) },
			cascade.KindUnavailable, "read node run jobs"},
		{"no job recorded", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DELETE FROM ci_job`) },
			cascade.KindPermissionDenied, "jobs are not terminal"},
		{"step table gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DROP TABLE ci_step`) },
			cascade.KindUnavailable, "read node run steps"},
		{"no step recorded", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DELETE FROM ci_step`) },
			cascade.KindPermissionDenied, "recorded no step"},
		{"step row with a non-numeric number", func(t *testing.T, r nodeAttestRig) {
			mustExec(t, r, `UPDATE ci_step SET number='not-a-number' WHERE rowid=(SELECT min(rowid) FROM ci_step)`)
		},
			cascade.KindUnavailable, "scan node run step"},
	}
}

// hostBreakCases damage the checkout, the key and the signed-state table.
func hostBreakCases(ctx context.Context) []signBreakCase {
	return []signBreakCase{
		{"checkout deleted", func(t *testing.T, r nodeAttestRig) {
			if err := os.RemoveAll(r.repo); err != nil {
				t.Fatal(err)
			}
		}, cascade.KindUnavailable, "read the run's tree"},
		{"node key deleted", func(t *testing.T, r nodeAttestRig) {
			if err := r.a.keys.Delete(ctx, r.self.NodeID); err != nil {
				t.Fatal(err)
			}
		}, cascade.KindUnavailable, "sign the attestation"},
		{"signed-state table gone", func(t *testing.T, r nodeAttestRig) { mustExec(t, r, `DROP TABLE ci_attest_signed`) },
			cascade.KindUnavailable, "write node attestation state"},
	}
}

func TestNodeSignRefusalsBeforeAndAfterTheRun(t *testing.T) {
	ctx := context.Background()
	t.Run("expired request", func(t *testing.T) {
		r := newNodeAttestRig(t)
		req := r.request
		req.ExpiresAt = r.clock.Now().Add(-time.Second)
		_, err := r.a.Sign(ctx, req)
		wantCIFailure(t, err, cascade.KindPermissionDenied, "has expired")
	})
	t.Run("request missing a field", func(t *testing.T) {
		r := newNodeAttestRig(t)
		req := r.request
		req.CheckpointID = ""
		_, err := r.a.Sign(ctx, req)
		wantCIFailure(t, err, cascade.KindInvalidInput, "missing a field")
	})
	cases := append(rowBreakCases(), hostBreakCases(ctx)...)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newNodeAttestRig(t)
			r.run(t, "exec-1", int64(-60-i), r.passExec)
			tc.breakIt(t, r)
			_, err := r.a.Sign(ctx, r.request)
			wantCIFailure(t, err, tc.kind, tc.text)
		})
	}
}

func mustExec(t *testing.T, r nodeAttestRig, stmt string) {
	t.Helper()
	if _, err := r.db.Exec(stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}
