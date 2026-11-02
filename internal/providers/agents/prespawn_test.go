// Purpose: tests for the shim's pre-spawn surfaces: the minimal-tree
// assertion, the fail-closed secret scan and the spawn-order table.
// Constraints: the credential-shaped sample is assembled from split literals;
// symlink cases skip on windows with the platform gate's message.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-03.

package agents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// canary builds a credential-shaped line at run time.
func canary() string {
	return "ANTHROPIC_API_KEY=sk-" + "ant-api03-" + strings.Repeat("aZ3kQ9", 8)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// minimalWorktreeCase is one AssertMinimalWorktree scenario.
type minimalWorktreeCase struct {
	setup   func(t *testing.T, root, outside string)
	wantErr string
}

// minimalWorktreeCases returns the named AssertMinimalWorktree scenarios.
func minimalWorktreeCases() map[string]minimalWorktreeCase {
	return map[string]minimalWorktreeCase{
		"clean tree": {setup: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "src", "x.go"), "x", 0o600)
			writeFile(t, filepath.Join(root, "pkg", "a", "y.go"), "y", 0o600)
			writeFile(t, filepath.Join(root, "go.mod"), "m", 0o600)
			writeFile(t, filepath.Join(root, ".git", "HEAD"), "h", 0o600)
			writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}", 0o600)
		}},
		"out of scope file": {wantErr: "secrets.txt", setup: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "src", "x.go"), "x", 0o600)
			writeFile(t, filepath.Join(root, "secrets.txt"), "s", 0o600)
		}},
		"out of scope directory": {wantErr: "other", setup: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "other", "z.go"), "z", 0o600)
		}},
		"file beside a nested prefix": {wantErr: "pkg/b.go", setup: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "pkg", "a", "y.go"), "y", 0o600)
			writeFile(t, filepath.Join(root, "pkg", "b.go"), "b", 0o600)
		}},
		"link escaping the root": {wantErr: "src/auth", setup: func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(outside, "token"), "t", 0o600)
			symlinkOrSkip(t, outside, filepath.Join(root, "src", "auth"))
		}},
		"link onto an out of scope path": {wantErr: "docs", setup: func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, "docs", "private.md"), "p", 0o600)
			symlinkOrSkip(t, filepath.Join(root, "docs"), filepath.Join(root, "src", "up"))
		}},
		"dangling link": {wantErr: "src/gone", setup: func(t *testing.T, root, _ string) {
			if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, filepath.Join(root, "nowhere"), filepath.Join(root, "src", "gone"))
		}},
	}
}

func TestAssertMinimalWorktree(t *testing.T) {
	scope := provider.WorktreeScope{InScopePrefixes: []string{"src", "pkg/a"}, DeclaredDeps: []string{"go.mod"}}
	cases := minimalWorktreeCases()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			tc.setup(t, root, outside)
			err := AssertMinimalWorktree(root, scope)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("AssertMinimalWorktree = %v, want nil", err)
				}
				return
			}
			if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AssertMinimalWorktree = %v, want KindPolicyDenied naming %q", err, tc.wantErr)
			}
		})
	}
	for _, bad := range []string{"../x", "/etc"} {
		err := AssertMinimalWorktree(t.TempDir(), provider.WorktreeScope{InScopePrefixes: []string{bad}})
		if !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("prefix %q = %v, want KindPolicyDenied", bad, err)
		}
	}
}

func TestPreSpawnScanFailsClosed(t *testing.T) {
	isolateHome(t)
	scope := provider.WorktreeScope{InScopePrefixes: []string{"src"}}
	clean := t.TempDir()
	writeFile(t, filepath.Join(clean, "src", "a.go"), "package a\n", 0o600)
	if err := RunPreSpawnScan(clean, scope, nil); err != nil {
		t.Fatalf("clean tree = %v, want nil", err)
	}
	hit := t.TempDir()
	writeFile(t, filepath.Join(hit, "src", "leak.env"), canary()+"\n", 0o600)
	var gotClass, gotPath string
	err := RunPreSpawnScan(hit, scope, func(c, p string) { gotClass, gotPath = c, p })
	if err != provider.ErrPreSpawnSecretFound { //nolint:errorlint // identity: errors.Is compares Kind only
		t.Fatalf("hit = %v, want ErrPreSpawnSecretFound by identity", err)
	}
	if gotClass == "" || gotPath != "src" || strings.Contains(gotClass+gotPath, "sk-") {
		t.Fatalf("journal got class %q path %q, want a class name and the prefix only", gotClass, gotPath)
	}
	broken := t.TempDir()
	writeFile(t, filepath.Join(broken, "src", "a.go"), "a", 0o600)
	symlinkOrSkip(t, filepath.Join(broken, "missing"), filepath.Join(broken, "src", "dangling"))
	err = RunPreSpawnScan(broken, scope, nil)
	if !cascade.HasKind(err, cascade.KindCapabilityDenied) || !strings.Contains(err.Error(), "could not finish") {
		t.Fatalf("scan error = %v, want a fail-closed refusal (KindCapabilityDenied, could not finish)", err)
	}
}

