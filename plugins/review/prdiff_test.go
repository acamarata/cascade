package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fetchedDiff carries CRLF, a tab and a missing final newline so any
// re-encoding between the fetcher and the provider would show.
var fetchedDiff = []byte("diff --git a/x.go b/x.go\r\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-\told\n+new\n\\ No newline at end of file")

type recordingPRDiffFetcher struct {
	owner, repo string
	number      int
	calls       int
	diff        []byte
	err         error
}

func (f *recordingPRDiffFetcher) FetchPRDiff(_ context.Context, owner, repo string, number int) ([]byte, error) {
	f.owner, f.repo, f.number = owner, repo, number
	f.calls++
	return f.diff, f.err
}

func isolatePRDiffTest(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	old := prDiffFetcher
	t.Cleanup(func() { prDiffFetcher = old })
}

func installPRDiffFetcher(t *testing.T, f PRDiffFetcher) {
	t.Helper()
	isolatePRDiffTest(t)
	if err := SetPRDiffFetcher(f); err != nil {
		t.Fatal(err)
	}
}

// TestReviewPRUsesFetcher: --pr hands the parsed reference to the fetcher
// once, and the fetched bytes reach reviewProvider.Review exactly as the
// same bytes given to --diff do.
func TestReviewPRUsesFetcher(t *testing.T) {
	f := &recordingPRDiffFetcher{diff: fetchedDiff}
	installPRDiffFetcher(t, f)
	var got provider.ReviewRequest
	withStubProvider(t, &stubReviewProvider{gotReq: &got})
	if _, err := runCmd(t, []string{"--pr", "octocat/Hello-World#42", "--level", "A"}); err != nil {
		t.Fatal(err)
	}
	if f.owner != "octocat" || f.repo != "Hello-World" || f.number != 42 || f.calls != 1 {
		t.Fatalf("fetch = %+v", f)
	}
	pr := got
	cmd := NewReviewCommand()
	cmd.SetIn(strings.NewReader(string(fetchedDiff)))
	if err := runReview(cmd, &reviewFlags{diff: "-", level: "A"}); err != nil {
		t.Fatal(err)
	}
	if pr.Diff != string(fetchedDiff) || pr.Diff != got.Diff || pr.Level != got.Level {
		t.Fatalf("--pr request %+v differs from --diff request %+v", pr, got)
	}
}

// TestReviewPRDefaultFetcherRefuses: this package's default fetcher (no
// host wiring linked) refuses KindUnavailable with the contract message,
// and the review provider is never called.
func TestReviewPRDefaultFetcherRefuses(t *testing.T) {
	isolatePRDiffTest(t)
	if _, ok := prDiffFetcher.(unwiredPRDiffFetcher); !ok {
		t.Fatalf("default fetcher is %T, want unwiredPRDiffFetcher", prDiffFetcher)
	}
	calls := 0
	withStubProvider(t, &countingReviewProvider{inner: &stubReviewProvider{resp: findingResponse()}, calls: &calls})
	_, err := runCmd(t, []string{"--pr", "octocat/Hello-World#1"})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable ||
		!strings.Contains(err.Error(), "PR diff fetch is not wired in this build") || calls != 0 {
		t.Fatalf("err %v (kind %v), provider calls %d", err, kind, calls)
	}
}

// TestReviewPRAndDiffStillMutuallyExclusive: both flags refuse before any
// fetch.
func TestReviewPRAndDiffStillMutuallyExclusive(t *testing.T) {
	f := &recordingPRDiffFetcher{diff: fetchedDiff}
	installPRDiffFetcher(t, f)
	_, err := runCmd(t, []string{"--pr", "octocat/Hello-World#1", "--diff", "-"})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput || f.calls != 0 {
		t.Fatalf("err %v, fetch calls %d", err, f.calls)
	}
}

// TestSetPRDiffFetcherRefusesNil: nil and a typed nil are KindInvalidInput
// and the installed fetcher is kept; a real fetcher is accepted.
func TestSetPRDiffFetcherRefusesNil(t *testing.T) {
	isolatePRDiffTest(t)
	kept := &recordingPRDiffFetcher{}
	if err := SetPRDiffFetcher(kept); err != nil {
		t.Fatal(err)
	}
	var typedNil *recordingPRDiffFetcher
	for _, f := range []PRDiffFetcher{nil, typedNil} {
		err := SetPRDiffFetcher(f)
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("SetPRDiffFetcher(%#v) = %v, want KindInvalidInput", f, err)
		}
		if prDiffFetcher != PRDiffFetcher(kept) {
			t.Fatalf("refusal replaced the fetcher with %T", prDiffFetcher)
		}
	}
}

// TestReviewPRSetsUntrustedProvenance: --pr marks the request
// untrusted-source; --diff leaves Provenance zero.
func TestReviewPRSetsUntrustedProvenance(t *testing.T) {
	installPRDiffFetcher(t, &recordingPRDiffFetcher{diff: fetchedDiff})
	var got provider.ReviewRequest
	withStubProvider(t, &stubReviewProvider{gotReq: &got})
	if _, err := runCmd(t, []string{"--pr", "octocat/Hello-World#1"}); err != nil {
		t.Fatal(err)
	}
	if got.Provenance != provider.ProvenanceUntrustedSource || got.Diff != string(fetchedDiff) {
		t.Fatalf("--pr request = %+v", got)
	}
	cmd := NewReviewCommand()
	cmd.SetIn(strings.NewReader(string(fetchedDiff)))
	if err := runReview(cmd, &reviewFlags{diff: "-", level: "B"}); err != nil || got.Provenance != "" {
		t.Fatalf("--diff provenance = %q, err %v; want zero", got.Provenance, err)
	}
}

// TestReviewPRFetchErrorStopsReview: a fetch refusal is returned unchanged
// and the provider is never called.
func TestReviewPRFetchErrorStopsReview(t *testing.T) {
	refusal := cascade.New(cascade.KindPolicyDenied, "test: refused")
	installPRDiffFetcher(t, &recordingPRDiffFetcher{diff: fetchedDiff, err: refusal})
	calls := 0
	withStubProvider(t, &countingReviewProvider{inner: &stubReviewProvider{resp: findingResponse()}, calls: &calls})
	if _, err := runCmd(t, []string{"--pr", "octocat/Hello-World#1"}); !errors.Is(err, refusal) || calls != 0 {
		t.Fatalf("err %v, provider calls %d", err, calls)
	}
}
