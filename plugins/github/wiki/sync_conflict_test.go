// Purpose (this file): the push-conflict scenario (a real non-fast-forward
//
//	rejection from the real git binary) and the small pure-function tests
//	for resolveWikiURL/validateOwnerRepo — split out of sync_test.go to
//	stay under the 300-line cap.
//
// SPORT: plugins/github/wiki:sync (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSync_PushConflict simulates a concurrent wiki edit landing between
// this sync's clone and its push: the push must fail with typed guidance,
// never silently overwrite or panic.
func TestSync_PushConflict(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "v1"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "v2-from-this-sync"})

	want := wantSyncURL()
	runner := &conflictInjectingRunner{t: t, want: want, local: remote, remote: remote}
	opts := baseOpts(t, remote, local)
	opts.Runner = runner

	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync into a concurrently-modified remote returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err kind = %v, want KindConflict", err)
	}
	if !strings.Contains(err.Error(), "cascade github wiki check") {
		t.Fatalf("err = %v, want guidance naming `cascade github wiki check`", err)
	}
}

// TestSync_PushConflictErrorNeverContainsTheToken is D3's error-text
// scrub proof: a failed push's error text must never leak the token,
// even though the token IS present (in env, per that same call).
func TestSync_PushConflictErrorNeverContainsTheToken(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "v1"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "v2"})

	want := wantSyncURL()
	runner := &conflictInjectingRunner{t: t, want: want, local: remote, remote: remote}
	opts := baseOpts(t, remote, local)
	opts.Token = "super-secret-token"
	opts.Runner = runner

	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync into a concurrently-modified remote returned nil error")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("err = %v, leaks the token", err)
	}
}

// conflictInjectingRunner redirects the clone URL exactly as
// redirectingRunner does, and — the first time it sees a `push` — lands
// an independent commit on remote first, so the real push this test drives
// hits a genuine non-fast-forward rejection from the real git binary.
type conflictInjectingRunner struct {
	t      *testing.T
	want   string
	local  string
	remote string

	once sync.Once
}

func (r *conflictInjectingRunner) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, []byte, error) {
	rewritten := make([]string, len(args))
	copy(rewritten, args)
	if len(rewritten) >= 2 && rewritten[0] == "clone" && rewritten[1] == r.want {
		rewritten[1] = r.local
	}
	if len(rewritten) > 0 && rewritten[0] == "push" {
		r.once.Do(func() { landConcurrentCommit(r.t, r.remote) })
	}
	return execRunner{}.Run(ctx, dir, env, rewritten...)
}

// landConcurrentCommit clones remote fresh, commits an unrelated change,
// and pushes it — modeling a second writer that raced this sync.
func landConcurrentCommit(t *testing.T, remote string) {
	t.Helper()
	tmp := t.TempDir()
	runGit(t, "", "clone", remote, tmp)
	runGit(t, tmp, "-c", "user.name=other-writer", "-c", "user.email=other@example.com",
		"commit", "--allow-empty", "-m", "a concurrent edit")
	runGit(t, tmp, "push", "origin", "HEAD:master")
}

// cloneToTempAndRead clones remote fresh and reads its file content, used
// to verify a push's effect independent of Sync's own return value.
func cloneToTempAndRead(t *testing.T, remote string) map[string][]byte {
	t.Helper()
	tmp := t.TempDir()
	runGit(t, "", "clone", remote, tmp)
	tree, err := readTree(tmp)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolveWikiURL_NeverContainsAToken is the D3 proof at the URL
// level: the URL production code builds becomes a git argv element
// (`git clone <url> <dir>`), so it must never carry a secret regardless
// of caller.
func TestResolveWikiURL_NeverContainsAToken(t *testing.T) {
	url := resolveWikiURL("acamarata", "cascade")
	if url != "https://github.com/acamarata/cascade.wiki.git" {
		t.Fatalf("resolveWikiURL = %q", url)
	}
}

// TestGitAuthEnv_CarriesTheTokenOnlyInEnvNeverInTheURL is the D3 proof at
// the auth-env level: the header value carries the token (as the
// documented `x-access-token:<token>` Basic-auth identity git's own
// extraHeader mechanism expects, base64-encoded — never the raw token
// text, which would defeat the point of a header); the URL (an unrelated
// production call) never carries it in any form.
func TestGitAuthEnv_CarriesTheTokenOnlyInEnvNeverInTheURL(t *testing.T) {
	env := gitAuthEnv("super-secret-token")
	wantBasic := base64.StdEncoding.EncodeToString([]byte("x-access-token:super-secret-token"))
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, wantBasic) {
		t.Fatalf("gitAuthEnv = %v, want it to carry the base64 Basic-auth value %q", env, wantBasic)
	}
	if strings.Contains(joined, "super-secret-token") {
		t.Fatalf("gitAuthEnv = %v, leaks the RAW token text (must be base64-encoded)", env)
	}
	url := resolveWikiURL("acamarata", "cascade")
	if strings.Contains(url, "super-secret-token") {
		t.Fatalf("resolveWikiURL leaked the token into the URL: %q", url)
	}
}

// TestGitAuthEnv_EmptyTokenReturnsNoEnv covers the unauthenticated
// (public, unauthenticated read) path: no token means no extra env, the
// same as an anonymous git clone.
func TestGitAuthEnv_EmptyTokenReturnsNoEnv(t *testing.T) {
	if env := gitAuthEnv(""); env != nil {
		t.Fatalf("gitAuthEnv(\"\") = %v, want nil", env)
	}
}

// TestValidateOwnerRepo_RefusesTraversalAndSeparators guards the values
// that reach a URL and, in drift/overwriteTree, a filesystem path.
func TestValidateOwnerRepo_RefusesTraversalAndSeparators(t *testing.T) {
	cases := []struct{ owner, repo string }{
		{"", "cascade"}, {"acamarata", ""}, {"a/b", "cascade"}, {"acamarata", "../etc"},
	}
	for _, c := range cases {
		if err := validateOwnerRepo(c.owner, c.repo); err == nil {
			t.Errorf("validateOwnerRepo(%q, %q) = nil, want a refusal", c.owner, c.repo)
		}
	}
	if err := validateOwnerRepo("acamarata", "cascade"); err != nil {
		t.Fatalf("validateOwnerRepo(valid pair) = %v", err)
	}
}
