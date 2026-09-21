// Purpose (this file): the error-path and daemonless-scoping tests for
//
//	cmd.go's `review` command that cmd_test.go's own 300-line cap could not
//	also hold — same suite, same package, split purely for Art.10.3.
//
// SPORT: plugins/review:cmd_test (ADD) -- P1-E25-W5-S52-T5.
package review

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// countingReviewProvider wraps another provider.ReviewProvider, counting
// Review calls. Test-only instrumentation, not a stand-in for any
// production seam.
type countingReviewProvider struct {
	inner provider.ReviewProvider
	calls *int
}

func (c countingReviewProvider) Review(ctx context.Context, req provider.ReviewRequest) (provider.ReviewResponse, error) {
	*c.calls++
	return c.inner.Review(ctx, req)
}

func (c countingReviewProvider) Capabilities(ctx context.Context, lane string) (provider.Capabilities, error) {
	return c.inner.Capabilities(ctx, lane)
}

// TestReviewDaemonlessDispatchIsDirect proves the acceptance criterion's
// "in-process ReviewProvider" claim about cmd.go's OWN code, honestly
// scoped: runReview makes exactly one call, straight to the package-level
// reviewProvider seam (this stub, standing in for whatever
// review_wiring.go's init() installed) -- no daemon RPC hop, no second
// construction, and no branch on daemon-vs-daemonless inside this file.
// Whether THAT seam's real T4 implementation (internal/review.Provider,
// via internal/plugins/review_wiring.go) can reach a live daemon socket is
// T4's own contract, proven by that package's provider_test.go/
// router_test.go against the real router -- this repository has no
// embedded/in-process conductor.Executor anywhere (verified by grep before
// writing this test; internal/runtime/daemonless.go's "headless embedded
// runtime" covers cascade.db write arbitration only, not model dispatch;
// cmd/cascade/run_exec.go's fetchRun REFUSES outright when daemonless for
// exactly this reason), so a literal "dial a non-existent socket and still
// get findings" test would have to fabricate an execution path nothing in
// this tree provides. Recorded as a contract deviation in this ticket's
// report, not silently worked around.
func TestReviewDaemonlessDispatchIsDirect(t *testing.T) {
	calls := 0
	withStubProvider(t, &stubReviewProvider{resp: findingResponse()})
	orig := reviewProvider
	reviewProvider = countingReviewProvider{inner: orig, calls: &calls}
	t.Cleanup(func() { reviewProvider = unwiredReviewProvider{} })

	if _, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", "B"}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if calls != 1 {
		t.Fatalf("reviewProvider.Review called %d times, want exactly 1 (no retry/second-path fallback)", calls)
	}
}

// TestReviewSensitivityRefusalPropagatesKind proves the "No silent
// downgrade" contract: a KindPolicyDenied refusal from the provider seam
// (standing in for T4's real router refusal -- router_test.go proves that
// refusal against the real router; this test proves cmd.go does not
// reclassify or swallow it) reaches the process exit path with its Kind
// intact.
func TestReviewSensitivityRefusalPropagatesKind(t *testing.T) {
	refusal := cascade.New(cascade.KindPolicyDenied, "internal/conductor: local-only thread refuses an external lane")
	withStubProvider(t, &stubReviewProvider{err: refusal})
	_, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", "B"})
	if err == nil {
		t.Fatal("exit: got nil error for a provider refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Fatalf("Kind = %v (ok=%v), want KindPolicyDenied (no downgrade to KindUnavailable/KindInternal)", kind, ok)
	}
}

// TestReviewDiffAndPRMutuallyExclusive covers the flag-combination half of
// validateReviewFlags task 1 does not otherwise exercise.
func TestReviewDiffAndPRMutuallyExclusive(t *testing.T) {
	_, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--pr", "123"})
	if err == nil {
		t.Fatal("got nil error for --diff and --pr both set")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// TestReviewPRRefusedNoNetworkCall proves --pr's real fetch refusal (D6,
// 2026-09-21: plugins/review cannot reach internal/ci's GitHub client, and
// no egress class covers a PR-diff fetch either way) fires before any
// provider dispatch -- the stub records zero calls -- for a well-formed
// reference. A well-formed ref is required here specifically so this test
// exercises errPRFetchUnsupported, not parsePRRef's format refusal (see
// TestReviewPRMalformedRefIsInvalidInput for that one).
func TestReviewPRRefusedNoNetworkCall(t *testing.T) {
	calls := 0
	withStubProvider(t, &countingReviewProvider{inner: &stubReviewProvider{resp: findingResponse()}, calls: &calls})
	_, err := runCmd(t, []string{"--pr", "acamarata/cascade#1234"})
	if err == nil {
		t.Fatal("got nil error for --pr")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("Kind = %v (ok=%v), want KindUnsupported", kind, ok)
	}
	if !strings.Contains(err.Error(), "acamarata/cascade#1234") {
		t.Fatalf("error = %q, want it to name the ref it refused", err.Error())
	}
	if calls != 0 {
		t.Fatalf("reviewProvider.Review called %d times for --pr, want 0 (fetch is refused, never attempted)", calls)
	}
}

// TestReviewPRMalformedRefIsInvalidInput proves --pr's format validation
// (parsePRRef, D6) is real: a ref that is not "owner/repo#number" is
// refused as invalid input, distinct from -- and reached before --
// errPRFetchUnsupported's KindUnsupported refusal above.
func TestReviewPRMalformedRefIsInvalidInput(t *testing.T) {
	for _, bad := range []string{"123", "owner/repo", "owner#123", "/repo#123", "owner/#123", "owner/repo#abc", "owner/repo#0"} {
		_, err := runCmd(t, []string{"--pr", bad})
		if err == nil {
			t.Fatalf("--pr %q: got nil error, want a format refusal", bad)
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("--pr %q: Kind = %v (ok=%v), want KindInvalidInput", bad, kind, ok)
		}
	}
}

// TestReviewDiffFromStdin covers --diff - (stdin), the other half of task 1
// review-diff's own flag surface.
func TestReviewDiffFromStdin(t *testing.T) {
	var got provider.ReviewRequest
	withStubProvider(t, &stubReviewProvider{resp: findingResponse(), gotReq: &got})
	c := NewReviewCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(strings.NewReader(sampleDiff))
	c.SetArgs([]string{"--diff", "-", "--level", "B"})
	if err := c.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got.Diff != sampleDiff {
		t.Fatalf("Diff read from stdin = %q, want the sample diff verbatim", got.Diff)
	}
}

// TestReviewUnwiredProviderRefusesHonestly proves the default seam (no
// SetReviewProvider call -- the state before any composition root wires
// review_wiring.go's init()) reaches the CLI as a real, non-zero-exit
// refusal, never a fabricated success.
func TestReviewUnwiredProviderRefusesHonestly(t *testing.T) {
	t.Cleanup(func() { reviewProvider = unwiredReviewProvider{} })
	reviewProvider = unwiredReviewProvider{}
	_, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", "B"})
	if err == nil {
		t.Fatal("got nil error with no provider wired")
	}
	if !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("error = %q, want it to name the unwired provider", err.Error())
	}
}

// TestReviewDiffFileNotFound covers a --diff path that does not exist: a
// typed, invalid-input error, never a panic or an empty diff silently sent
// to the provider.
func TestReviewDiffFileNotFound(t *testing.T) {
	withStubProvider(t, &stubReviewProvider{resp: findingResponse()})
	_, err := runCmd(t, []string{"--diff", "/nonexistent-dir-for-cascade-review-tests/does-not-exist.diff"})
	if err == nil {
		t.Fatal("got nil error for a missing --diff file")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}
