package ci

// Tests for the GitHubReader reads on *Client (P1-CI-03), replaying
// responses captured from the real API (testdata/gha_reader/README.md).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

const (
	fixtureSHA = "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"
	missingSHA = "0000000000000000000000000000000000000001"
	ghBase     = "https://api.github.com/repos/octocat/Hello-World"
)

func ghFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gha_reader", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestGitHubReaderUsesCIPollGate: both reads are refused before any byte
// leaves the process when the ci-poll class is disabled or the repository
// routes local, and go out through the doer when the gate allows them.
func TestGitHubReaderUsesCIPollGate(t *testing.T) {
	disabled := egress.NewRegistry()
	disabled.MustRegister(egress.EgressClassCIPoll, egress.InterceptConfig{Enabled: false, Owner: "test"})
	for name, c := range map[string]*Client{
		"disabled class": NewClient(&fakeDoer{}, buildEngine(t, disabled), "", 1, allowActionsRoutes),
		"routes local":   NewClient(&fakeDoer{}, testEngine(t), "", 1, localOnlyRoutes),
		"no policy":      NewClient(&fakeDoer{}, testEngine(t), "", 1, nil),
	} {
		private, verr := c.RepoVisibility(context.Background(), "octocat", "Hello-World")
		exists, cerr := c.CommitExists(context.Background(), "octocat", "Hello-World", fixtureSHA)
		if verr == nil || !private || cerr == nil || exists || len(c.doer.(*fakeDoer).calls) != 0 {
			t.Fatalf("%s: visibility (%v, %v), commit (%v, %v), calls %d; want refusals and no request",
				name, private, verr, exists, cerr, len(c.doer.(*fakeDoer).calls))
		}
	}
	doer := &fakeDoer{responses: map[string]HTTPResponse{ghBase: {Status: 200, Body: ghFixture(t, "repo_public.json")}}}
	c := NewClient(doer, testEngine(t), "", 1, allowActionsRoutes)
	private, err := c.RepoVisibility(context.Background(), "octocat", "Hello-World")
	if err != nil || private || len(doer.calls) != 1 || doer.calls[0].URL != ghBase || doer.calls[0].Method != "GET" {
		t.Fatalf("allowed: (%v, %v), calls %+v; want public from one GET", private, err, doer.calls)
	}
}

// TestGitHubReaderCommitExists replays the captured commit responses:
// 200 naming the sha is true, 404 and 422 are false, anything else, a 200
// for another sha and a short sha are errors.
func TestGitHubReaderCommitExists(t *testing.T) {
	commit := ghFixture(t, "commit_200.json")
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		ghBase + "/commits/" + fixtureSHA:              {Status: 200, Body: commit},
		ghBase + "/commits/" + missingSHA:              {Status: 422, Body: ghFixture(t, "commit_missing.json")},
		ghBase + "/commits/" + strings.Repeat("c", 40): {Status: 404, Body: ghFixture(t, "commit_repo_404.json")},
		ghBase + "/commits/" + strings.Repeat("d", 40): {Status: 500},
		ghBase + "/commits/" + strings.Repeat("e", 40): {Status: 200, Body: commit},
	}}
	c := NewClient(doer, testEngine(t), "", 1, allowActionsRoutes)
	cases := []struct {
		sha     string
		want    bool
		wantErr bool
	}{
		{fixtureSHA, true, false}, {missingSHA, false, false}, {strings.Repeat("c", 40), false, false},
		{strings.Repeat("d", 40), false, true}, {strings.Repeat("e", 40), false, true}, {"7fd1a60", false, true},
	}
	for _, tc := range cases {
		got, err := c.CommitExists(context.Background(), "octocat", "Hello-World", tc.sha)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Fatalf("CommitExists(%s) = %v, %v; want %v, err %v", tc.sha, got, err, tc.want, tc.wantErr)
		}
	}
}

// TestGitHubReaderVisibilityFailsClosed: anything but an explicit public
// answer is private.
func TestGitHubReaderVisibilityFailsClosed(t *testing.T) {
	public := string(ghFixture(t, "repo_public.json"))
	if !strings.Contains(public, `"private":false`) || !strings.Contains(public, `"visibility":"public"`) {
		t.Fatal("fixture no longer carries the fields this test edits")
	}
	for name, resp := range map[string]HTTPResponse{
		"private true":   {Status: 200, Body: []byte(strings.Replace(public, `"private":false`, `"private":true`, 1))},
		"internal":       {Status: 200, Body: []byte(strings.Replace(public, `"visibility":"public"`, `"visibility":"internal"`, 1))},
		"no private key": {Status: 200, Body: []byte(`{"visibility":"public"}`)},
		"not found":      {Status: 404, Body: []byte(`{"message":"Not Found"}`)},
	} {
		c := NewClient(&fakeDoer{responses: map[string]HTTPResponse{ghBase: resp}}, testEngine(t), "", 1, allowActionsRoutes)
		if private, _ := c.RepoVisibility(context.Background(), "octocat", "Hello-World"); !private {
			t.Fatalf("%s: read as public", name)
		}
	}
}

func TestParseGitHubOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"https://github.com/acme/widget.git\n": "acme/widget", "https://github.com/acme/widget": "acme/widget",
		"git@github.com:acme/widget.git": "acme/widget", "https://gitlab.com/acme/widget.git": "",
		"https://github.com.evil.example/acme/widget": "", "git@github.com:acme/..": "", "file:///tmp/repo": "",
	} {
		o, r, ok := parseGitHubOrigin(origin)
		if got := o + "/" + r; (ok && got != want) || (!ok && want != "") {
			t.Fatalf("parseGitHubOrigin(%q) = %q, %v; want %q", origin, got, ok, want)
		}
	}
}
