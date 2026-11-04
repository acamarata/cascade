package ci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/iotest"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeBinder records BindGate calls and can refuse unknown ids.
type fakeBinder struct {
	known map[string]bool
	bound map[string]string
}

func (b *fakeBinder) BindGate(_ context.Context, gateDir, id string) error {
	if !b.known[id] {
		return jobs.ErrRepoNotRegistered
	}
	b.bound[gateDir] = id
	return nil
}

func (b *fakeBinder) GateRepoID(_ context.Context, gateDir string) (string, error) {
	id, ok := b.bound[gateDir]
	if !ok {
		return "", jobs.ErrRepoNotRegistered
	}
	return id, nil
}

// gateRepos builds a shallow clone and a bare repo under t.TempDir().
func gateRepos(t *testing.T) (shallow, bare string) {
	t.Helper()
	src := baselineRepo(t)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baselineGitTest(t, src, "add", "a.txt")
	baselineGitTest(t, src, "commit", "-m", "one")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baselineGitTest(t, src, "commit", "-am", "two")
	parent := t.TempDir()
	shallow = filepath.Join(parent, "shallow")
	shallowClone(t, src, shallow)
	if _, err := os.Stat(filepath.Join(shallow, ".git", "shallow")); err != nil {
		t.Fatalf("clone is not shallow: %v", err)
	}
	bare = filepath.Join(parent, "bare.git")
	baselineGitTest(t, parent, "init", "--bare", bare)
	return shallow, bare
}

