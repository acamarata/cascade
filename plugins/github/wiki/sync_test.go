// Purpose (this file): Sync's contract — every acceptance criterion this
//
//	ticket names for `cascade github wiki sync`, each driven against a
//	real local git remote (never github.com). The push-conflict scenario
//	and resolveWikiURL/validateOwnerRepo unit tests live in
//	sync_conflict_test.go, split out to stay under the 300-line cap.
//
// SPORT: plugins/github/wiki:sync (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wantSyncURL is the exact URL Sync's real resolveWikiURL produces for
// owner "acamarata", repo "cascade" — computed once here so every test in
// this file that needs to redirect it stays honest about what production
// code actually built. The URL no longer depends on the token (D3):
// resolveWikiURL takes none.
func wantSyncURL() string {
	return resolveWikiURL("acamarata", "cascade")
}

func baseOpts(t *testing.T, remote, local string) SyncOptions {
	want := wantSyncURL()
	return SyncOptions{
		Owner: "acamarata", Repo: "cascade", Token: "tok",
		LocalDir:  local,
		Yes:       true,
		Runner:    &redirectingRunner{want: want, local: remote},
		Checker:   fakeChecker{private: false},
		MkdirTemp: mkdirTempFor(t),
		Getenv:    func(string) string { return "" },
	}
}

// TestSync_EmptyLocalWikiIsNoOp: "If .github/wiki/ is empty, the command
// refuses (no-op, exit 0, delta reported)."
func TestSync_EmptyLocalWikiIsNoOp(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, nil)
	opts := baseOpts(t, remote, local)
	res, err := Sync(context.Background(), opts)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.NoOp || res.Pushed {
		t.Fatalf("res = %+v, want a no-op that never pushed", res)
	}
	if res.Reason == "" {
		t.Fatal("NoOp result reported no reason")
	}
}

// TestSync_PrivateRepoRefuses: "cascade github wiki sync on a private repo
// exits with an actionable refusal and non-zero code."
func TestSync_PrivateRepoRefuses(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, map[string]string{"Home.md": "x"})
	opts := baseOpts(t, remote, local)
	opts.Checker = fakeChecker{private: true}
	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync on a private repo returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err kind = %v, want KindPolicyDenied", err)
	}
	if !strings.Contains(err.Error(), "private") {
		t.Fatalf("err = %v, want it to name the repository as private", err)
	}
}

// TestSync_VisibilityCheckFailureFailsClosed proves an unanswerable
// visibility check is never treated as "public".
func TestSync_VisibilityCheckFailureFailsClosed(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, map[string]string{"Home.md": "x"})
	opts := baseOpts(t, remote, local)
	opts.Checker = fakeChecker{err: errors.New("network down")}
	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync proceeded despite an unanswerable visibility check")
	}
}

// TestSync_ConfirmationRequired: without --yes, sync refuses (the CLI
// layer's prompt is what would normally supply Yes=true).
func TestSync_ConfirmationRequired(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, map[string]string{"Home.md": "x"})
	opts := baseOpts(t, remote, local)
	opts.Yes = false
	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync without --yes returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("err kind = %v, want KindElevationRequired", err)
	}
}

// TestSync_NoInputWithoutYesHardError: "CASCADE_NO_INPUT=1 without --yes
// exits with a hard error (asserted)" — and the message names the exact
// condition, not a generic refusal.
func TestSync_NoInputWithoutYesHardError(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, map[string]string{"Home.md": "x"})
	opts := baseOpts(t, remote, local)
	opts.Yes = false
	opts.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync returned nil error")
	}
	if !strings.Contains(err.Error(), "no prompt was attempted") {
		t.Fatalf("err = %v, want the CASCADE_NO_INPUT-specific message", err)
	}
}

// TestSync_GitAbsent: "git-absent behavior (sync and check both) produces
// an actionable error rather than a panic."
func TestSync_GitAbsent(t *testing.T) {
	old := activeLocator
	activeLocator = fakeLocator{}
	defer func() { activeLocator = old }()

	remote := newBareRemote(t)
	local := writeLocalWiki(t, map[string]string{"Home.md": "x"})
	opts := baseOpts(t, remote, local)
	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync with no git binary returned nil error")
	}
	if !strings.Contains(err.Error(), "git binary is not on PATH") {
		t.Fatalf("err = %v, want the git-absent message", err)
	}
}

// TestSync_PushesRealChanges drives the full clone/overwrite/commit/push
// pipeline against a real local bare repository standing in for the
// remote wiki. This is the AC1 proof: "cascade github wiki sync with
// --yes clones the wiki repo, overwrites from .github/wiki/, commits, and
// pushes."
func TestSync_PushesRealChanges(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "old content", "Stale.md": "remove me"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "new content", "New.md": "added"})

	opts := baseOpts(t, remote, local)
	res, err := Sync(context.Background(), opts)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.NoOp || !res.Pushed {
		t.Fatalf("res = %+v, want a real push", res)
	}
	wantChanged := []string{"Home.md", "New.md", "Stale.md"}
	if !equalStrings(res.Changed, wantChanged) {
		t.Fatalf("Changed = %v, want %v", res.Changed, wantChanged)
	}

	// Verify against the real remote, independent of Sync's own bookkeeping.
	after := cloneToTempAndRead(t, remote)
	if string(after["Home.md"]) != "new content" {
		t.Errorf("remote Home.md = %q, want %q", after["Home.md"], "new content")
	}
	if _, ok := after["Stale.md"]; ok {
		t.Error("remote still carries Stale.md; it should have been removed")
	}
	if string(after["New.md"]) != "added" {
		t.Errorf("remote New.md = %q, want %q", after["New.md"], "added")
	}
}

// TestSync_AlreadyUpToDateIsNoOp: pushing identical content is a no-op,
// never an empty commit.
func TestSync_AlreadyUpToDateIsNoOp(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "same"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "same"})

	opts := baseOpts(t, remote, local)
	res, err := Sync(context.Background(), opts)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.NoOp || res.Pushed {
		t.Fatalf("res = %+v, want a no-op", res)
	}
}

// TestSync_TokenNeverAppearsInGitArgv is the D3 proof (confirming review
// finding 3) at the Sync level: drives a real push with a token and
// asserts the token appears in NO captured argv (clone or push), only in
// the auth env gitAuthEnv builds.
func TestSync_TokenNeverAppearsInGitArgv(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "old"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "new"})

	opts := baseOpts(t, remote, local)
	opts.Token = "super-secret-token"
	runner := opts.Runner.(*redirectingRunner)

	if _, err := Sync(context.Background(), opts); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	for _, call := range runner.calls {
		for _, arg := range call {
			if strings.Contains(arg, "super-secret-token") {
				t.Fatalf("argv %v contains the token", call)
			}
		}
	}
	wantBasic := base64.StdEncoding.EncodeToString([]byte("x-access-token:super-secret-token"))
	foundInEnv := false
	for _, env := range runner.envs {
		for _, kv := range env {
			if strings.Contains(kv, wantBasic) {
				foundInEnv = true
			}
		}
	}
	if !foundInEnv {
		t.Fatal("token never reached the git auth env either; a real sync would fail to authenticate")
	}
}

// TestSync_NilCheckerFailsClosed proves SyncOptions with no Checker refuses
// rather than treating the repository as implicitly public.
func TestSync_NilCheckerFailsClosed(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, map[string]string{"Home.md": "x"})
	opts := baseOpts(t, remote, local)
	opts.Checker = nil
	_, err := Sync(context.Background(), opts)
	if err == nil {
		t.Fatal("Sync with a nil Checker returned nil error")
	}
}
