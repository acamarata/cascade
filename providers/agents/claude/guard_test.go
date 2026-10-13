//go:build !windows

package claude

// Purpose: the tree guards (runtime home overlap, symlinks under .claude at
//   any depth, symlinks leaving the tree, the O_NOFOLLOW hook-pack copy) and
//   the hardened git status (the confirm's probes 05/05b/07/07b as tests).
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   guard.go and artifacts.go.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestTreeGuardsRefuseBeforeAnyChild(t *testing.T) {
	cases := map[string]func(f *fx){
		"symlinked .claude": func(f *fx) { mustSymlink(t, f.dir, filepath.Join(f.root, ".claude")) },
		"symlinked .claude/hooks": func(f *fx) {
			mustMkdir(t, filepath.Join(f.root, ".claude"))
			mustSymlink(t, f.dir, filepath.Join(f.root, ".claude", "hooks"))
		},
		"in-tree symlink under .claude": func(f *fx) {
			mustMkdir(t, filepath.Join(f.root, ".claude"))
			mustSymlink(t, "x", filepath.Join(f.root, ".claude", "l"))
		},
		"link to the runtime home": func(f *fx) { mustSymlink(t, f.home, filepath.Join(f.root, "h")) },
		"relative link out":        func(f *fx) { mustSymlink(t, "../../..", filepath.Join(f.root, "up")) },
		"dangling link out":        func(f *fx) { mustSymlink(t, "/nonexistent/x", filepath.Join(f.root, "d")) },
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			plant(f)
			_, err := f.driver(f.config()).Spawn(context.Background(), f.spec())
			if !errors.Is(err, ErrWorktreeEscape) {
				t.Fatalf("Spawn = %v, want ErrWorktreeEscape", err)
			}
			if exists(f.anyRan) || exists(filepath.Join(f.dir, "settings.json")) || exists(filepath.Join(f.dir, "h.sh")) {
				t.Fatal("a child ran or a hook-pack file was written outside the tree")
			}
		})
	}
	t.Run("in-tree link is fine", func(t *testing.T) {
		f := newFx(t)
		mustWrite(t, filepath.Join(f.root, "a.txt"), "x")
		mustSymlink(t, "a.txt", filepath.Join(f.root, "b.txt"))
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatalf("an in-tree symlink was refused: %v", err)
		}
	})
	t.Run("home inside the tree", func(t *testing.T) {
		f := newFx(t)
		home := filepath.Join(f.root, "home")
		mustMkdir(t, filepath.Join(home, "v"))
		spec, prof := f.spec(), f.profile()
		spec.Env.Home, prof.RuntimeHome = home, home
		spec.Env.Driver = []string{"HOME=" + home, VendorConfigDirVar + "=" + filepath.Join(home, "v"), KeyVarOAuthToken + "=" + fakeKeyValue}
		cfg := f.config()
		cfg.Profile = fakeProfile{prof: prof}
		if _, err := f.driver(cfg).Spawn(context.Background(), spec); !errors.Is(err, ErrWorktreeEscape) || exists(f.anyRan) {
			t.Fatalf("Spawn = %v: the runtime home must not be tool-visible", err)
		}
	})
}

