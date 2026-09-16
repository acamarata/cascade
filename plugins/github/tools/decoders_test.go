package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// Purpose (this file): every entry in the decoder table, exercised through
//   Client.Call over the RECORDED GitHub captures rather than over bodies
//   this package invented for itself (Art.2, ../testdata/README.md).
//   TestEveryBuiltRequestHasADecoder proves the two tables cover the same
//   tool names; it does not run a single decoder. This file runs them.
// Constraints: no net/http; the transport is the injected Doer seam.
// SPORT: plugins/github/tools tests (ADD) — P1-E25-W5-S51-T1.

// firstOf returns element 0 of a captured LIST fixture as its own object.
//
// Slicing a real array is how a single-object capture is obtained without
// authoring one: the bytes are still exactly what the API returned for that
// element. GitHub's list and single-item endpoints do differ (a listed pull
// request omits `merged`, which is why PullRequest carries MergedAt too),
// so this stands in for shape, never for field-level parity.
func firstOf(t *testing.T, name string) []byte {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(fixture(t, name), &items); err != nil {
		t.Fatalf("%s is not a JSON array: %v", name, err)
	}
	if len(items) == 0 {
		t.Fatalf("%s is empty, so it exercises no decoder", name)
	}
	return items[0]
}

// decoderCase is one tool driven over one captured body.
type decoderCase struct {
	tool string
	body []byte
	want func(t *testing.T, got any)
}

// decoderCases pairs every decodable tool with a recorded capture. Kept
// separate from the assertion loop so the table can grow with the tool
// groups without the test outgrowing the 50-line cap.
func decoderCases(t *testing.T) []decoderCase {
	t.Helper()
	issue := firstOf(t, "issues.list.json")
	pr := firstOf(t, "prs.list.json")

	return []decoderCase{
		{"repos.list", fixture(t, "repos.list.json"), func(t *testing.T, got any) {
			repos, ok := got.([]Repo)
			if !ok || len(repos) == 0 {
				t.Fatalf("got %T, want a non-empty []Repo", got)
			}
		}},
		{"repos.get", fixture(t, "repos.get.json"), func(t *testing.T, got any) {
			repo, ok := got.(Repo)
			if !ok || repo.FullName == "" {
				t.Fatalf("got %#v, want a named Repo", got)
			}
		}},
		{"repos.clone_url", fixture(t, "repos.get.json"), func(t *testing.T, got any) {
			url, ok := got.(string)
			if !ok || url == "" {
				t.Fatalf("got %#v, want the repository's clone URL", got)
			}
		}},
		{"issues.list", fixture(t, "issues.list.json"), func(t *testing.T, got any) {
			issues, ok := got.([]Issue)
			if !ok || len(issues) == 0 {
				t.Fatalf("got %T, want a non-empty []Issue", got)
			}
		}},
		{"issues.get", issue, wantIssue},
		{"issues.create", issue, wantIssue},
		{"issues.close", issue, wantIssue},
		{"prs.list", fixture(t, "prs.list.json"), func(t *testing.T, got any) {
			prs, ok := got.([]PullRequest)
			if !ok || len(prs) == 0 {
				t.Fatalf("got %T, want a non-empty []PullRequest", got)
			}
		}},
		{"prs.get", pr, wantPR},
		{"prs.create", pr, wantPR},
		{"prs.review_request", pr, wantPR},
	}
}

// TestEveryDecoderRunsOverARealCapture drives each tool through the whole
// Call path. A decoder that panics or refuses real API bytes would fail
// only AFTER the work was done at GitHub — the worst place to discover it
// for a create, a close or a merge.
func TestEveryDecoderRunsOverARealCapture(t *testing.T) {
	for _, tc := range decoderCases(t) {
		t.Run(tc.tool, func(t *testing.T) {
			client := Client{Doer: &recordingDoer{body: tc.body}}
			got, err := client.Call(context.Background(), tc.tool, ok())
			if err != nil {
				t.Fatalf("Call(%s) over a real capture: %v", tc.tool, err)
			}
			tc.want(t, got)
		})
	}
}

// wantIssue asserts a decoded issue carries the fields a caller reads.
func wantIssue(t *testing.T, got any) {
	t.Helper()
	issue, ok := got.(Issue)
	if !ok {
		t.Fatalf("got %T, want an Issue", got)
	}
	if issue.Number == 0 || issue.State == "" {
		t.Fatalf("the decoded issue lost its number or state: %+v", issue)
	}
}

// wantPR asserts a decoded pull request carries its number and branches.
func wantPR(t *testing.T, got any) {
	t.Helper()
	pr, ok := got.(PullRequest)
	if !ok {
		t.Fatalf("got %T, want a PullRequest", got)
	}
	if pr.Number == 0 || pr.Base.Ref == "" {
		t.Fatalf("the decoded pull request lost its number or base: %+v", pr)
	}
}

// TestTwoDecodersHaveNoCaptureYet records what this file does NOT cover, so
// the gap is a stated finding rather than a silent one.
//
// `issues.comment` (IssueComment) and `prs.merge` (MergeResult) have no
// recorded fixture: both responses come from endpoints that WRITE, so
// capturing one means actually commenting on or merging a real pull
// request. Authoring a body by hand instead would test this package against
// a dialect it invented — precisely what ../testdata/README.md forbids.
// Renewing the corpus should capture both against a scratch repository.
func TestTwoDecodersHaveNoCaptureYet(t *testing.T) {
	for _, tool := range []string{"issues.comment", "prs.merge"} {
		if _, ok := decoders[tool]; !ok {
			t.Errorf("%q is recorded here as awaiting a capture but is no longer a tool", tool)
		}
	}
}

// TestACloneURLIsRefusedWhenTheRepositoryHasNone proves the projection does
// not hand back an empty string a caller would pass to `git clone`.
func TestACloneURLIsRefusedWhenTheRepositoryHasNone(t *testing.T) {
	client := Client{Doer: &recordingDoer{body: []byte(`{"full_name":"acamarata/cascade"}`)}}
	if _, err := client.Call(context.Background(), "repos.clone_url", ok()); err == nil {
		t.Fatal("a repository with no clone_url produced a clone URL")
	}
}

// TestADecodeFailureIsReportedAfterTheCall covers the other end of the
// clone_url projection: a body that is not a repository at all.
func TestADecodeFailureIsReportedAfterTheCall(t *testing.T) {
	client := Client{Doer: &recordingDoer{body: []byte(`["not","a","repository"]`)}}
	if _, err := client.Call(context.Background(), "repos.clone_url", ok()); err == nil {
		t.Fatal("an undecodable body produced a clone URL")
	}
}
