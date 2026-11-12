//go:build !windows

// Purpose: exercise the rendered hook under /bin/sh and real receive quarantine.
package ci

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci/attest"
)

func installTestGate(t *testing.T, repo, body string) (string, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cascade's pinned binary")
	gateOK(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0755))
	digest, err := ComputeSHA256(bin)
	gateOK(t, err)
	gateOK(t, GitGateInstall(repo, "repo-1", bin, digest))
	hook, err := gateHookPath(repo)
	gateOK(t, err)
	return bin, hook
}

func TestGitGateInstallIdempotent(t *testing.T) {
	repo := baselineRepo(t)
	bin, hook := installTestGate(t, repo, "exit 0\n")
	before, err := os.ReadFile(hook)
	gateOK(t, err)
	info, err := os.Stat(hook)
	gateOK(t, err)
	digest, err := ComputeSHA256(bin)
	gateOK(t, err)
	gateOK(t, GitGateInstall(repo, "repo-1", bin, digest))
	after, err := os.ReadFile(hook)
	gateOK(t, err)
	infoAfter, err := os.Stat(hook)
	gateOK(t, err)
	if !bytes.Equal(before, after) || !os.SameFile(info, infoAfter) || !info.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("second install rewrote the hook")
	}
	s, err := GitGateStatus(repo, MirrorState{Enabled: true, Backlog: 3})
	gateOK(t, err)
	if !s.HookInstalled || s.BinaryDrift || s.BinaryPath != bin || s.RepoID != "repo-1" || !s.MirrorEnabled || s.Backlog != 3 {
		t.Fatalf("status: %+v", s)
	}
}

func TestGitGateInstallRepairsDisabledHook(t *testing.T) {
	repo := baselineRepo(t)
	bin, hook := installTestGate(t, repo, "exit 0\n")
	before, err := os.ReadFile(hook)
	gateOK(t, err)
	gateOK(t, os.Chmod(hook, 0600))
	digest, err := ComputeSHA256(bin)
	gateOK(t, err)
	gateOK(t, GitGateInstall(repo, "repo-1", bin, digest))
	after, err := os.ReadFile(hook)
	gateOK(t, err)
	info, err := os.Stat(hook)
	gateOK(t, err)
	if !bytes.Equal(before, after) || info.Mode().Perm() != 0755 {
		t.Fatal("reinstall did not preserve and enable the hook")
	}
}

func TestGitGateHookPinsBinary(t *testing.T) {
	repo := baselineRepo(t)
	bin, hook := installTestGate(t, repo, "printf '%s\\n' \"$@\"\n/bin/cat\n")
	data, err := os.ReadFile(hook)
	gateOK(t, err)
	digest, err := ComputeSHA256(bin)
	gateOK(t, err)
	if !strings.Contains(string(data), shellQuote(bin)) || !strings.Contains(string(data), digest) {
		t.Fatal("hook lacks binary path or digest")
	}
	cmd := exec.Command("/bin/sh", hook)
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	cmd.Stdin = strings.NewReader("receive input\n")
	out, err := cmd.CombinedOutput()
	gateOK(t, err)
	if string(out) != "node\ngit-gate\npre-receive\nreceive input\n" {
		t.Fatalf("hook args/stdin: %q", out)
	}
}

func TestGitGateHookRejectsChangedBinary(t *testing.T) {
	repo := baselineRepo(t)
	marker := filepath.Join(t.TempDir(), "executed")
	bin, hook := installTestGate(t, repo, "exit 0\n")
	gateOK(t, os.WriteFile(bin, []byte("#!/bin/sh\n: > "+shellQuote(marker)+"\n"), 0755))
	cmd := exec.Command("/bin/sh", hook)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "re-run cascade node git-gate install") {
		t.Fatalf("changed binary: %v, %s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("changed binary executed")
	}
	s, err := GitGateStatus(repo, MirrorState{})
	gateOK(t, err)
	if !s.BinaryDrift {
		t.Fatal("status missed binary drift")
	}
}

func TestGitGateWindowsRefuses(t *testing.T) {
	t.Skip(ErrWindowsNotSupported.Error())
}

func TestGitGateRealPush(t *testing.T) {
	src := baselineRepo(t)
	accepted := baselineCommit(t, src)
	tree := baselineGitTest(t, src, "rev-parse", "HEAD^{tree}")
	bare := filepath.Join(t.TempDir(), "receive.git")
	baselineGitTest(t, filepath.Dir(bare), "init", "--bare", bare)
	testbin, err := os.Executable()
	gateOK(t, err)
	installTestGate(t, bare, "exec "+shellQuote(testbin)+" -test.run '^TestGitGateReceiveProcess$'\n")
	t.Setenv("CASCADE_GATE_PUSH_TEST", "1")
	t.Setenv("CASCADE_GATE_ACCEPTED", accepted)
	t.Setenv("CASCADE_GATE_TREE", tree)
	baselineGitTest(t, src, "push", bare, "HEAD:refs/heads/main")
	gateOK(t, os.WriteFile(filepath.Join(src, "tampered"), []byte("unattested change\n"), 0600))
	baselineGitTest(t, src, "add", "tampered")
	baselineGitTest(t, src, "commit", "-m", "tampered")
	cmd := exec.Command("git", "push", bare, "HEAD:refs/heads/main")
	cmd.Dir = src
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "attestation") {
		t.Fatalf("tampered push: %v, %s", err, stderr.String())
	}
	if got := baselineGitTest(t, bare, "rev-parse", "refs/heads/main"); got != accepted {
		t.Fatalf("ref moved to %s", got)
	}
}

// TestGitGateReceiveProcess is the test executable's receive entrypoint. Keys
// exist only in memory; evidence is checked by the production AttestVerify.
func TestGitGateReceiveProcess(t *testing.T) {
	if os.Getenv("CASCADE_GATE_PUSH_TEST") != "1" {
		return
	}
	repo, err := os.Getwd()
	gateOK(t, err)
	refs, err := ParsePreReceiveStdin(os.Stdin)
	gateOK(t, err)
	r := newGateRig(t)
	rec := r.record(attest.ClassEnrolledNode)
	rec.CommitSHA, rec.TreeHash = os.Getenv("CASCADE_GATE_ACCEPTED"), os.Getenv("CASCADE_GATE_TREE")
	r.insert(t, rec, r.priv)
	err = CheckPush(context.Background(), repo, refs, func(commit string) error {
		// A real rev-list inside receive quarantine must see incoming objects.
		out, err := baselineGit(context.Background(), repo, "rev-list", commit, "--not", "--all")
		if err != nil || !strings.Contains(string(out), commit) {
			t.Fatalf("quarantine rev-list: %s, %v", out, err)
		}
		tree, err := baselineGit(context.Background(), repo, "rev-parse", commit+"^{tree}")
		if err != nil {
			return err
		}
		want, err := ResolveGateWant(context.Background(), repo, AttestWant{GateDir: repo, CommitSHA: commit, TreeHash: strings.TrimSpace(string(tree))}, staticGate("repo-1"))
		if err != nil {
			return err
		}
		return AttestVerify(context.Background(), want, true, r.store, r.trust, r.facts, staticGate("repo-1"), gatePolicy, r.now)
	})
	if err != nil {
		t.Fatalf("attestation: %v", err)
	}
}