func TestLeaseAndScopeValidation(t *testing.T) {
	for name, mut := range map[string]func(f *fx, c *Config, s *provider.AgentJobSpec){
		"relative root": func(_ *fx, c *Config, _ *provider.AgentJobSpec) {
			c.Worktree = fakeLeaser{lease: Lease{Root: "r", GitDir: "/g"}}
		},
		"missing root": func(_ *fx, c *Config, _ *provider.AgentJobSpec) {
			c.Worktree = fakeLeaser{lease: Lease{Root: "/no/such", GitDir: "/g"}}
		},
		"lease error": func(_ *fx, c *Config, _ *provider.AgentJobSpec) {
			c.Worktree = fakeLeaser{err: provider.ErrJobNotFound}
		},
		"escaping prefix": func(_ *fx, _ *Config, s *provider.AgentJobSpec) { s.Scope.InScopePrefixes = []string{"../x"} },
		"absolute prefix": func(_ *fx, _ *Config, s *provider.AgentJobSpec) { s.Scope.InScopePrefixes = []string{"/etc"} },
		"unknown credential class": func(f *fx, c *Config, _ *provider.AgentJobSpec) {
			p := f.profile()
			p.CredentialClass = "team"
			c.Profile = fakeProfile{prof: p}
		},
		"hook pack is bad": func(f *fx, c *Config, _ *provider.AgentJobSpec) { c.HookPackPath = filepath.Join(f.dir, "nope") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			cfg, spec := f.config(), f.spec()
			mut(f, &cfg, &spec)
			if _, err := f.driver(cfg).Spawn(context.Background(), spec); err == nil || exists(f.anyRan) {
				t.Fatalf("Spawn = %v anyRan=%v, want a refusal before any child", err, exists(f.anyRan))
			}
		})
	}
}

func TestCopyHookPackRefusesSymlinkTargets(t *testing.T) {
	f := newFx(t)
	claudeDir := filepath.Join(f.root, ".claude")
	mustMkdir(t, filepath.Join(claudeDir, "hooks"))
	outside := filepath.Join(f.dir, "outside.sh")
	mustWrite(t, outside, "original")
	mustSymlink(t, outside, filepath.Join(claudeDir, "hooks", "h.sh"))
	if _, err := copyHookPack(f.pack, claudeDir); !errors.Is(err, ErrWorktreeEscape) {
		t.Fatalf("copy over a symlinked file = %v", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "original" {
		t.Fatal("the copy wrote through a symlink")
	}
	written, err := copyHookPack(f.pack, filepath.Join(newGitWorktree(t), ".claude"))
	if err != nil || !reflect.DeepEqual(written, []string{".claude/hooks/h.sh", ".claude/settings.json"}) {
		t.Fatalf("written %v, %v", written, err)
	}
	if w, err := copyHookPack("", claudeDir); w != nil || err != nil {
		t.Fatal("an empty pack path must be a no-op")
	}
}

func TestGitStatusRunsNoConfigDrivenCode(t *testing.T) {
	ctx := context.Background()
	t.Run("fsmonitor", func(t *testing.T) {
		root, dir := newGitWorktree(t), t.TempDir()
		marker := filepath.Join(dir, "fsmon-ran")
		hook := filepath.Join(dir, "fsmon")
		mustWrite(t, hook, "#!/bin/sh\ntouch "+marker+"\n")
		_ = os.Chmod(hook, 0o700)
		gitIn(t, root, "config", "core.fsmonitor", hook)
		mustWrite(t, filepath.Join(root, "a.txt"), "x")
		paths, _, err := gitModified(ctx, Lease{Root: root, GitDir: filepath.Join(root, ".git")})
		if err != nil || exists(marker) || !reflect.DeepEqual(paths, []string{"a.txt"}) {
			t.Fatalf("paths %v err %v fsmonitorRan %v", paths, err, exists(marker))
		}
	})
	t.Run("filter drivers", gitFilterDriversRefused)
	t.Run("rename entries", func(t *testing.T) {
		root := newGitWorktree(t)
		mustWrite(t, filepath.Join(root, "old.txt"), "same\n")
		gitIn(t, root, "add", "old.txt")
		gitIn(t, root, "commit", "-qm", "c")
		gitIn(t, root, "mv", "old.txt", "new.txt")
		paths, _, err := gitModified(ctx, Lease{Root: root, GitDir: filepath.Join(root, ".git")})
		if err != nil || !reflect.DeepEqual(paths, []string{"new.txt"}) {
			t.Fatalf("paths %v err %v", paths, err)
		}
	})
	t.Run("broken git dir", func(t *testing.T) {
		root := t.TempDir()
		if _, _, err := gitModified(ctx, Lease{Root: root, GitDir: filepath.Join(root, "no-git")}); err == nil {
			t.Fatal("git status over a missing git dir succeeded")
		}
	})
}

func TestGuardEdgeCases(t *testing.T) {
	t.Run("link resolving out through an in-tree hop", func(t *testing.T) {
		f := newFx(t)
		mustSymlink(t, f.dir, filepath.Join(f.root, "z-out"))
		mustSymlink(t, "z-out", filepath.Join(f.root, "a-hop"))
		if err := guardTree(f.root, f.home); !errors.Is(err, ErrWorktreeEscape) {
			t.Fatalf("guardTree = %v", err)
		}
	})
	t.Run("unreadable directory in the tree", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every directory")
		}
		f := newFx(t)
		locked := filepath.Join(f.root, "locked")
		mustMkdir(t, locked)
		_ = os.Chmod(locked, 0)
		defer func() { _ = os.Chmod(locked, 0o700) }()
		if err := guardTree(f.root, f.home); err == nil {
			t.Fatal("an unwalkable tree passed the guard")
		}
	})
	t.Run("symlinked runtime home", func(t *testing.T) {
		f := newFx(t)
		link := filepath.Join(f.dir, "home-link")
		mustSymlink(t, f.home, link)
		if err := checkRuntimeHome(link); !errors.Is(err, ErrSpawnEnvRejected) {
			t.Fatalf("checkRuntimeHome(symlink) = %v", err)
		}
		if err := checkRuntimeHome(filepath.Join(f.dir, "missing")); err == nil {
			t.Fatal("a missing runtime home passed")
		}
	})
	t.Run("commented filter attribute", func(t *testing.T) {
		root := newGitWorktree(t)
		mustWrite(t, filepath.Join(root, ".gitattributes"), "# *.bin filter=lfs\n*.txt text\n")
		if _, _, err := gitModified(context.Background(), Lease{Root: root, GitDir: filepath.Join(root, ".git")}); err != nil {
			t.Fatalf("a commented filter line refused: %v", err)
		}
	})
	t.Run("short socket base", func(t *testing.T) {
		t.Setenv("TMPDIR", "/"+strings.Repeat("d", 80))
		if shortTempBase() != "/tmp" {
			t.Fatal("a long TMPDIR must fall back to /tmp")
		}
	})
}

