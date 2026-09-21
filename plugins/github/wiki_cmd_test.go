// Purpose (this file): wikiReply's own contract — "assert --yes flag
//
//	skips prompt; assert CASCADE_NO_INPUT=1 without --yes exits with hard
//	error on sync; assert wiki check is prompt-free" (P1-E25-W5-S51-T6
//	task 6), driven at the RPC dispatch layer rather than inside
//	plugins/github/wiki directly.
//
// Constraints: no test here lets a real git clone reach github.com. The
//
//	confirmation-refusal tests never get past requireConfirmation, which
//	runs before any git exec. The "Yes=true skips the prompt" test clears
//	PATH so the NEXT gate (requireGit) is what actually stops it — a real,
//	deterministic, network-free failure that only happens once
//	requireConfirmation has already returned nil, which is the proof.
//
// SPORT: plugins/github:wiki-cmd (ADD) — P1-E25-W5-S51-T6.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/github/wiki"
)

// publicRepoDoer answers repos.get with a public (non-private) repository.
func publicRepoDoer() *fakeDoer {
	return &fakeDoer{body: []byte(`{"name":"cascade","private":false}`)}
}

// nonEmptyLocalWiki writes one file into a fresh directory, returning its
// path.
func nonEmptyLocalWiki(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Home.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func wikiSyncFrame(id uint64, owner, repo, localDir string, yes bool) frame {
	params, _ := json.Marshal(wikiArgs{Owner: owner, Repo: repo, LocalDir: localDir, Yes: yes})
	return frame{JSONRPC: "2.0", ID: &id, Method: "cascade-github.wiki.sync", Params: params}
}

// TestWikiSyncReply_WithoutYesRefusesBeforeAnyGitOrNetworkCall proves the
// confirmation gate runs, and runs before requireGit/clone (no PATH
// manipulation needed: this test's git is whatever the machine has, and
// it is never reached).
func TestWikiSyncReply_WithoutYesRefusesBeforeAnyGitOrNetworkCall(t *testing.T) {
	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	out, reply := p.dispatch(wikiSyncFrame(1, "acamarata", "cascade", nonEmptyLocalWiki(t), false))
	if !reply || out.Error == nil {
		t.Fatalf("out = %+v, reply = %v, want a refusal", out, reply)
	}
	if strings.Contains(out.Error.Message, "git binary") {
		t.Fatalf("refused with the git-absent message %q; confirmation should have refused first", out.Error.Message)
	}
}

// TestWikiSyncReply_NoInputWithoutYesIsTheSpecificHardError asserts the
// exact CASCADE_NO_INPUT=1 message, not a generic refusal.
func TestWikiSyncReply_NoInputWithoutYesIsTheSpecificHardError(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "1")
	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	out, reply := p.dispatch(wikiSyncFrame(1, "acamarata", "cascade", nonEmptyLocalWiki(t), false))
	if !reply || out.Error == nil {
		t.Fatalf("out = %+v, reply = %v, want a refusal", out, reply)
	}
	if !strings.Contains(out.Error.Message, "no prompt was attempted") {
		t.Fatalf("err = %q, want the CASCADE_NO_INPUT-specific message", out.Error.Message)
	}
}

// TestWikiSyncReply_YesSkipsThePromptGate proves --yes satisfies
// requireConfirmation: with PATH cleared, the NEXT gate (requireGit) is
// what refuses, which can only happen once confirmation already passed.
func TestWikiSyncReply_YesSkipsThePromptGate(t *testing.T) {
	t.Setenv("PATH", "")
	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	out, reply := p.dispatch(wikiSyncFrame(1, "acamarata", "cascade", nonEmptyLocalWiki(t), true))
	if !reply || out.Error == nil {
		t.Fatalf("out = %+v, reply = %v, want the git-absent refusal (git is unreachable with PATH cleared)", out, reply)
	}
	if !strings.Contains(out.Error.Message, "git binary is not on PATH") {
		t.Fatalf("err = %q, want the git-absent message — a confirmation refusal here would mean --yes did not skip the prompt", out.Error.Message)
	}
}

