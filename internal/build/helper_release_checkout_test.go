// helper_release_checkout_test.go covers what binds the helper build to its
// source: the script's own tree must be a clean git checkout at the release
// commit with a real .git, git location overrides never reach git or go, the
// go env file and module source cannot change the build, the output lies
// outside the tree, and submit/finish refuse executables signed in another
// mode. The fake clone (hrRepo) holds copies of the release scripts.
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hrRepo makes a fake clone with a .git directory and copies of the release
// scripts and the helper entitlements, so a test can shape the checkout.
func hrRepo(f *hrFx) string {
	repo := filepath.Join(f.dir, "repo")
	for _, rel := range []string{".github/scripts/assemble-rc-dist.sh", ".github/scripts/build-elevate-helper.sh",
		".github/scripts/notary-lib.sh", "apps/elevate-helper/entitlements.plist"} {
		dst := filepath.Join(repo, filepath.FromSlash(rel))
		ishMust(f.t, os.MkdirAll(filepath.Dir(dst), 0o755))
		ishMust(f.t, os.WriteFile(dst, []byte(ishRead(f.t, filepath.Join(f.root, filepath.FromSlash(rel)))), 0o755))
	}
	ishMust(f.t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	return repo
}

// hrHelper runs build-elevate-helper.sh of the fake clone from the fixture
// directory and fails unless it exits with code.
func (f *hrFx) hrHelper(code int, envs ...string) string {
	f.t.Helper()
	c := exec.Command("bash", filepath.Join(f.repo, ".github", "scripts", "build-elevate-helper.sh"))
	c.Dir, c.Env = f.dir, append(append([]string{}, f.env...), envs...)
	f.writeShimEnv(c.Env)
	b, _ := c.CombinedOutput() // the exit code below is the verdict
	if got := c.ProcessState.ExitCode(); got != code {
		f.t.Fatalf("build-elevate-helper %v: exit %d, want %d\n%s", envs, got, code, b)
	}
	return string(b)
}

// writeShimEnv writes the SHIM_* settings of env to bin/shim.env, where a
// shim run in an empty environment (go under env -i) reads them.
func (f *hrFx) writeShimEnv(env []string) {
	var b strings.Builder
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "SHIM_") {
			b.WriteString("export " + k + "='" + strings.ReplaceAll(v, "'", `'\''`) + "'\n")
		}
	}
	ishMust(f.t, os.WriteFile(filepath.Join(f.dir, "bin", "shim.env"), []byte(b.String()), 0o600))
}

func TestHelperCheckoutBinding(t *testing.T) {
	for _, c := range []struct {
		name, env, msg string
		shape          func(f *hrFx)
	}{
		{"HEAD elsewhere", "SHIM_GIT_HEAD=2222222222222222222222222222222222222222", "is not at commit", nil},
		{"untracked file", "SHIM_GIT_STATUS=?? stray.go", "is not clean", nil},
		{"nested in another checkout", "SHIM_GIT_TOPLEVEL=/example/outer", "is not the top level", nil},
		{"no .git", "", "has no .git of its own", func(f *hrFx) { ishMust(f.t, os.RemoveAll(filepath.Join(f.repo, ".git"))) }},
		{"foreign gitfile", "", "is not a worktree entry", hrForeignGitfile},
		{"no -trimpath", "SHIM_NOTRIM=1", "is not a -trimpath build", nil},
		{"env file flags", "", "the go env file sets CGO_LDFLAGS GOFLAGS", hrGoEnvFile("CGO_LDFLAGS=-fake\nGOFLAGS=-tags=devkeys\n")},
		{"env file toolchain", "", "the go env file sets GOTOOLCHAIN", hrGoEnvFile("GOTOOLCHAIN=go1.0.0\n")},
		{"file proxy", "SHIM_GOPROXY=https://proxy.golang.org|file:///example/mods", "unauthenticated source", nil},
		{"http proxy", "SHIM_GOPROXY=http://example.invalid", "unauthenticated source", nil},
		{"sumdb off", "SHIM_GOSUMDB=off", "GOSUMDB is off", nil},
		{"go.work", "SHIM_GOWORK=/example/go.work", "a go.work file is in use", nil},
		{"git status fails", "SHIM_GIT_STATUS_FAIL=1", "git status failed", nil},
		{"upper-case http proxy", "SHIM_GOPROXY=HTTP://example.invalid", "unauthenticated source", nil},
		{"skip-worktree entry", "SHIM_GIT_LSV=H go.mod\nS cmd/cascade-elevate-helper/main.go", "assume-unchanged or skip-worktree", nil},
		{"assume-unchanged entry", "SHIM_GIT_LSV=h cmd/cascade-elevate-helper/main.go", "assume-unchanged or skip-worktree", nil},
		{"excluded source file", "SHIM_GIT_IGNORED=.cache/x.go\ncmd/cascade-elevate-helper/extra.go\n", "ignore rules hide Go build inputs", nil},
	} {
		f := hrNew(t)
		_, key := f.key()
		if c.shape != nil {
			c.shape(f)
		}
		hrWant(t, c.name, f.assemble(false, key, c.env), c.msg)
		hrNoOut(f, c.name)
		if f.calls("codesign", "--force") != 0 {
			t.Errorf("%s: signing ran before the refusal", c.name)
		}
	}
}

