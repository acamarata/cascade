//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/policy"
)

// TestMirrorCapabilityGrantable: ci.mirror is registered in both the CLI and
// the daemon capability sets at the external-side-effect class, so
// `cascade policy grant ci.mirror` has a name to resolve.
func TestMirrorCapabilityGrantable(t *testing.T) {
	if ci.MirrorCapability != "ci.mirror" {
		t.Fatalf("MirrorCapability = %q", ci.MirrorCapability)
	}
	for name, caps := range map[string][]policy.Capability{"github-ci": githubCICapabilities(), "daemon": bootCapabilities()} {
		reg := policy.NewMemoryRegistry()
		for _, c := range caps {
			if err := reg.Add(context.Background(), c); err != nil {
				t.Fatalf("%s: register %s: %v", name, c.Name, err)
			}
		}
		got, err := reg.Lookup(context.Background(), ci.MirrorCapability)
		if err != nil || got.Class() != policy.ClassExternalSideEffect {
			t.Fatalf("%s: Lookup(ci.mirror) = %+v, %v; want a registered L3 capability", name, got, err)
		}
	}
}

func mirrorTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestExecMirrorGitFastForwardOnly drives the real git runner against a local
// bare remote: a fast-forward lands, a diverged remote is reported as not an
// ancestor, and no tag or other ref is created.
func TestExecMirrorGitFastForwardOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	remote, work := filepath.Join(root, "r.git"), filepath.Join(root, "w")
	mirrorTestGit(t, root, "init", "-q", "--bare", remote)
	mirrorTestGit(t, root, "init", "-q", work)
	mirrorTestGit(t, work, "commit", "-q", "--allow-empty", "-m", "one")
	first := mirrorTestGit(t, work, "rev-parse", "HEAD")
	mirrorTestGit(t, work, "commit", "-q", "--allow-empty", "-m", "two")
	second := mirrorTestGit(t, work, "rev-parse", "HEAD")
	g := execMirrorGit{Dir: work, Env: ci.MirrorPushEnv(os.Environ())}
	ctx := context.Background()
	if err := g.Push(ctx, remote, first, "main"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if tip, err := g.RemoteTip(ctx, remote, "main"); err != nil || tip != first {
		t.Fatalf("RemoteTip = %q, %v; want %q", tip, err, first)
	}
	if ok, err := g.IsAncestor(ctx, first, second); err != nil || !ok {
		t.Fatalf("IsAncestor(first, second) = %v, %v", ok, err)
	}
	if ok, err := g.IsAncestor(ctx, second, first); err != nil || ok {
		t.Fatalf("IsAncestor(second, first) = %v, %v; want false", ok, err)
	}
	if err := g.Push(ctx, remote, second, "main"); err != nil {
		t.Fatalf("fast-forward push: %v", err)
	}
	if refs := mirrorTestGit(t, remote, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
		t.Fatalf("remote refs = %q, want only refs/heads/main", refs)
	}
}