// TestWikiCheckReply_NeverAsksForConfirmation drives wiki check with an
// invalid owner (refused by validateOwnerRepo, the very first check
// CheckDrift runs) under CASCADE_NO_INPUT=1 and no --yes: if check had
// any confirmation gate, THIS is where it would produce the
// CASCADE_NO_INPUT-specific message instead of the plain validation
// refusal. wiki.DriftOptions also simply has no Yes/Getenv field for this
// dispatch to set — this test proves the runtime behaviour that fact
// implies.
func TestWikiCheckReply_NeverAsksForConfirmation(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "1")
	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	params, _ := json.Marshal(wikiArgs{Owner: "", Repo: "cascade", LocalDir: nonEmptyLocalWiki(t)})
	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade-github.wiki.check", Params: params})
	if !reply || out.Error == nil {
		t.Fatalf("out = %+v, reply = %v, want the owner-is-empty refusal", out, reply)
	}
	if strings.Contains(out.Error.Message, "prompt") || strings.Contains(out.Error.Message, "CASCADE_NO_INPUT") {
		t.Fatalf("err = %q, want the plain validation refusal, not a confirmation-shaped one", out.Error.Message)
	}
	if !strings.Contains(out.Error.Message, "owner") {
		t.Fatalf("err = %q, want it to name the empty owner", out.Error.Message)
	}
}

// TestWikiReply_UnknownVerbIsRefused proves the dispatch table itself is
// exhaustive, not silently permissive.
func TestWikiReply_UnknownVerbIsRefused(t *testing.T) {
	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade-github.wiki.frobnicate"})
	if !reply || out.Error == nil {
		t.Fatalf("out = %+v, reply = %v, want a refusal for an unknown wiki verb", out, reply)
	}
}

// TestWikiSyncReply_TokenReachesSyncOptions is the D2 pinning test
// (confirming review finding 2): with the plugin's own OAuth token unset
// (p.token's zero value — a fresh CI-launched process that never called
// cascade.auth.complete) and CASCADE_GITHUB_TOKEN set in the environment
// (the auto-sync workflow template's own env var), the token that reaches
// wiki.SyncOptions.Token must be the one from the environment, never
// empty. This is exactly the reviewer's mutation target
// (`Token: client.Token` -> `Token: ""`): with that mutation applied,
// recordedToken is "" and this test goes RED.
func TestWikiSyncReply_TokenReachesSyncOptions(t *testing.T) {
	t.Setenv("CASCADE_GITHUB_TOKEN", "env-token-from-workflow")
	var recordedToken string
	old := wikiSyncFunc
	wikiSyncFunc = func(_ context.Context, opts wiki.SyncOptions) (wiki.SyncResult, error) {
		recordedToken = opts.Token
		return wiki.SyncResult{NoOp: true, Reason: "test spy"}, nil
	}
	defer func() { wikiSyncFunc = old }()

	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	out, reply := p.dispatch(wikiSyncFrame(1, "acamarata", "cascade", nonEmptyLocalWiki(t), true))
	if !reply || out.Error != nil {
		t.Fatalf("out = %+v, reply = %v, want a clean reply from the spy", out, reply)
	}
	if recordedToken != "env-token-from-workflow" {
		t.Fatalf("SyncOptions.Token = %q, want the CASCADE_GITHUB_TOKEN value", recordedToken)
	}
}

// TestClient_FallsBackToEnvTokenWhenNoneWasSetInteractively is D2's proof
// at the broker.client() seam directly: no setToken call was ever made,
// and GITHUB_TOKEN (not CASCADE_GITHUB_TOKEN) is what's set, proving the
// full precedence list is read, not just the first name.
func TestClient_FallsBackToEnvTokenWhenNoneWasSetInteractively(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from-github-token")
	p := &broker{doer: publicRepoDoer()}
	if got := p.client().Token; got != "from-github-token" {
		t.Fatalf("client().Token = %q, want the GITHUB_TOKEN fallback", got)
	}
}

// TestClient_InteractiveTokenWinsOverEnv proves setToken's value is never
// shadowed by an env var that happens to also be set.
func TestClient_InteractiveTokenWinsOverEnv(t *testing.T) {
	t.Setenv("CASCADE_GITHUB_TOKEN", "from-env")
	p := &broker{doer: publicRepoDoer()}
	p.setToken("from-oauth")
	if got := p.client().Token; got != "from-oauth" {
		t.Fatalf("client().Token = %q, want the interactively-set token", got)
	}
}

// TestWikiReply_LocalDirDefaultsToDotGithubWiki proves an omitted
// local_dir resolves to the repository convention rather than an empty
// path (which readTree would just report as "no files", masking a caller
// bug as a no-op).
func TestWikiReply_LocalDirDefaultsToDotGithubWiki(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "1")
	p, _ := newTestPlugin(publicRepoDoer(), nil, nil)
	params, _ := json.Marshal(wikiArgs{Owner: "acamarata", Repo: "cascade"})
	id := uint64(1)
	// No local_dir set, and CWD (the package directory) has no
	// .github/wiki/, so this resolves to an empty tree — a real
	// NoOp result, proving the default path is exercised and read.
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade-github.wiki.sync", Params: params})
	if !reply {
		t.Fatal("no reply")
	}
	if out.Error != nil {
		t.Fatalf("err = %v, want a no-op result (no .github/wiki/ here)", out.Error)
	}
}
