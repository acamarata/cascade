package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeIssues_RealCapture decodes a real issue list.
func TestDecodeIssues_RealCapture(t *testing.T) {
	issues, err := DecodeIssues(fixture(t, "issues.list.json"))
	if err != nil {
		t.Fatalf("DecodeIssues: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("decoded no issues from a populated capture")
	}
	for i, issue := range issues {
		if issue.Number <= 0 {
			t.Errorf("issue %d decoded with number %d", i, issue.Number)
		}
		if issue.State == "" {
			t.Errorf("issue %d decoded with no state", i)
		}
		if issue.User.Login == "" {
			t.Errorf("issue %d decoded with no user", i)
		}
		if issue.HTMLURL == "" {
			t.Errorf("issue %d decoded with no html_url", i)
		}
	}
}

// TestIssuesEndpointReturnsPullRequestsToo is the finding this decoder
// exists to make visible.
//
// GitHub's /issues endpoint returns pull requests as well as issues,
// distinguished only by the presence of a pull_request member. A caller
// that did not know would report every PR as an issue and count both wrong.
// The assertion is driven off the real capture rather than a belief about
// what the endpoint returns.
func TestIssuesEndpointReturnsPullRequestsToo(t *testing.T) {
	issues, err := DecodeIssues(fixture(t, "issues.list.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Cross-check against the raw JSON so this cannot pass because the
	// decoder and the assertion share a mistake.
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, "issues.list.json"), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(issues) {
		t.Fatalf("decoded %d issues from %d raw entries", len(issues), len(raw))
	}
	for i := range raw {
		_, rawHasPR := raw[i]["pull_request"]
		if issues[i].IsPullRequest() != rawHasPR {
			t.Errorf("entry %d: IsPullRequest()=%v but the raw capture %s a pull_request member",
				i, issues[i].IsPullRequest(), map[bool]string{true: "has", false: "has no"}[rawHasPR])
		}
	}
}

// TestDecodeIssueRejectsAnArray proves the single-issue decoder refuses a
// list response rather than silently yielding a zero issue.
func TestDecodeIssueRejectsAnArray(t *testing.T) {
	if _, err := DecodeIssue(fixture(t, "issues.list.json")); err == nil {
		t.Fatal("DecodeIssue accepted an array as a single issue")
	}
}

// TestIssueRequests covers each verb's method, path and body.
func TestIssueRequests(t *testing.T) {
	list, err := ListIssuesRequest("acamarata", "cascade", "open", 5)
	if err != nil {
		t.Fatal(err)
	}
	if list.Method != "GET" || !strings.HasPrefix(list.Path, "/repos/acamarata/cascade/issues?") {
		t.Fatalf("ListIssuesRequest = %+v", list)
	}
	for _, want := range []string{"state=open", "per_page=5"} {
		if !strings.Contains(list.Path, want) {
			t.Errorf("list path %q is missing %q", list.Path, want)
		}
	}

	get, err := GetIssueRequest("acamarata", "cascade", 42)
	if err != nil {
		t.Fatal(err)
	}
	if get.Path != "/repos/acamarata/cascade/issues/42" {
		t.Fatalf("GetIssueRequest path = %q", get.Path)
	}

	create, err := CreateIssueRequest("acamarata", "cascade", "a title", "a body", []string{"bug"})
	if err != nil {
		t.Fatal(err)
	}
	if create.Method != "POST" || create.Path != "/repos/acamarata/cascade/issues" {
		t.Fatalf("CreateIssueRequest = %+v", create)
	}
	var payload map[string]any
	if err := json.Unmarshal(create.Body, &payload); err != nil {
		t.Fatalf("create body is not JSON: %v", err)
	}
	if payload["title"] != "a title" || payload["body"] != "a body" {
		t.Fatalf("create body = %v", payload)
	}

	comment, err := CommentIssueRequest("acamarata", "cascade", 42, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if comment.Path != "/repos/acamarata/cascade/issues/42/comments" {
		t.Fatalf("CommentIssueRequest path = %q", comment.Path)
	}
}

// TestCloseIssueSendsStateOnly proves close PATCHes nothing but state. A
// close that also sent a title or body would overwrite whatever a
// concurrent editor changed between read and write.
func TestCloseIssueSendsStateOnly(t *testing.T) {
	req, err := CloseIssueRequest("acamarata", "cascade", 42)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "PATCH" {
		t.Fatalf("method = %q, want PATCH", req.Method)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 || payload["state"] != "closed" {
		t.Fatalf("close body = %v, want exactly {state: closed}", payload)
	}
}

// TestIssueRequestRefusals covers every guard before the API is reached.
func TestIssueRequestRefusals(t *testing.T) {
	if _, err := ListIssuesRequest("a", "b", "sideways", 0); err == nil {
		t.Error("an unrecognized issue state was accepted")
	}
	for _, state := range []string{"", "open", "closed", "all"} {
		if _, err := ListIssuesRequest("a", "b", state, 0); err != nil {
			t.Errorf("state %q was refused: %v", state, err)
		}
	}
	for _, n := range []int{0, -1} {
		if _, err := GetIssueRequest("a", "b", n); err == nil {
			t.Errorf("issue number %d was accepted", n)
		}
	}
	if _, err := CreateIssueRequest("a", "b", "   ", "body", nil); err == nil {
		t.Error("an issue with a blank title was accepted")
	}
	if _, err := CommentIssueRequest("a", "b", 1, "  "); err == nil {
		t.Error("a blank comment was accepted")
	}
}
