// Purpose (this file): the two `cascade review` assertions D2 (T0 decision,
//
//	2026-09-21) names that review-mounted.txtar's own driver (script_test.go's
//	runScript: env/exec/stdout/stderr directives only) has no hook for --
//	"exits 0 with findings" and "an unconfigured provider refuses with the
//	refusal text" both need the plugins/review.reviewProvider seam swapped
//	deterministically around one exec call, which no txtar directive can
//	express. Both drive the SAME execCascade helper review-mounted.txtar's
//	driver uses (script_test.go), against the SAME real newRootCmd() tree,
//	so this is not a second, divergent proof of the mount -- it is the
//	provider-dependent half of the same one.
//
// Inputs: none beyond the process-wide plugins/review.reviewProvider var,
//
//	which this file's own t.Cleanup calls always restore to a refusing
//	state (never left holding a test double for a later test in this
//	binary to silently depend on).
//
// Outputs: two passing subtests proving the mounted command reaches a real
//
//	provider dispatch, and refuses honestly when none is wired.
//
// Constraints: no network (Art.7); the fake providers below are test-only
//
//	doubles, never a stand-in for cmd.go's own logic, which execCascade's
//	real newRootCmd()/root.Execute() call exercises unchanged.
//
// SPORT: cmd/cascade:review-mount-test (ADD) -- FIX P1-E25-W5-S52-T5 (D2).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// mountedFindingProvider is a fake provider.ReviewProvider returning one
// deterministic finding -- standing in for T4's real engine's own network
// call, never for cmd.go's own flag/output logic, which execCascade's real
// command construction exercises for real.
type mountedFindingProvider struct{}

func (mountedFindingProvider) Review(context.Context, provider.ReviewRequest) (provider.ReviewResponse, error) {
	return provider.ReviewResponse{
		Findings: []provider.ReviewFinding{
			{Severity: provider.ReviewSeverityMajor, File: "mounted.go", Line: 7, Message: "mounted-fixture finding"},
		},
	}, nil
}

func (mountedFindingProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}

// mountedRefusingProvider reproduces the exact contract plugins/review's
// own unexported unwiredReviewProvider guarantees (plugin_test.go's
// TestUnwiredReviewProviderIsHonest proves that type directly): every call
// refuses, honestly, naming "not wired". Reproduced here, rather than
// referenced, because unwiredReviewProvider is unexported and this test
// binary cannot observe the true pre-init() state once root.go's own
// transitive import chain (internal/plugins, via builtin_plugins.go) has
// already run SetReviewProvider(real) for this process.
type mountedRefusingProvider struct{}

func (mountedRefusingProvider) Review(context.Context, provider.ReviewRequest) (provider.ReviewResponse, error) {
	return provider.ReviewResponse{}, errors.New("cascade-review: review provider not wired (internal/plugins/review_wiring.go must call review.SetReviewProvider)")
}

func (mountedRefusingProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, errors.New("cascade-review: review provider not wired (internal/plugins/review_wiring.go must call review.SetReviewProvider)")
}

// writeMountedDiffFixture writes a minimal, real unified diff to a fresh
// temp file -- readReviewDiff (cmd.go) needs a real, readable path; the
// fake providers above ignore its content, so its exact shape does not
// matter beyond "a file --diff can read".
func writeMountedDiffFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mounted.diff")
	diff := "diff --git a/mounted.go b/mounted.go\n--- a/mounted.go\n+++ b/mounted.go\n@@ -1 +1,2 @@\n" +
		" package mounted\n+// fixture line\n"
	if err := os.WriteFile(p, []byte(diff), 0o600); err != nil {
		t.Fatalf("write diff fixture: %v", err)
	}
	return p
}

// TestReviewMountedDispatchesToTheRealProvider proves D2's two
// provider-dependent assertions against the REAL mounted command
// (execCascade -> newRootCmd() -> root.Execute(), script_test.go's own
// driver): a wired provider's findings reach stdout with exit 0, and an
// unconfigured one refuses with a non-zero exit naming "not wired".
func TestReviewMountedDispatchesToTheRealProvider(t *testing.T) {
	t.Cleanup(func() { _ = reviewplugin.SetReviewProvider(mountedRefusingProvider{}) })
	diffPath := writeMountedDiffFixture(t)

	t.Run("wired-provider-returns-findings", func(t *testing.T) {
		if err := reviewplugin.SetReviewProvider(mountedFindingProvider{}); err != nil {
			t.Fatalf("SetReviewProvider(mountedFindingProvider): %v", err)
		}
		got := execCascade([]string{"review", "--diff", diffPath, "--level", "A", "--json"})
		if got.exit != 0 {
			t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", got.exit, got.stdout, got.stderr)
		}
		if !strings.Contains(got.stdout, "mounted-fixture finding") {
			t.Fatalf("stdout missing the fake provider's finding: %q", got.stdout)
		}
	})

	t.Run("unconfigured-provider-refuses", func(t *testing.T) {
		if err := reviewplugin.SetReviewProvider(mountedRefusingProvider{}); err != nil {
			t.Fatalf("SetReviewProvider(mountedRefusingProvider): %v", err)
		}
		got := execCascade([]string{"review", "--diff", diffPath, "--level", "A"})
		if got.exit == 0 {
			t.Fatalf("exit = 0, want non-zero for an unconfigured provider\nstdout: %s", got.stdout)
		}
		if !strings.Contains(got.stderr, "not wired") {
			t.Fatalf("stderr = %q, want it to name the unwired provider", got.stderr)
		}
	})
}
