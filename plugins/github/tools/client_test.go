package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): Client.Call's contract — build before egress,
//   classify before decode, and a decoder for every tool that can be built.
// Constraints: no net/http here; the transport is the injected Doer seam.
// SPORT: plugins/github/tools tests (ADD) — P1-E25-W5-S51-T1.

// recordingDoer answers from a canned response and records what it was
// asked to send.
type recordingDoer struct {
	status int
	body   []byte
	err    error

	calls []Request
	token string
}

func (d *recordingDoer) Do(_ context.Context, req Request, token string) (int, []byte, error) {
	d.calls = append(d.calls, req)
	d.token = token
	if d.err != nil {
		return 0, nil, d.err
	}
	status := d.status
	if status == 0 {
		status = 200
	}
	return status, d.body, nil
}

// TestEveryBuiltRequestHasADecoder is the parity assertion between the two
// tables. A tool BuildRequest can build but decodeFor cannot decode would
// reach GitHub, do the work, and then fail on the way back — the worst
// possible place for a missing case, because a create or a merge has
// already happened by then.
func TestEveryBuiltRequestHasADecoder(t *testing.T) {
	args := Args{Owner: "a", Repo: "b", Number: 1, Title: "t", Body: "b", Head: "h", Base: "m",
		MergeMethod: "squash", Reviewers: []string{"someone"}}

	for tool := range decoders {
		if _, err := BuildRequest(tool, args); err != nil {
			t.Errorf("%q has a decoder but cannot be built: %v", tool, err)
		}
	}

	// And the other direction: every tool the manifest-facing builder
	// accepts must decode. The list is the builder's own switch, kept here
	// so a tool added to one table and not the other fails loudly.
	for _, tool := range []string{
		"repos.list", "repos.get", "repos.clone_url",
		"issues.list", "issues.get", "issues.create", "issues.comment", "issues.close",
		"prs.list", "prs.get", "prs.create", "prs.merge", "prs.review_request",
	} {
		if _, ok := decoders[tool]; !ok {
			t.Errorf("%q can be built but has no response decoder", tool)
		}
	}
}

