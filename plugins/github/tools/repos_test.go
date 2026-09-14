package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fixture reads one captured GitHub response. Provenance for every file is
// recorded in ../testdata/README.md; these are real API bytes with
// third-party personal data redacted and nothing else changed.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read the captured fixture %s: %v", name, err)
	}
	return raw
}

// TestDecodeRepo_RealCapture decodes a real repository response. It asserts
// on values the capture actually carries, so a decoder that silently
// stopped reading a field fails here rather than returning a zero value
// that looks plausible.
func TestDecodeRepo_RealCapture(t *testing.T) {
	repo, err := DecodeRepo(fixture(t, "repos.get.json"))
	if err != nil {
		t.Fatalf("DecodeRepo: %v", err)
	}
	if repo.Name != "cascade" {
		t.Errorf("Name = %q, want %q", repo.Name, "cascade")
	}
	if repo.FullName != "acamarata/cascade" {
		t.Errorf("FullName = %q, want %q", repo.FullName, "acamarata/cascade")
	}
	if repo.Owner.Login != "acamarata" {
		t.Errorf("Owner.Login = %q, want %q", repo.Owner.Login, "acamarata")
	}
	if repo.Private {
		t.Error("Private = true for a public repository")
	}
	if repo.DefaultBranch == "" {
		t.Error("DefaultBranch is empty")
	}
	if !strings.HasSuffix(repo.CloneURL, ".git") {
		t.Errorf("CloneURL = %q, want a git URL", repo.CloneURL)
	}
	if repo.ID == 0 {
		t.Error("ID is zero; the numeric id did not decode")
	}
}

// TestDecodeRepos_RealCapture decodes the list response, which is a
// different shape (a top-level array) from the single-repo response.
func TestDecodeRepos_RealCapture(t *testing.T) {
	repos, err := DecodeRepos(fixture(t, "repos.list.json"))
	if err != nil {
		t.Fatalf("DecodeRepos: %v", err)
	}
	if len(repos) == 0 {
		t.Fatal("decoded no repositories from a populated capture")
	}
	for i, r := range repos {
		if r.FullName == "" || r.Owner.Login == "" {
			t.Errorf("repo %d decoded without a full name or owner: %+v", i, r)
		}
	}
}

// TestDecodeRepoRejectsNonJSON proves a malformed body is a typed integrity
// error rather than an empty struct a caller would treat as a repository
// that exists but has no fields.
func TestDecodeRepoRejectsNonJSON(t *testing.T) {
	_, err := DecodeRepo([]byte("<html>502 Bad Gateway</html>"))
	if err == nil {
		t.Fatal("DecodeRepo accepted an HTML error page as a repository")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindIntegrity)
	}
}