// gitFilterDriversRefused: a declared filter or process driver, or a filter
// attribute, refuses status before any filter can run (probe 07b).
func gitFilterDriversRefused(t *testing.T) {
	ctx := context.Background()
	for name, declare := range map[string]func(root, filt string){
		"config clean filter": func(root, filt string) {
			gitIn(t, root, "config", "filter.x.clean", filt)
			mustWrite(t, filepath.Join(root, ".gitattributes"), "b.txt filter=x\n")
		},
		"config process driver": func(root, filt string) { gitIn(t, root, "config", "filter.y.process", filt) },
		"attributes only":       func(root, _ string) { mustWrite(t, filepath.Join(root, ".gitattributes"), "*.bin filter=lfs\n") },
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := newGitWorktree(t), t.TempDir()
			marker := filepath.Join(dir, "filter-ran")
			filt := filepath.Join(dir, "filt")
			mustWrite(t, filt, "#!/bin/sh\ntouch "+marker+"\ncat\n")
			_ = os.Chmod(filt, 0o700)
			mustWrite(t, filepath.Join(root, "b.txt"), "one\n")
			gitIn(t, root, "add", "b.txt")
			gitIn(t, root, "commit", "-qm", "c")
			declare(root, filt)
			tick := time.Now().Add(2 * time.Second)
			_ = os.Chtimes(filepath.Join(root, "b.txt"), tick, tick)
			mustWrite(t, filepath.Join(root, "b.txt"), "two\n")
			_, _, err := gitModified(ctx, Lease{Root: root, GitDir: filepath.Join(root, ".git")})
			if !errors.Is(err, ErrGitExecDriver) || exists(marker) {
				t.Fatalf("err %v filterRan %v, want a refusal before any filter runs", err, exists(marker))
			}
		})
	}
}

// processRunnable reports whether pid is alive and not a zombie, using ps
// (the test's view only; the driver never needs this distinction).
func processRunnable(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && len(out) > 0 && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
