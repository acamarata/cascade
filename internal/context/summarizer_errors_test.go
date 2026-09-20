package context

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSummarizerErrorConstructors asserts every construction/misuse
// constructor produces a KindInvalidInput error carrying a distinguishing
// message substring. *cascade.Error.Is compares Kind only
// (pkg/cascade/errors.go), so message substrings -- not errors.Is -- are what
// actually tells these apart in a test.
func TestSummarizerErrorConstructors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil executor", errSummarizerNilExecutor(), "ModelExecutor"},
		{"nil store", errSummarizerNilStore(), "Store"},
		{"nil counter", errSummarizerNilCounter(), "TokenCounter"},
		{"nil clock", errSummarizerNilClock(), "Clock"},
		{"nil context", errSummarizerNilContext(), "ctx"},
		{"empty entity id", errSummarizerEmptyEntityID(), "entityID"},
		{"invalid granularity", errSummarizerInvalidGranularity(Granularity(9)), "granularity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if k, ok := cascade.KindOf(tc.err); !ok || k != cascade.KindInvalidInput {
				t.Errorf("%s: kind = %v (ok=%v), want invalid-input", tc.name, k, ok)
			}
			if !strings.Contains(tc.err.Error(), tc.want) {
				t.Errorf("%s: error = %q, want substring %q", tc.name, tc.err.Error(), tc.want)
			}
		})
	}
}

// TestSummarizerInvalidGranularityReportsValue asserts the out-of-range
// numeric value itself appears in the message, not a placeholder.
func TestSummarizerInvalidGranularityReportsValue(t *testing.T) {
	err := errSummarizerInvalidGranularity(Granularity(7))
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("error = %q, want it to report the invalid value 7", err.Error())
	}
}

// TestSummarizerOversizeOutputError asserts the oversize-response error is
// KindIntegrity (a verification step on the response failed) and names the
// measured size, the level and the bound.
func TestSummarizerOversizeOutputError(t *testing.T) {
	err := errSummarizerOversizeOutput(GranularityEpoch, 400, 125)
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Errorf("kind = %v (ok=%v), want integrity", k, ok)
	}
	for _, want := range []string{"400", "125", "epoch", "size"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want substring %q", err.Error(), want)
		}
	}
}

// TestSummarizerDependencyPreservesKind asserts errSummarizerDependency
// keeps an already-typed inner error's Kind, and falls back to
// KindInternal for an untyped one.
func TestSummarizerDependencyPreservesKind(t *testing.T) {
	typed := cascade.New(cascade.KindConflict, "already exists")
	wrapped := errSummarizerDependency(typed, "context: summarizer: test")
	if k, ok := cascade.KindOf(wrapped); !ok || k != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want conflict (preserved)", k, ok)
	}

	plain := errSummarizerDependency(errPlain("boom"), "context: summarizer: test")
	if k, ok := cascade.KindOf(plain); !ok || k != cascade.KindInternal {
		t.Errorf("kind = %v (ok=%v), want internal (fallback)", k, ok)
	}
}

// errPlain is a bare error type (never wrapping a *cascade.Error), used
// only to exercise the untyped-error fallback legs.
type errPlain string

func (e errPlain) Error() string { return string(e) }

// TestFailureReasonCarriesKindAndMessage asserts a failure Reason names the
// taxonomy kind AND the underlying message, so three different regeneration
// failures do not all read the same to an operator.
func TestFailureReasonCarriesKindAndMessage(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		wants []string
	}{
		{
			"a refused dispatch",
			errSummarizerDependency(cascade.New(cascade.KindPolicyDenied, "policy refused"),
				"context: summarizer: model.execute dispatch failed"),
			[]string{"policy-denied", "dispatch failed", "policy refused"},
		},
		{
			"no lane available",
			errSummarizerDependency(cascade.New(cascade.KindQuotaExhausted, "no candidate lane"),
				"context: summarizer: model.execute dispatch failed"),
			[]string{"quota-exhausted", "dispatch failed", "no candidate lane"},
		},
		{
			"a failed store write",
			errSummarizerDependency(cascade.New(cascade.KindUnavailable, "disk full"),
				"context: summarizer: writing summary to storage"),
			[]string{"unavailable", "writing summary to storage", "disk full"},
		},
		{
			"an untyped failure",
			errPlain("something broke"),
			[]string{"untyped", "something broke"},
		},
		{"no cause at all", nil, []string{"unreported"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := failureReason(tc.cause)
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("failureReason = %q, want substring %q", got, want)
				}
			}
			// A *cascade.Error already renders its own kind as its leading
			// token, so the reason must not open with the kind twice. (A
			// WRAPPED error legitimately shows its kind again further in, at
			// the inner error: that is cascade's own rendering, not this
			// function prefixing what was already there.)
			if k, ok := cascade.KindOf(tc.cause); ok {
				if doubled := k.String() + ": " + k.String() + ":"; strings.HasPrefix(got, doubled) {
					t.Errorf("failureReason = %q, want the kind named once at the front, not %q", got, doubled)
				}
			}
		})
	}
}

// TestFailureReasonsAreDistinctPerStage asserts the three stages do not
// collapse into one another's text: a test that only checked "contains
// 'failed'" would pass on all three.
func TestFailureReasonsAreDistinctPerStage(t *testing.T) {
	dispatch := failureReason(errSummarizerDependency(
		cascade.New(cascade.KindUnavailable, "model unreachable"),
		"context: summarizer: model.execute dispatch failed"))
	storage := failureReason(errSummarizerDependency(
		cascade.New(cascade.KindUnavailable, "disk full"),
		"context: summarizer: writing summary to storage"))
	size := failureReason(errSummarizerOversizeOutput(GranularityThread, 400, 250))

	if dispatch == storage || dispatch == size || storage == size {
		t.Fatalf("the three stage reasons must differ: dispatch=%q storage=%q size=%q", dispatch, storage, size)
	}
	if strings.Contains(storage, "dispatch") {
		t.Errorf("the storage reason names dispatch: %q", storage)
	}
	if strings.Contains(dispatch, "storage") {
		t.Errorf("the dispatch reason names storage: %q", dispatch)
	}
}
