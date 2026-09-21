package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the CR fix D1 proofs. The rejected draft refused a local-only diff
//   on a REGEX over the caller's free-text Context, which three ordinary
//   spellings walked straight past, and then dispatched CR-A/CR-B at
//   sensitivity `internal` -- BELOW the review task class's own
//   SensitivityDefault. Both are gone. What replaces them is proven here, the
//   way internal/context/pipeline_lane_test.go proves its own lane policy:
//   the reviewer's OWN ModelRequest, routed through the REAL
//   conductor.DefaultRouter over the REAL §5.16 taxonomy, with the caller's
//   ctx carrying a local-only thread privacy mode.
// Constraints: no live provider, no socket. The router and its filters are
//   real; the registry, spiller and clock are doubles (Art.2).
// SPORT: internal/review.router-proof (ADD, P1-E25-W5-S52-T4).

// mustPlan builds the Plan a level would dispatch, failing the test rather
// than returning an error a caller might ignore.
func mustPlan(t *testing.T, level provider.ReviewCRLevel, consequence ConsequenceClass, diff, reqContext string) Plan {
	t.Helper()
	p, err := NewPlan(provider.ReviewRequest{Level: level, Diff: diff, Context: reqContext}, consequence)
	if err != nil {
		t.Fatalf("NewPlan(%s): %v", level, err)
	}
	return p
}

// TestReviewProvider_LocalOnlySensitivityRefused is the ticket's named root
// test, rebuilt as a REAL-ROUTER proof. A local-only thread, a registry whose
// only lanes are external, and the request the reviewer itself builds: the
// router refuses, and ZERO dispatches happen. The three Context spellings
// the CR walked past the old regex with are fed in as well -- they change
// nothing now, because the refusal no longer depends on the text at all.
func TestReviewProvider_LocalOnlySensitivityRefused(t *testing.T) {
	bypassSpellings := []string{
		"sensitivity: local-only (do not egress)",
		"# sensitivity: local-only",
		`{"sensitivity":"local-only"}`,
		"", // and with no tag whatsoever: the thread's mode is what decides
	}
	diff := "diff --git a/x.go b/x.go\n+secret"

	for _, level := range []provider.ReviewCRLevel{provider.ReviewCRLevelB, provider.ReviewCRLevelC} {
		for _, spelling := range bypassSpellings {
			t.Run(string(level)+"/"+spelling, func(t *testing.T) {
				reg := externalOnlyRegistry()
				exec := &routerExecutor{router: realRouter(t, reg)}
				p, err := NewProvider(exec, reg, nil)
				if err != nil {
					t.Fatalf("NewProvider: %v", err)
				}
				_, err = p.Review(localOnlyThreadContext(),
					provider.ReviewRequest{Level: level, Diff: diff, Context: spelling})
				requireRouterPrivacyRefusal(t, err)
				if exec.dispatches != 0 {
					t.Errorf("%s dispatched %d times on a local-only thread with only external lanes, want 0",
						level, exec.dispatches)
				}
			})
		}
	}
}

// requireRouterPrivacyRefusal asserts the error is the ROUTER's own
// sensitivity refusal: the conductor sentinel, the policy-denied kind, and a
// message naming the local-only mode. Identity is checked through the
// message as well, because (*cascade.Error).Is compares Kind alone.
func requireRouterPrivacyRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a local-only thread with only external lanes was not refused: Review returned nil error")
	}
	if !errors.Is(err, conductor.ErrSensitivityViolation) {
		t.Errorf("error = %v, want conductor.ErrSensitivityViolation", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("error kind = %v (ok=%v), want KindPolicyDenied", kind, ok)
	}
	if !strings.Contains(err.Error(), "local-only") {
		t.Errorf("error message %q does not name the local-only mode", err.Error())
	}
}

// TestReviewCRALocalOnlyNeverReachesExternalLane closes the other half of the
// old hole: CR-A was EXEMPT from the regex refusal, so a local-only artifact
// at CR-A went out on an external lane. Under the same local-only ctx CR-A is
// refused too when only external lanes exist, and when a controller-local
// lane IS available it lands on THAT lane, never the external one.
func TestReviewCRALocalOnlyNeverReachesExternalLane(t *testing.T) {
	t.Run("external only: refused", func(t *testing.T) {
		reg := externalOnlyRegistry()
		exec := &routerExecutor{router: realRouter(t, reg)}
		p, err := NewProvider(exec, reg, nil)
		if err != nil {
			t.Fatalf("NewProvider: %v", err)
		}
		_, err = p.Review(localOnlyThreadContext(), provider.ReviewRequest{
			Level: provider.ReviewCRLevelA, Diff: "diff --git a/x.go b/x.go\n+secret",
		})
		requireRouterPrivacyRefusal(t, err)
		if exec.dispatches != 0 {
			t.Errorf("CR-A dispatched %d times on a local-only thread, want 0", exec.dispatches)
		}
	})

	t.Run("local lane available: lands local", func(t *testing.T) {
		reg := externalOnlyRegistry().withLocalLane()
		exec := &routerExecutor{router: realRouter(t, reg)}
		p, err := NewProvider(exec, reg, nil)
		if err != nil {
			t.Fatalf("NewProvider: %v", err)
		}
		if _, err := p.Review(localOnlyThreadContext(), provider.ReviewRequest{
			Level: provider.ReviewCRLevelA, Diff: "diff --git a/x.go b/x.go\n+secret",
		}); err != nil {
			t.Fatalf("CR-A with a controller-local lane available: %v", err)
		}
		if len(exec.lanes) != 1 || exec.lanes[0] != localLaneName {
			t.Fatalf("CR-A landed on lanes %v, want exactly [%s]", exec.lanes, localLaneName)
		}
	})
}

