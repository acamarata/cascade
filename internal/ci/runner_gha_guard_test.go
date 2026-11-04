package ci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type hostedVisibility struct {
	private bool
	err     error
	calls   int
}

func (v *hostedVisibility) RepoVisibility(_ context.Context, owner, repo string) (bool, error) {
	v.calls++
	if owner != "owner" || repo != "repo" {
		panic("unexpected repository")
	}
	return v.private, v.err
}

func (*hostedVisibility) CommitExists(context.Context, string, string, string) (bool, error) {
	panic("guard must not resolve again")
}

type hostedAttention struct {
	items []supervision.AttentionItem
	err   error
}

func (a *hostedAttention) Push(_ context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error) {
	a.items = append(a.items, item)
	return item, a.err
}

// TestHostedGuardRefusals proves each policy refusal produces audit evidence.
func TestHostedGuardRefusals(t *testing.T) {
	for _, condition := range []string{"unset", "false", "unadmitted", "origin", "private", "unknown", "reader", "sensitivity"} {
		t.Run(condition, func(t *testing.T) {
			v := &hostedVisibility{}
			a := &hostedAttention{}
			var rows []Refusal
			now := time.Unix(123, 0)
			g := HostedGuard{Allowed: func() (bool, error) { return true, nil }, Visibility: v,
				Clock: runtime.NewFixedClock(now), Attention: a,
				Refusals: func(_ context.Context, r Refusal) { rows = append(rows, r) }}
			spec := JobSpec{JobID: "job", Sensitivity: provider.SensitivityPublic}
			origin := "https://github.com/owner/repo.git"
			switch condition {
			case "unset":
				g.Allowed = nil
			case "false":
				g.Allowed = func() (bool, error) { return false, nil }
			case "unadmitted":
				g.Allowed = func() (bool, error) { return true, errors.New("not admitted") }
			case "origin":
				origin = "https://github.com.evil.test/owner/repo"
			case "private":
				v.private = true
			case "unknown":
				v.err = errors.New("offline")
			case "reader":
				g.Visibility = nil
			case "sensitivity":
				spec.Sensitivity = provider.SensitivityRestricted
			}
			owner, repo, err := g.Check(context.Background(), spec, origin)
			kind, _ := cascade.KindOf(err)
			if !errChainHas(err, ErrHostedExecutorRefused) || kind != cascade.KindPolicyDenied || owner != "" || repo != "" {
				t.Fatalf("refusal: %q/%q %v", owner, repo, err)
			}
			if len(rows) != 1 || rows[0].Step != ExecutorGitHubActions || rows[0].At != now || !strings.Contains(err.Error(), rows[0].Reason) || len(a.items) != 1 {
				t.Fatalf("missing refusal evidence: %v, %v", rows, a.items)
			}
			if condition != "private" && condition != "unknown" && v.calls != 0 {
				t.Fatalf("visibility queried after an earlier refusal: %d calls", v.calls)
			}
		})
	}
}

// TestHostedGuardLiveVisibility proves both origin forms use fresh visibility.
func TestHostedGuardLiveVisibility(t *testing.T) {
	v := &hostedVisibility{}
	g := HostedGuard{Allowed: func() (bool, error) { return true, nil }, Visibility: v,
		Clock: runtime.NewFixedClock(time.Unix(123, 0)), Attention: &hostedAttention{}, Refusals: func(context.Context, Refusal) {}}
	for _, origin := range []string{"https://github.com/owner/repo.git", "git@github.com:owner/repo.git"} {
		owner, repo, err := g.Check(context.Background(), JobSpec{Sensitivity: provider.SensitivityPublic}, origin)
		if err != nil || owner != "owner" || repo != "repo" {
			t.Fatalf("check: %s/%s %v", owner, repo, err)
		}
	}
	v.private = true
	if _, _, err := g.Check(context.Background(), JobSpec{Sensitivity: provider.SensitivityPublic}, "https://github.com/owner/repo"); err == nil || v.calls != 3 {
		t.Fatalf("visibility was cached: calls=%d err=%v", v.calls, err)
	}
}

// TestHostedGuardAdmissionRevocation proves allow is read again for each check.
func TestHostedGuardAdmissionRevocation(t *testing.T) {
	allowed := true
	v := &hostedVisibility{}
	a := &hostedAttention{err: errors.New("queue unavailable")}
	var rows []Refusal
	g := HostedGuard{Allowed: func() (bool, error) { return allowed, nil }, Visibility: v,
		Clock: runtime.NewFixedClock(time.Unix(123, 0)), Attention: a,
		Refusals: func(_ context.Context, r Refusal) { rows = append(rows, r) }}
	spec := JobSpec{JobID: "job", Sensitivity: provider.SensitivityPublic}
	if _, _, err := g.Check(context.Background(), spec, "https://github.com/owner/repo"); err != nil {
		t.Fatal(err)
	}
	allowed = false
	_, _, err := g.Check(context.Background(), spec, "https://github.com/owner/repo")
	if !errChainHas(err, ErrHostedExecutorRefused) || !errors.Is(err, a.err) || len(rows) != 1 || v.calls != 1 {
		t.Fatalf("revoked admission or queue failure lost: %v, rows=%v, calls=%d", err, rows, v.calls)
	}
}