func TestShimPreSpawnScanUsesProcessDetector(t *testing.T) {
	f := newShimFix(t)
	writeFile(t, filepath.Join(f.root, "src", "leak.env"), canary()+"\n", 0o600)
	_, err := f.shim.Spawn(context.Background(), f.req)
	f.refused(err)
	if err != provider.ErrPreSpawnSecretFound { //nolint:errorlint // identity: errors.Is compares Kind only
		t.Fatalf("Spawn = %v, want ErrPreSpawnSecretFound by identity", err)
	}
}

func TestShimRefusesUnlistedExecutable(t *testing.T) {
	for name, exe := range map[string]string{
		"other path":              filepath.Join(t.TempDir(), "evil"),
		"same basename elsewhere": filepath.Join(t.TempDir(), "claude"),
		"empty":                   "",
		"relative":                "claude",
	} {
		t.Run(name, func(t *testing.T) {
			f := newShimFix(t)
			f.drv.exe = exe
			_, err := f.shim.Spawn(context.Background(), f.req)
			f.refused(err)
			if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "executable") {
				t.Fatalf("Spawn = %v, want KindPolicyDenied naming the executable", err)
			}
		})
	}
}

// noReporter is a driver that cannot report its executable.
type noReporter struct{ provider.AgentProvider }

func TestShimRefusesDriverWithoutExecutableReporter(t *testing.T) {
	f := newShimFix(t)
	f.useFactory(func(SpawnSite) (provider.AgentProvider, error) { return noReporter{}, nil })
	f.build()
	_, err := f.shim.Spawn(context.Background(), f.req)
	f.refused(err)
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "does not report its executable") {
		t.Fatalf("Spawn = %v, want KindPolicyDenied: does not report its executable", err)
	}
}

func TestShimSpawnOrderRefusesBeforeChild(t *testing.T) {
	cases := map[string]func(f *shimFix){
		"unknown lane":     func(f *shimFix) { f.req.Lane = "agent:nope" },
		"expired envelope": func(f *shimFix) { f.req.Job.Envelope.ExpiresAt = shimInstant.Add(-time.Second) },
		"root not covered": func(f *shimFix) { f.req.Job.Envelope.Roots = []string{"/elsewhere"} },
		"wider tool policy": func(f *shimFix) {
			f.req.Job.ToolPolicy = provider.ToolPolicyAll
		},
		"empty lease marker": func(f *shimFix) { f.req.LeaseMarker = "" },
		"out of scope file":  func(f *shimFix) { writeFile(t, filepath.Join(f.root, "stray.txt"), "s", 0o600) },
		"secret in tree":     func(f *shimFix) { writeFile(t, filepath.Join(f.root, "src", "k.env"), canary(), 0o600) },
		"unlisted exe":       func(f *shimFix) { f.drv.exe = "/not/the/binary" },
		"key missing":        func(f *shimFix) { delete(f.env, shimKeyEnv) },
		"no vendor binary":   func(f *shimFix) { f.deps.Binaries = map[HarnessID]string{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newShimFix(t)
			mutate(f)
			f.build()
			_, err := f.shim.Spawn(context.Background(), f.req)
			f.refused(err)
			if pend, perr := f.el.PendingEffects(context.Background()); perr != nil || len(pend) != 0 {
				t.Fatalf("pending effects after a refusal = %v, %v; want none", pend, perr)
			}
			if _, found, serr := f.el.EffectState(context.Background(), "agent.spawn:job-fix-1"); serr != nil || found {
				t.Fatalf("effect state found=%v err=%v; a refusal before the intent writes no record", found, serr)
			}
		})
	}
}
