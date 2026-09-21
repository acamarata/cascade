// Purpose (this file): the exec-only git subprocess seam sync.go and
//
//	drift.go both drive. Every git operation this package performs — clone,
//	status, add, commit, push — goes through GitRunner rather than a direct
//	os/exec.Command call scattered across the package, so a test can drive
//	the exact same decision logic against a local, network-free git
//	repository (a bundle or a bare repo under t.TempDir()).
//
// Inputs: a working directory, per-invocation extra environment
//
//	variables (the auth header gitAuthEnv builds; nil for a local
//	operation that needs none) and a git argv; production runs the real
//	`git` binary (exec-only invocation per 06-FORGE-SPEC.md §5.15 — process
//	spawn, not egress in the net/http sense).
//
// Outputs: stdout, stderr and the exec error, all three returned so a
//
//	caller can build an actionable message from git's own text rather than
//	a bare exit code.
//
// Constraints: plugins/** may not import internal/** (Art.10.2), so binary
//
//	presence is resolved locally (mirrors plugins/nself/doctor.go's
//	binaryLocator) rather than through a shared internal helper. This file
//	imports os/exec, which is why plugins/github/wiki is on
//	internal/build/egress_allow.go's EgressExecNotYetMigrated list — a
//	scope deviation this ticket's journal declares (see that file's entry
//	for the reason).
//
//	The OAuth token NEVER appears in a git argv, in git config written to
//	disk, or in an error/log message (D3, confirming review finding 3):
//	gitAuthEnv carries it as an HTTP Basic Authorization header injected
//	through GIT_CONFIG_COUNT/GIT_CONFIG_KEY_0/GIT_CONFIG_VALUE_0 — the
//	same mechanism actions/checkout uses for the identical problem — which
//	is process-environment-scoped (invisible to `ps`, unlike argv) and
//	never persisted into the cloned repository's own .git/config the way
//	an embedded-in-URL credential would be.
//
// SPORT: plugins/github/wiki:runner (ADD) — P1-E25-W5-S51-T6.

package wiki

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// GitRunner performs one git subprocess invocation in dir, with env
// appended to the child's environment (nil for none). It never panics and
// never opens a socket of its own: whatever bytes leave the machine leave
// through the `git` binary this seam invokes, never through this
// package's own Go code.
type GitRunner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) (stdout, stderr []byte, err error)
}

// wikiGitHostExtraHeaderKey is the git config key gitAuthEnv sets, scoped
// to exactly the git-protocol host this package ever reaches (never a
// bare "http.extraheader", which would attach the header to every remote
// a command touches).
const wikiGitHostExtraHeaderKey = "http.https://github.com/.extraheader"

// gitAuthEnv returns the GIT_CONFIG_* environment variables that
// authenticate a github.com git operation via an HTTP Basic Authorization
// header injected through git's own extraHeader mechanism — never through
// the URL (D3). An empty token returns nil: the operation proceeds
// unauthenticated, which is correct for a public wiki's read-only clone.
//
// This env is per-invocation only (git does not persist GIT_CONFIG_*
// values into any file), so cloneWiki and pushWiki each receive it fresh
// — the token is never written to the cloned repository's .git/config.
func gitAuthEnv(token string) []string {
	if token == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=" + wikiGitHostExtraHeaderKey,
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
	}
}

// binaryLocator abstracts resolving the `git` binary so tests can prove
// the git-absent path without depending on what this machine's PATH holds
// (same seam shape as plugins/nself/doctor.go's binaryLocator, for the
// identical reason).
type binaryLocator interface {
	Lookup(binary string) (path string, err error)
}

// execLocator is the real, production binaryLocator.
type execLocator struct {
	lookPath func(string) (string, error)
}

func (l execLocator) Lookup(binary string) (string, error) {
	look := l.lookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(binary)
	if err != nil {
		return "", &gitAbsentError{GOOS: goruntime.GOOS}
	}
	return path, nil
}

// gitAbsentError is the typed, actionable failure "git is not installed"
// produces. It never surfaces as a raw exec.ErrNotFound.
type gitAbsentError struct {
	GOOS string
}

func (e *gitAbsentError) Error() string {
	return "cascade-github wiki (" + e.GOOS + "): the git binary is not on PATH; install git and ensure " +
		"it is reachable from this process's PATH"
}

// ErrGitAbsent is the sentinel a caller matches against with errors.As to
// distinguish "git is not installed" from every other git failure.
var ErrGitAbsent = &gitAbsentError{}

// activeLocator resolves `git`. Same-package test files assign it
// directly; there is no exported setter, matching plugins/nself's
// convention that only same-package tests may swap the locator.
var activeLocator binaryLocator = execLocator{}

