// Purpose: the refusals AcceptanceRerun makes before it dispatches anything.
// SPORT: internal.ci.Dispatcher/AcceptanceRerun (P1-CI-02).
package ci

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestAcceptanceRerunRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		want    string
		breakIt func(*testing.T, *streamRig)
	}{
		"fence": {"fence down", func(_ *testing.T, r *streamRig) {
			r.d.deps.Fence = func(context.Context, string, string, int64) error { return errors.New("fence down") }
		}},
		"stored tier unreadable": {"tier unreadable", func(_ *testing.T, r *streamRig) {
			r.d.deps.StoredSensitivity = func(context.Context, string) (provider.SensitivityTier, error) {
				return provider.SensitivityInternal, errors.New("tier unreadable")
			}
		}},
		"commit tree unresolvable": {"resolve checkpoint tree", func(t *testing.T, _ *streamRig) {
			saved := acceptanceCommitTree
			acceptanceCommitTree = func(context.Context, string, string) (string, error) { return "", errors.New("no tree") }
			t.Cleanup(func() { acceptanceCommitTree = saved })
		}},
		"executor fails": {"executor down", func(_ *testing.T, r *streamRig) {
			r.exec.fn = func(SubJob) (SubJobResult, error) { return SubJobResult{}, errors.New("executor down") }
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := seededAcceptanceRig(t, nil)
			tc.breakIt(t, r)
			if _, err := r.d.AcceptanceRerun(context.Background(), r.ref, RequirementModel{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AcceptanceRerun = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAcceptanceRerunRefusesATombstonedAttempt(t *testing.T) {
	r, snap := seededAcceptanceRig(t, nil)
	if _, err := r.ciDB.Exec("UPDATE ci_stream_attempt SET state = ? WHERE attempt_id = ?", attemptTombstoned, snap.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.d.AcceptanceRerun(context.Background(), r.ref, RequirementModel{}); err == nil {
		t.Fatal("accepted a tombstoned attempt")
	}
}

func TestAcceptanceRerunRefusesACorruptCachedVerdict(t *testing.T) {
	r, _ := seededAcceptanceRig(t, nil)
	if _, err := r.bus.Publish(context.Background(), EventNamespace, acceptanceCompleted, streamEventSource, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.d.AcceptanceRerun(context.Background(), r.ref, RequirementModel{}); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("corrupt cache = %v", err)
	}
}