// TestReviewDispatchSensitivityIsTaskClassDefault is the assertion the router
// alone cannot make: filterSensitivity's restricted/internal/public legs
// remove no candidate (its own recorded contract deviation), so a dispatch
// silently downgraded from restricted to internal would still route. This
// pins every level's dispatched Sensitivity -- and its Requirements -- to the
// REAL §5.16 row for its task class, so the downgrade the CR found cannot
// come back unnoticed.
func TestReviewDispatchSensitivityIsTaskClassDefault(t *testing.T) {
	rows := conductor.TaskClasses()
	cases := []struct {
		level provider.ReviewCRLevel
		class conductor.TaskClass
		// cheaperOK marks CR-A's single documented deviation: the ticket
		// pins it to medium/32k, BELOW the review row's high/200k. The
		// sensitivity tier is never part of that deviation.
		cheaperOK bool
	}{
		{provider.ReviewCRLevelA, conductor.TaskClassReview, true},
		{provider.ReviewCRLevelB, conductor.TaskClassReview, false},
		{provider.ReviewCRLevelC, conductor.TaskClassArbitrate, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.level), func(t *testing.T) {
			row, ok := rowFor(rows, tc.class)
			if !ok {
				t.Fatalf("the real §5.16 table has no %q row", tc.class)
			}
			reg := externalOnlyRegistry()
			exec := &routerExecutor{router: realRouter(t, reg), outputs: []string{
				`{"approved":true,"findings":[]}`,
				`{"approved":true,"verdict":"v","dissent":"d","findings":[]}`,
			}}
			p, err := NewProvider(exec, reg, nil)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			// CR-C's two passes route through the same spill order over the
			// same registry, so the REAL router lands both on one family --
			// which is exactly the D2(b) post-dispatch HOLD condition, and
			// exactly why that gate exists. The HOLD is accepted here; what
			// this test is about is the requests that were dispatched before
			// it, which are recorded either way.
			_, err = p.Review(context.Background(), provider.ReviewRequest{
				Level: tc.level, Diff: "diff --git a/x.go b/x.go\n+x",
			})
			if err != nil && !errors.Is(err, ErrNoEligibleReviewerFamily) {
				t.Fatalf("Review: %v", err)
			}
			if len(exec.requests) == 0 {
				t.Fatal("no request was dispatched")
			}
			for i, req := range exec.requests {
				if req.Sensitivity != row.SensitivityDefault {
					t.Errorf("dispatch %d Sensitivity = %v, want the %q row's own SensitivityDefault %v -- "+
						"a review dispatch never goes out below its task class's default",
						i, req.Sensitivity, tc.class, row.SensitivityDefault)
				}
				if req.TaskClass != row.Class {
					t.Errorf("dispatch %d TaskClass = %q, want %q", i, req.TaskClass, row.Class)
				}
				if !tc.cheaperOK && (req.Requirements.Reasoning != row.Reasoning || req.Requirements.Context != row.CtxK*1000) {
					t.Errorf("dispatch %d Requirements = %+v, want the row's own {%s, %d}",
						i, req.Requirements, row.Reasoning, row.CtxK*1000)
				}
			}
		})
	}
}

// rowFor finds one §5.16 row by class name.
func rowFor(rows []conductor.TaskClassRow, class conductor.TaskClass) (conductor.TaskClassRow, bool) {
	for _, r := range rows {
		if r.Class == string(class) {
			return r, true
		}
	}
	return conductor.TaskClassRow{}, false
}

// TestReviewPassesCallerContextThrough is the ctx-passthrough proof standing
// on its own: the executor records whether the thread privacy the CALLER
// attached was still on the ctx when Execute ran. Swap ctx for a fresh
// context.Background() anywhere on the dispatch path and this goes red even
// before the router's own refusal does.
func TestReviewPassesCallerContextThrough(t *testing.T) {
	reg := externalOnlyRegistry().withLocalLane()
	exec := &ctxRecordingExecutor{}
	p, err := NewProvider(exec, reg, nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, err := p.Review(localOnlyThreadContext(), provider.ReviewRequest{
		Level: provider.ReviewCRLevelC, Diff: "diff --git a/x.go b/x.go\n+x",
	}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(exec.threads) != 2 {
		t.Fatalf("CR-C made %d dispatches, want 2", len(exec.threads))
	}
	for i, tp := range exec.threads {
		if tp.ThreadID != "thread-local-only-fixture" || tp.Mode != provider.SensitivityLocalOnly {
			t.Errorf("dispatch %d saw thread privacy %+v, want the caller's own local-only thread -- "+
				"the reviewer must pass ctx to Execute unchanged", i, tp)
		}
	}
}

// ctxRecordingExecutor records the thread privacy present on each Execute's
// ctx (the zero value when none was), and returns a distinct-family pair so
// CR-C's own post-dispatch gate is satisfied.
type ctxRecordingExecutor struct {
	threads []conductor.ThreadPrivacy
	n       int
}

func (e *ctxRecordingExecutor) Execute(ctx context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
	tp, _ := conductor.ThreadPrivacyFrom(ctx)
	e.threads = append(e.threads, tp)
	e.n++
	if e.n == 1 {
		return provider.ModelResponse{Output: `{"approved":true,"findings":[]}`,
			Selection: provider.Selection{Provider: "prov-external"}}, nil
	}
	return provider.ModelResponse{Output: `{"approved":true,"verdict":"v","dissent":"d","findings":[]}`,
		Selection: provider.Selection{Provider: "prov-external-2"}}, nil
}