// requireGit refuses with an actionable error when git is not on PATH. It
// is the first check both Sync and CheckDrift run, before any tempdir is
// created or any clone is attempted.
func requireGit() error {
	if _, err := activeLocator.Lookup("git"); err != nil {
		var absent *gitAbsentError
		if errors.As(err, &absent) {
			return cascade.Wrap(cascade.KindUnavailable, absent, absent.Error())
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade-github wiki: resolving the git binary")
	}
	return nil
}

// execRunner is the real, production GitRunner: exec.CommandContext
// against the system git binary, exactly as required ("exec-only
// invocation of the system git binary").
type execRunner struct{}

// Run implements GitRunner. env, when non-empty, is appended to this
// process's own environment (os.Environ()) for this ONE invocation only —
// gitAuthEnv's GIT_CONFIG_* auth variables reach git this way, never
// through an argv git logs or a `ps` listing can see.
func (execRunner) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// newRunner returns r if non-nil, else the real production runner. Every
// entry point accepts an optional injected runner so a test never has to
// mutate package state to substitute one.
func newRunner(r GitRunner) GitRunner {
	if r != nil {
		return r
	}
	return execRunner{}
}

// cloneWiki clones url into dir, authenticated (if env carries a token)
// through gitAuthEnv rather than through url itself — url never carries a
// credential (D3), so it is safe to repeat verbatim in an error message.
func cloneWiki(ctx context.Context, runner GitRunner, url, dir string, env []string) error {
	_, stderr, err := runner.Run(ctx, "", env, "clone", url, dir)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"cascade-github wiki: cloning %s failed: %s", url, firstLine(stderr))
	}
	return nil
}

// commitAuthorName and commitAuthorEmail identify every sync commit. They
// are passed as `-c` config overrides rather than left to the ambient git
// config, so a commit succeeds even in an environment with no
// user.name/user.email configured (a fresh CI runner or plugin sandbox),
// and so a test never depends on this machine's git config either.
const (
	commitAuthorName  = "cascade-bot"
	commitAuthorEmail = "cascade-bot@users.noreply.github.com"
	// commitMessage is the fixed commit message every sync produces. It is
	// deterministic (no timestamp in the text itself) so two runs against
	// identical content commit the same message; the commit's own author
	// date is what an operator reads for "when".
	commitMessage = "cascade github wiki sync"
)

// commitAll stages every change in dir and commits it. A dir with nothing
// staged is the caller's responsibility to avoid (overwriteTree's
// empty-delta return already short-circuits this in Sync).
func commitAll(ctx context.Context, runner GitRunner, dir string) error {
	if _, stderr, err := runner.Run(ctx, dir, nil, "add", "-A"); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: git add failed: %s", firstLine(stderr))
	}
	commitArgs := []string{
		"-c", "user.name=" + commitAuthorName,
		"-c", "user.email=" + commitAuthorEmail,
		"commit", "-m", commitMessage,
	}
	if _, stderr, err := runner.Run(ctx, dir, nil, commitArgs...); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: git commit failed: %s", firstLine(stderr))
	}
	return nil
}

// pushWiki pushes dir's current HEAD to the `origin` remote clone already
// configured (by cloneWiki) with url — env carries the SAME auth header
// gitAuthEnv built for the clone; env is per-invocation only, so it must
// be supplied again here rather than assumed to persist. A non-fast-
// forward rejection is classified and given guidance rather than passed
// through as git's own terse text.
func pushWiki(ctx context.Context, runner GitRunner, dir, url string, env []string) error {
	_, stderr, err := runner.Run(ctx, dir, env, "push", "origin", "HEAD:master")
	if err != nil {
		if isPushConflict(stderr) {
			return cascade.Wrapf(cascade.KindConflict, err,
				"cascade-github wiki: push to %s was rejected (the remote wiki has commits this sync did not "+
					"see) — run `cascade github wiki check` to inspect the drift, then sync again", url)
		}
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"cascade-github wiki: push to %s failed: %s", url, firstLine(stderr))
	}
	return nil
}

// isPushConflict reports whether stderr names a non-fast-forward
// rejection, the shape a concurrent wiki edit produces.
func isPushConflict(stderr []byte) bool {
	return containsFold(string(stderr), "non-fast-forward") ||
		containsFold(string(stderr), "fetch first") ||
		containsFold(string(stderr), "rejected")
}

// containsFold is a case-insensitive substring test, kept local so this
// package's git-output classification does not depend on strings.ToLower
// allocating twice per call site.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// firstLine returns b's first line, trimmed, for a compact error message.
// git's own stderr is often multi-line; the first line is almost always
// the actionable one.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no output)"
	}
	return s
}