// TestClassify_RealNotFoundBody uses the captured 404 body, so the message
// an operator sees is built from what GitHub really says.
func TestClassify_RealNotFoundBody(t *testing.T) {
	body := fixture(t, "error.404.json")
	err := Classify(404, body)
	if err == nil {
		t.Fatal("Classify(404) returned no error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindNotFound)
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("error = %v, want it to carry GitHub's own message", err)
	}
	if !strings.Contains(err.Error(), "cannot see it") {
		t.Errorf("error = %v, want it to say a private resource looks identical", err)
	}
}

// TestClassify_StatusMapping pins every status this plugin maps.
//
// Only the 404 body above is a real capture; GitHub does not hand out 401,
// 403 or 422 bodies on demand, so those rows drive the mapping with a
// representative message and assert the KIND, which is the part callers
// branch on. That limit is stated here rather than implied.
func TestClassify_StatusMapping(t *testing.T) {
	cases := []struct {
		status  int
		message string
		want    cascade.Kind
	}{
		{401, "Bad credentials", cascade.KindPermissionDenied},
		{403, "API rate limit exceeded for user", cascade.KindQuotaExhausted},
		{403, "Resource not accessible by integration", cascade.KindPermissionDenied},
		{404, "Not Found", cascade.KindNotFound},
		{422, "Validation Failed", cascade.KindInvalidInput},
		{429, "Too many requests", cascade.KindQuotaExhausted},
		{500, "Server Error", cascade.KindUnavailable},
		{418, "teapot", cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		body := []byte(`{"message":"` + tc.message + `"}`)
		err := Classify(tc.status, body)
		if err == nil {
			t.Errorf("Classify(%d, %q) returned no error", tc.status, tc.message)
			continue
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != tc.want {
			t.Errorf("Classify(%d, %q) kind = %v, want %v", tc.status, tc.message, kind, tc.want)
		}
	}
}

// TestClassifySeparatesRateLimitFromForbidden is the distinction the whole
// 403 branch exists for: one is worth retrying after a wait, the other
// never is, and GitHub uses the same status for both.
func TestClassifySeparatesRateLimitFromForbidden(t *testing.T) {
	limited := Classify(403, []byte(`{"message":"API rate limit exceeded"}`))
	forbidden := Classify(403, []byte(`{"message":"Must have admin rights"}`))

	lk, _ := cascade.KindOf(limited)
	fk, _ := cascade.KindOf(forbidden)
	if lk == fk {
		t.Fatalf("rate-limited and forbidden both mapped to %v; a caller cannot tell retry from refusal", lk)
	}
	if lk != cascade.KindQuotaExhausted {
		t.Errorf("rate-limited kind = %v, want %v", lk, cascade.KindQuotaExhausted)
	}
	if fk != cascade.KindPermissionDenied {
		t.Errorf("forbidden kind = %v, want %v", fk, cascade.KindPermissionDenied)
	}
}

// TestClassifySuccessIsNotAnError proves 2xx produces nil.
func TestClassifySuccessIsNotAnError(t *testing.T) {
	for _, status := range []int{200, 201, 204, 304} {
		if err := Classify(status, nil); err != nil {
			t.Errorf("Classify(%d) = %v, want nil", status, err)
		}
	}
}

// TestRepoRequests covers path construction and the segment guard.
func TestRepoRequests(t *testing.T) {
	got, err := ListReposRequest("acamarata", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "GET" || got.Path != "/users/acamarata/repos?per_page=2" {
		t.Fatalf("ListReposRequest = %+v", got)
	}
	if got.URL() != APIBase+"/users/acamarata/repos?per_page=2" {
		t.Fatalf("URL() = %q", got.URL())
	}

	one, err := GetRepoRequest("acamarata", "cascade")
	if err != nil {
		t.Fatal(err)
	}
	if one.Path != "/repos/acamarata/cascade" {
		t.Fatalf("GetRepoRequest path = %q", one.Path)
	}
}

// TestRequestsRefuseTraversingSegments is the boundary that keeps a tool
// argument from addressing an endpoint this plugin never meant to expose.
func TestRequestsRefuseTraversingSegments(t *testing.T) {
	bad := []string{"", "   ", "../admin", "owner/extra", ".."}
	for _, seg := range bad {
		if _, err := GetRepoRequest(seg, "cascade"); err == nil {
			t.Errorf("GetRepoRequest accepted owner %q", seg)
		}
		if _, err := GetRepoRequest("acamarata", seg); err == nil {
			t.Errorf("GetRepoRequest accepted repo %q", seg)
		}
	}
}

// TestCloneURLRefusesAnAbsentValue proves the plugin reports what GitHub
// gave rather than assembling a URL it guessed.
func TestCloneURLRefusesAnAbsentValue(t *testing.T) {
	if _, err := CloneURL(Repo{FullName: "a/b"}); err == nil {
		t.Fatal("CloneURL invented a URL for a repository that reported none")
	}
	got, err := CloneURL(Repo{FullName: "a/b", CloneURL: "https://github.com/a/b.git"})
	if err != nil || got != "https://github.com/a/b.git" {
		t.Fatalf("CloneURL = %q, %v", got, err)
	}
}