// TestCallDecodesARealCapture runs the whole path against the recorded
// GitHub response, so the decode this plugin performs in production is the
// one exercised here.
func TestCallDecodesARealCapture(t *testing.T) {
	doer := &recordingDoer{body: fixture(t, "repos.get.json")}
	client := Client{Doer: doer, Token: "gho_test"}

	result, err := client.Call(context.Background(), "repos.get", Args{Owner: "acamarata", Repo: "cascade"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	repo, ok := result.(Repo)
	if !ok {
		t.Fatalf("result = %T, want a Repo", result)
	}
	if repo.Name == "" {
		t.Error("the decoded repository has no name")
	}
	if doer.token != "gho_test" {
		t.Errorf("the call carried token %q", doer.token)
	}
	if got := doer.calls[0].URL(); got != APIBase+"/repos/acamarata/cascade" {
		t.Errorf("url = %q", got)
	}
}

// TestCloneURLToolProjectsTheRepository proves clone_url performs a plain
// repository fetch and returns just the string.
func TestCloneURLToolProjectsTheRepository(t *testing.T) {
	doer := &recordingDoer{body: fixture(t, "repos.get.json")}
	result, err := Client{Doer: doer}.Call(context.Background(), "repos.clone_url",
		Args{Owner: "acamarata", Repo: "cascade"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	url, ok := result.(string)
	if !ok {
		t.Fatalf("result = %T, want a string", result)
	}
	if url == "" {
		t.Fatal("clone_url returned an empty string")
	}
}

// TestCallValidatesBeforeAnyEgress is the ordering assertion. A malformed
// argument must cost nothing: no request may leave the process.
func TestCallValidatesBeforeAnyEgress(t *testing.T) {
	doer := &recordingDoer{}
	_, err := Client{Doer: doer}.Call(context.Background(), "prs.merge",
		Args{Owner: "a", Repo: "b", Number: 1, MergeMethod: "fast-forward"})
	if err == nil {
		t.Fatal("an unrecognized merge method was accepted")
	}
	if len(doer.calls) != 0 {
		t.Fatalf("a request went out despite invalid arguments: %+v", doer.calls)
	}
}

// TestCallClassifiesBeforeDecoding proves an error status becomes a typed
// error rather than being decoded as if it were the expected shape. GitHub's
// error envelope is valid JSON, so a decoder pointed at it succeeds and
// yields a zero value — a silently empty result instead of a refusal.
func TestCallClassifiesBeforeDecoding(t *testing.T) {
	doer := &recordingDoer{status: 404, body: fixture(t, "error.404.json")}
	_, err := Client{Doer: doer}.Call(context.Background(), "repos.get", Args{Owner: "a", Repo: "b"})
	if err == nil {
		t.Fatal("a 404 decoded as a repository")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

// TestCallSeparatesTransportFailureFromRefusal proves the two are different
// kinds: one is worth retrying, the other is not.
func TestCallSeparatesTransportFailureFromRefusal(t *testing.T) {
	doer := &recordingDoer{err: errors.New("dial tcp: refused")}
	_, err := Client{Doer: doer}.Call(context.Background(), "repos.get", Args{Owner: "a", Repo: "b"})
	if err == nil {
		t.Fatal("a transport failure was reported as success")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestCallRefusesWithoutATransport proves a misconstructed client fails as
// a typed error rather than a nil-pointer panic inside a tool call.
func TestCallRefusesWithoutATransport(t *testing.T) {
	if _, err := (Client{}).Call(context.Background(), "repos.get", Args{Owner: "a", Repo: "b"}); err == nil {
		t.Fatal("a client with no transport performed a call")
	}
}

// TestCallRefusesAnUnknownTool proves an undeclared tool is refused by name
// before anything is sent.
func TestCallRefusesAnUnknownTool(t *testing.T) {
	doer := &recordingDoer{}
	_, err := Client{Doer: doer}.Call(context.Background(), "repos.delete", Args{Owner: "a", Repo: "b"})
	if err == nil {
		t.Fatal("an undeclared tool was accepted")
	}
	if len(doer.calls) != 0 {
		t.Error("a request went out for an undeclared tool")
	}
}

// TestDecodeForRejectsAToolItDoesNotKnow covers the decoder table's own
// refusal, which BuildRequest's guard would normally shadow.
func TestDecodeForRejectsAToolItDoesNotKnow(t *testing.T) {
	if _, err := decodeFor("nope", []byte(`{}`)); err == nil {
		t.Fatal("decodeFor accepted a tool with no decoder")
	}
}

// TestListDecodersReturnSlices pins that a list tool decodes to a slice
// rather than to a single object — the mistake would show as a decode
// error only when the list happened to be empty.
func TestListDecodersReturnSlices(t *testing.T) {
	doer := &recordingDoer{body: fixture(t, "issues.list.json")}
	result, err := Client{Doer: doer}.Call(context.Background(), "issues.list", Args{Owner: "a", Repo: "b"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	issues, ok := result.([]Issue)
	if !ok {
		t.Fatalf("result = %T, want []Issue", result)
	}
	if len(issues) == 0 {
		t.Fatal("the capture decoded to no issues")
	}
}

// TestAPIHeadersPinTheRequestPolicy asserts the headers every call carries.
func TestAPIHeadersPinTheRequestPolicy(t *testing.T) {
	bodiless := APIHeaders(Request{Method: "GET", Path: "/x"}, "gho_x")
	for name, want := range map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           UserAgent,
		"Authorization":        "Bearer gho_x",
	} {
		if bodiless[name] != want {
			t.Errorf("header %q = %q, want %q", name, bodiless[name], want)
		}
	}
	if _, present := bodiless["Content-Type"]; present {
		t.Error("a bodiless request declared a Content-Type")
	}

	withBody := APIHeaders(Request{Method: "POST", Path: "/x", Body: []byte(`{}`)}, "  ")
	if withBody["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q", withBody["Content-Type"])
	}
	if _, present := withBody["Authorization"]; present {
		t.Error("a blank token produced an Authorization header; GitHub answers that 401 " +
			"instead of serving the call unauthenticated")
	}
}

// TestDecodeIssueCommentAndMergeResult covers the two response shapes the
// write tools return.
func TestDecodeIssueCommentAndMergeResult(t *testing.T) {
	comment, err := DecodeIssueComment([]byte(`{"id":9,"body":"hi","html_url":"https://github.com/a/b/issues/1#c9"}`))
	if err != nil {
		t.Fatalf("DecodeIssueComment: %v", err)
	}
	if comment.ID != 9 || comment.HTMLURL == "" {
		t.Errorf("decoded comment = %+v", comment)
	}
	if _, err := DecodeIssueComment([]byte(`[]`)); err == nil {
		t.Error("an array decoded as a single comment")
	}

	merged, err := DecodeMergeResult([]byte(`{"sha":"abc","merged":true,"message":"Pull Request successfully merged"}`))
	if err != nil {
		t.Fatalf("DecodeMergeResult: %v", err)
	}
	if !merged.Merged || merged.SHA != "abc" {
		t.Errorf("decoded merge = %+v", merged)
	}
	if _, err := DecodeMergeResult([]byte(`nope`)); err == nil {
		t.Error("a non-JSON body decoded as a merge result")
	}
}