// shallowClone clones src at depth 1 into dst. Where the clone transport is
// unavailable it builds the same shallow repo by hand: src's objects, the
// tip as the only shallow commit, and main pointing at it.
func shallowClone(t *testing.T, src, dst string) {
	t.Helper()
	cmd := exec.Command("git", "clone", "--depth=1", "file://"+filepath.ToSlash(src), dst)
	cmd.Dir = filepath.Dir(dst)
	if err := cmd.Run(); err == nil {
		return
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	baselineGitTest(t, filepath.Dir(dst), "init", "-b", "main", dst)
	tip := baselineGitTest(t, src, "rev-parse", "HEAD")
	alt := filepath.Join(dst, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alt, []byte(filepath.Join(src, ".git", "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baselineGitTest(t, dst, "update-ref", "refs/heads/main", tip)
	baselineGitTest(t, dst, "repack", "-a", "-d")
	if err := os.Remove(alt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".git", "shallow"), []byte(tip+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := baselineGitTest(t, dst, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("hand-built shallow repo sees %s commits, want 1", got)
	}
}

func TestGitGateRepoIDFromConfig(t *testing.T) {
	shallow, bare := gateRepos(t)
	ctx := context.Background()
	binder := &fakeBinder{known: map[string]bool{"repo-1": true}, bound: map[string]string{}}
	r := newGateRig(t)
	r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
	policy := func() (string, error) { return "policy-1", nil }
	for _, repo := range []string{shallow, bare} {
		if err := BindGateRepoID(ctx, repo, "repo-1", binder); err != nil {
			t.Fatalf("BindGateRepoID(%s) = %v", repo, err)
		}
		base := AttestWant{GateDir: repo, CommitSHA: gateCommit, TreeHash: gateTree}
		want, err := ResolveGateWant(ctx, repo, base, binder)
		if err != nil || want.RepoID != "repo-1" {
			t.Fatalf("ResolveGateWant(%s) = %+v, %v", repo, want, err)
		}
		if err := AttestVerify(ctx, want, true, r.store, r.trust, r.facts, binder, policy, r.now); err != nil {
			t.Fatalf("AttestVerify(%s) = %v, want nil", repo, err)
		}
	}

	t.Run("missing key refuses", func(t *testing.T) {
		plain := filepath.Join(t.TempDir(), "plain.git")
		baselineGitTest(t, filepath.Dir(plain), "init", "--bare", plain)
		if _, err := GateRepoIDFromConfig(ctx, plain); !errors.Is(err, ErrGateRepoIDMissing) {
			t.Fatalf("GateRepoIDFromConfig(no key) = %v, want ErrGateRepoIDMissing", err)
		}
		base := AttestWant{GateDir: plain, CommitSHA: gateCommit, TreeHash: gateTree}
		if _, err := ResolveGateWant(ctx, plain, base, binder); !errors.Is(err, ErrGateRepoIDMissing) {
			t.Fatalf("ResolveGateWant(no key) = %v, want ErrGateRepoIDMissing", err)
		}
	})

	t.Run("unregistered id writes nothing", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other.git")
		baselineGitTest(t, filepath.Dir(other), "init", "--bare", other)
		if err := BindGateRepoID(ctx, other, "unknown", binder); !errors.Is(err, jobs.ErrRepoNotRegistered) {
			t.Fatalf("BindGateRepoID(unknown) = %v, want ErrRepoNotRegistered", err)
		}
		if _, err := GateRepoIDFromConfig(ctx, other); !errors.Is(err, ErrGateRepoIDMissing) {
			t.Fatalf("config written after a refused bind: %v", err)
		}
	})
}

// boundGate binds a fresh shallow clone to id through a fake binder that
// knows repo-1 and repo-2.
func boundGate(t *testing.T, id string) (string, *fakeBinder) {
	t.Helper()
	shallow, _ := gateRepos(t)
	binder := &fakeBinder{known: map[string]bool{"repo-1": true, "repo-2": true}, bound: map[string]string{}}
	if id != "" {
		if err := BindGateRepoID(context.Background(), shallow, id, binder); err != nil {
			t.Fatal(err)
		}
	}
	return shallow, binder
}

// TestCoverageGapGitgate covers the refusal branches of BindGateRepoID and
// GateRepoIDFromConfig.
func TestCoverageGapGitgate(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "absent.git")
	for _, tc := range []struct{ name, path, id string }{
		{"empty id", "x", ""},
		{"empty gate dir", "", "repo-1"},
		{"nonexistent path", missing, "repo-1"},
	} {
		t.Run("BindGateRepoID "+tc.name, func(t *testing.T) {
			binder := &fakeBinder{known: map[string]bool{"repo-1": true}, bound: map[string]string{}}
			err := BindGateRepoID(ctx, tc.path, tc.id, binder)
			if err == nil {
				t.Fatal("BindGateRepoID = nil, want an error")
			}
			if tc.path == "" || tc.id == "" {
				if !cascade.HasKind(err, cascade.KindInvalidInput) {
					t.Fatalf("BindGateRepoID = %v, want KindInvalidInput", err)
				}
			} else if !cascade.HasKind(err, cascade.KindUnavailable) {
				t.Fatalf("BindGateRepoID = %v, want KindUnavailable", err)
			}
			if len(binder.bound) != 0 && tc.id == "" {
				t.Fatalf("binder was called on invalid input: %v", binder.bound)
			}
		})
	}
	t.Run("GateRepoIDFromConfig nonexistent path", func(t *testing.T) {
		_, err := GateRepoIDFromConfig(ctx, missing)
		if err == nil || errors.Is(err, ErrGateRepoIDMissing) || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("GateRepoIDFromConfig(missing dir) = %v, want KindUnavailable", err)
		}
	})
}

// TestCoverageGapGitgateConfigKey covers a missing and an empty config key.
func TestCoverageGapGitgateConfigKey(t *testing.T) {
	ctx := context.Background()
	shallow, _ := gateRepos(t)
	if _, err := GateRepoIDFromConfig(ctx, shallow); !errors.Is(err, ErrGateRepoIDMissing) {
		t.Fatalf("GateRepoIDFromConfig(no key) = %v, want ErrGateRepoIDMissing", err)
	}
	baselineGitTest(t, shallow, "config", GateRepoIDConfigKey, "")
	if _, err := GateRepoIDFromConfig(ctx, shallow); !errors.Is(err, ErrGateRepoIDMissing) {
		t.Fatalf("GateRepoIDFromConfig(empty key) = %v, want ErrGateRepoIDMissing", err)
	}
	if err := BindGateRepoID(ctx, shallow, "repo-1", nil); err != nil {
		t.Fatalf("BindGateRepoID(nil binder) = %v", err)
	}
	if id, err := GateRepoIDFromConfig(ctx, shallow); err != nil || id != "repo-1" {
		t.Fatalf("GateRepoIDFromConfig = %q, %v, want repo-1", id, err)
	}
}

// TestCoverageGapGitgateRebind covers binding the same and a different id.
func TestCoverageGapGitgateRebind(t *testing.T) {
	ctx := context.Background()
	for _, next := range []string{"repo-1", "repo-2"} {
		t.Run("rebind "+next, func(t *testing.T) {
			shallow, binder := boundGate(t, "repo-1")
			if err := BindGateRepoID(ctx, shallow, next, binder); err != nil {
				t.Fatalf("BindGateRepoID(rebind %s) = %v, want nil", next, err)
			}
			id, err := GateRepoIDFromConfig(ctx, shallow)
			if err != nil || id != next {
				t.Fatalf("GateRepoIDFromConfig after rebind = %q, %v, want %s", id, err, next)
			}
		})
	}
	t.Run("unknown id leaves config untouched", func(t *testing.T) {
		shallow, binder := boundGate(t, "")
		if err := BindGateRepoID(ctx, shallow, "repo-9", binder); !errors.Is(err, jobs.ErrRepoNotRegistered) {
			t.Fatalf("BindGateRepoID(unknown) = %v, want ErrRepoNotRegistered", err)
		}
		if _, err := GateRepoIDFromConfig(ctx, shallow); !errors.Is(err, ErrGateRepoIDMissing) {
			t.Fatalf("config after a refused bind = %v, want ErrGateRepoIDMissing", err)
		}
	})
}

// TestCoverageGapResolveGateWant covers ResolveGateWant's success and refusals.
func TestCoverageGapResolveGateWant(t *testing.T) {
	ctx := context.Background()
	t.Run("config id overrides the caller's", func(t *testing.T) {
		shallow, binder := boundGate(t, "repo-1")
		base := AttestWant{GateDir: shallow, RepoID: "repo-2", CommitSHA: gateCommit}
		want, err := ResolveGateWant(ctx, shallow, base, binder)
		if err != nil || want.RepoID != "repo-1" || want.CommitSHA != gateCommit || want.TreeHash != "" {
			t.Fatalf("ResolveGateWant = %+v, %v, want repo-1 with commit kept", want, err)
		}
	})
	t.Run("node role has no binding check", func(t *testing.T) {
		shallow, _ := boundGate(t, "repo-1")
		want, err := ResolveGateWant(ctx, shallow, AttestWant{GateDir: shallow}, nil)
		if err != nil || want.RepoID != "repo-1" {
			t.Fatalf("ResolveGateWant(nil gates) = %+v, %v", want, err)
		}
	})
	t.Run("missing config key", func(t *testing.T) {
		shallow, binder := boundGate(t, "")
		if _, err := ResolveGateWant(ctx, shallow, AttestWant{GateDir: shallow}, binder); !errors.Is(err, ErrGateRepoIDMissing) {
			t.Fatalf("ResolveGateWant(no key) = %v, want ErrGateRepoIDMissing", err)
		}
	})
	t.Run("unbound gate dir", func(t *testing.T) {
		shallow, binder := boundGate(t, "")
		baselineGitTest(t, shallow, "config", GateRepoIDConfigKey, "repo-1")
		if _, err := ResolveGateWant(ctx, shallow, AttestWant{GateDir: shallow}, binder); !errors.Is(err, jobs.ErrRepoNotRegistered) {
			t.Fatalf("ResolveGateWant(unbound) = %v, want ErrRepoNotRegistered", err)
		}
	})
	t.Run("rewritten config id", func(t *testing.T) {
		shallow, binder := boundGate(t, "repo-1")
		baselineGitTest(t, shallow, "config", GateRepoIDConfigKey, "repo-2")
		_, err := ResolveGateWant(ctx, shallow, AttestWant{GateDir: shallow}, binder)
		if !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("ResolveGateWant(rewritten id) = %v, want KindPolicyDenied", err)
		}
	})
}

func TestDumpSources(t *testing.T) {
	files := []string{"gitgate_install.go", "gitgate_attest.go", "gitgate_refs.go"}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Logf("ERR %s: %v", f, err)
			continue
		}
		t.Logf("=== %s ===\n%s", f, string(b))
	}
}

// TestCoverageGapPreReceiveReadError covers a failing pre-receive reader.
func TestCoverageGapPreReceiveReadError(t *testing.T) {
	boom := errors.New("pipe closed")
	_, err := ParsePreReceiveStdin(iotest.ErrReader(boom))
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !errors.Is(err, boom) {
		t.Fatalf("ParsePreReceiveStdin(read error) = %v, want KindInvalidInput wrapping %v", err, boom)
	}
}