func hrForeignGitfile(f *hrFx) {
	entry := filepath.Join(f.dir, "other-common", "worktrees", "x")
	ishMust(f.t, os.MkdirAll(entry, 0o755))
	ishMust(f.t, os.WriteFile(filepath.Join(entry, "gitdir"), []byte("/example/elsewhere/.git\n"), 0o644))
	ishMust(f.t, os.RemoveAll(filepath.Join(f.repo, ".git")))
	ishMust(f.t, os.WriteFile(filepath.Join(f.repo, ".git"), []byte("gitdir: "+entry+"\n"), 0o644))
}

func hrGoEnvFile(body string) func(f *hrFx) {
	return func(f *hrFx) {
		p := filepath.Join(f.dir, "goenv")
		ishMust(f.t, os.WriteFile(p, []byte(body), 0o600))
		f.env = append(f.env, "SHIM_GOENV="+p)
	}
}

func TestHelperBuildIgnoresGitOverrides(t *testing.T) {
	f := hrNew(t)
	_, key := f.key()
	entry := filepath.Join(f.dir, "common", "worktrees", "repo")
	ishMust(t, os.MkdirAll(entry, 0o755))
	ishMust(t, os.WriteFile(filepath.Join(entry, "gitdir"), []byte(filepath.Join(f.repo, ".git")+"\n"), 0o644))
	ishMust(t, os.RemoveAll(filepath.Join(f.repo, ".git")))
	ishMust(t, os.WriteFile(filepath.Join(f.repo, ".git"), []byte("gitdir: "+entry+"\n"), 0o644)) // a valid linked worktree
	f.assemble(true, key, "GIT_DIR=/example/other/.git", "GIT_WORK_TREE=/example/other")
	b, _ := os.ReadFile(f.shLog)
	if strings.Count(string(b), "git-env GIT_DIR=[] GIT_WORK_TREE=[]") < 3 || strings.Contains(string(b), "/example/other") {
		t.Errorf("git location overrides reached git or go:\n%s", b)
	}
	if f.calls("go-build-env", "GIT_DIR=[]") != 2 {
		t.Error("the helper build saw the caller's GIT_DIR")
	}
}

func TestHelperBuildOutputPath(t *testing.T) {
	f := hrNew(t)
	f.hrHelper(0, "CASCADE_HELPER_OUT=rel-helpers") // relative to the cwd; revision defaults to HEAD
	if _, err := os.Stat(filepath.Join(f.dir, "rel-helpers", "darwin_amd64", "cascade-elevate-helper")); err != nil {
		t.Fatalf("relative CASCADE_HELPER_OUT: %v", err)
	}
	inside := filepath.Join(f.repo, "helpers")
	hrWant(t, "inside the tree", f.hrHelper(1, "CASCADE_HELPER_OUT="+inside), "is inside the source tree")
	if _, err := os.Lstat(inside); !os.IsNotExist(err) {
		t.Errorf("an output inside the source tree was created (%v)", err)
	}
}

func TestAssembleSignModeBinding(t *testing.T) {
	f := hrNew(t)
	_, key := f.key()
	f.script("sign", 0, key) // ad-hoc and skip mode
	realEnv := append(hrReal(f), key)
	hrWant(t, "real submit after ad-hoc sign", f.script("submit", 1, realEnv...), "signed with another identity or notarization mode")
	hrWant(t, "real finish after ad-hoc sign", f.script("finish", 1, realEnv...), "signed with another identity or notarization mode")
	if f.calls("notarytool", "") != 0 {
		t.Error("ad-hoc executables were submitted for notarization")
	}
	hrNoOut(f, "mode mismatch")
}

func TestHelperBuildEnvAllowList(t *testing.T) {
	f := hrNew(t)
	_, key := f.key()
	hostile := []string{"CCC_OVERRIDE_OPTIONS=#^-fake", "GOTOOLCHAIN=go1.0.0", "GONOSUMDB=*", "GOINSECURE=*", "GOAMD64=v4",
		"SDKROOT=/example/evil", "CPATH=/example/evil", "GOPROXY=https://example.invalid"}
	f.assemble(true, append(hostile, key, "SHIM_GIT_IGNORED=.cache/x.go\ninternal/build/testdata/y.go\n")...)
	b, _ := os.ReadFile(f.shLog)
	for _, kv := range hostile {
		if name, _, _ := strings.Cut(kv, "="); strings.Contains(string(b), "go-build-var "+name+"\n") {
			t.Errorf("%s reached the helper build", name)
		}
	}
	if f.calls("go-build-var", "GIT_NO_REPLACE_OBJECTS") != 2 || f.calls("git-env", "NOREPLACE=[1]") < 3 || f.calls("git-env", "NOREPLACE=[]") != 0 {
		t.Errorf("replace objects are not disabled for git and go:\n%s", b)
	}
}

func TestAssembleSignIdentityBinding(t *testing.T) {
	a, b := "CASCADE_APPLE_SIGNING_IDENTITY=Developer ID Application: Example A (FAKETEAMAA)", "CASCADE_APPLE_SIGNING_IDENTITY=-"
	for _, c := range [][2]string{{a, b}, {b, a}, {a, "CASCADE_APPLE_SIGNING_IDENTITY=Developer ID Application: Example B (FAKETEAMBB)"}} {
		f := hrNew(t)
		_, key := f.key()
		f.script("sign", 0, key, c[0]) // skip mode both times: only the identity differs
		hrWant(t, c[1]+" after "+c[0], f.script("submit", 1, key, c[1]), "signed with another identity or notarization mode")
		hrWant(t, c[1]+" after "+c[0], f.script("finish", 1, key, c[1]), "signed with another identity or notarization mode")
		hrNoOut(f, "identity mismatch")
		f.script("submit", 0, key, c[0]) // the signing identity itself still passes
		f.script("finish", 0, key, c[0])
	}
}
