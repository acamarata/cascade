package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodePullRequests_RealCapture decodes a real pull-request list.
func TestDecodePullRequests_RealCapture(t *testing.T) {
	prs, err := DecodePullRequests(fixture(t, "prs.list.json"))
	if err != nil {
		t.Fatalf("DecodePullRequests: %v", err)
	}
	if len(prs) == 0 {
		t.Fatal("decoded no pull requests from a populated capture")
	}
	for i, pr := range prs {
		if pr.Number <= 0 {
			t.Errorf("pr %d decoded with number %d", i, pr.Number)
		}
		if pr.Head.Ref == "" || pr.Base.Ref == "" {
			t.Errorf("pr %d decoded without both refs: head=%q base=%q", i, pr.Head.Ref, pr.Base.Ref)
		}
		if pr.Head.SHA == "" {
			t.Errorf("pr %d decoded without a head sha", i)
		}
		if pr.User.Login == "" {
			t.Errorf("pr %d decoded without a user", i)
		}
	}
}

// TestListedPullRequestsCarryNoMergedBoolean is the finding IsMerged exists
// for, asserted against the real capture rather than against a belief.
//
// GitHub's single-PR response carries a boolean `merged`. The LIST response
// does not — it carries only `merged_at`. Code checking `Merged` alone
// therefore reports every merged PR in a list as unmerged. This test fails
// if the API ever starts sending the boolean in lists, which is the right
// outcome: that would be a shape change worth knowing about.
func TestListedPullRequestsCarryNoMergedBoolean(t *testing.T) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, "prs.list.json"), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("the pull-request capture is empty")
	}
	for i := range raw {
		if _, present := raw[i]["merged"]; present {
			t.Fatalf("entry %d carries a `merged` member; the list shape changed and IsMerged's "+
				"merged_at fallback should be revisited", i)
		}
		if _, present := raw[i]["merged_at"]; !present {
			t.Fatalf("entry %d carries neither `merged` nor `merged_at`; IsMerged has nothing to read", i)
		}
	}
}

// TestIsMergedReadsBothFields covers both halves of the rule.
func TestIsMergedReadsBothFields(t *testing.T) {
	if (PullRequest{}).IsMerged() {
		t.Error("an open pull request reported itself merged")
	}
	if !(PullRequest{Merged: true}).IsMerged() {
		t.Error("a PR with merged=true reported itself unmerged")
	}
	if !(PullRequest{MergedAt: "2026-09-14T00:00:00Z"}).IsMerged() {
		t.Error("a listed PR with merged_at reported itself unmerged; this is the list-shape case")
	}
}

// TestPRRequests covers each verb's method, path and body.
func TestPRRequests(t *testing.T) {
	list, err := ListPRsRequest("acamarata", "cascade", "all", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(list.Path, "/repos/acamarata/cascade/pulls?") {
		t.Fatalf("ListPRsRequest path = %q", list.Path)
	}

	get, err := GetPRRequest("acamarata", "cascade", 7)
	if err != nil {
		t.Fatal(err)
	}
	if get.Path != "/repos/acamarata/cascade/pulls/7" {
		t.Fatalf("GetPRRequest path = %q", get.Path)
	}

	create, err := CreatePRRequest("acamarata", "cascade", "t", "feature", "main", "b", true)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(create.Body, &payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"title": "t", "head": "feature", "base": "main", "draft": true} {
		if payload[key] != want {
			t.Errorf("create body[%q] = %v, want %v", key, payload[key], want)
		}
	}

	merge, err := MergePRRequest("acamarata", "cascade", 7, "squash")
	if err != nil {
		t.Fatal(err)
	}
	if merge.Method != "PUT" || merge.Path != "/repos/acamarata/cascade/pulls/7/merge" {
		t.Fatalf("MergePRRequest = %+v", merge)
	}

	review, err := ReviewRequestRequest("acamarata", "cascade", 7, []string{"someone"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if review.Path != "/repos/acamarata/cascade/pulls/7/requested_reviewers" {
		t.Fatalf("ReviewRequestRequest path = %q", review.Path)
	}
}

// TestCreatePRRefusesASelfMerge proves the guard against opening a PR whose
// head and base are the same branch — GitHub rejects it, but only after the
// call, and the local message says why.
func TestCreatePRRefusesASelfMerge(t *testing.T) {
	_, err := CreatePRRequest("a", "b", "t", "main", "main", "", false)
	if err == nil {
		t.Fatal("a pull request from main into main was accepted")
	}
	if !strings.Contains(err.Error(), "into itself") {
		t.Errorf("error = %v, want it to explain the refusal", err)
	}
}

// TestPRRequestRefusals covers the remaining guards. The merge-method one
// matters most: a merge is not a call worth making speculatively to learn
// the strategy name was wrong.
func TestPRRequestRefusals(t *testing.T) {
	for _, field := range []string{"title", "head", "base"} {
		args := map[string]string{"title": "t", "head": "h", "base": "b"}
		args[field] = "  "
		if _, err := CreatePRRequest("a", "b", args["title"], args["head"], args["base"], "", false); err == nil {
			t.Errorf("a pull request with a blank %s was accepted", field)
		}
	}
	for _, method := range []string{"", "fast-forward", "MERGE", "squash-merge"} {
		if _, err := MergePRRequest("a", "b", 1, method); err == nil {
			t.Errorf("merge method %q was accepted", method)
		}
	}
	for _, method := range MergeMethods {
		if _, err := MergePRRequest("a", "b", 1, method); err != nil {
			t.Errorf("merge method %q was refused: %v", method, err)
		}
	}
	if _, err := ReviewRequestRequest("a", "b", 1, nil, nil); err == nil {
		t.Error("a review request naming nobody was accepted; it would succeed and change nothing")
	}
}

// TestBuildRequestRoutesEveryManifestTool proves the dispatch table covers
// every tool the manifest advertises, and refuses anything else by name.
func TestBuildRequestRoutesEveryManifestTool(t *testing.T) {
	args := Args{
		Owner: "acamarata", Repo: "cascade", Number: 1, Title: "t",
		Head: "feature", Base: "main", Body: "b", MergeMethod: "squash",
		Reviewers: []string{"someone"},
	}
	for _, tool := range []string{
		"repos.list", "repos.get", "repos.clone_url",
		"issues.list", "issues.get", "issues.create", "issues.comment", "issues.close",
		"prs.list", "prs.get", "prs.create", "prs.merge", "prs.review_request",
	} {
		if _, err := BuildRequest(tool, args); err != nil {
			t.Errorf("BuildRequest(%q): %v", tool, err)
		}
	}
	if _, err := BuildRequest("repos.delete", args); err == nil {
		t.Error("BuildRequest accepted a tool the manifest does not declare")
	}
}
