// helper_release_guard_test.go covers the guards of assemble-rc-dist.sh and
// build-elevate-helper.sh that a plain run never trips: notarization
// outcomes, finish ordering, post-sign integrity, rehearsal-only modes, the
// output lock and exclusive publish, relative paths, the helper's commit
// binding and build settings, and the syft pin. Fixtures and shims are the
// ones of helper_release_test.go.
package build

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// hrReal is a real-mode environment with obviously fake credentials.
func hrReal(f *hrFx) []string {
	key := filepath.Join(f.dir, "fake.p8")
	ishMust(f.t, os.WriteFile(key, []byte("fake key\n"), 0o600))
	return []string{"!CASCADE_NOTARY_SKIP", "CASCADE_APPLE_SIGNING_IDENTITY=Developer ID Application: Example (FAKETEAM00)",
		"CASCADE_APPLE_NOTARY_KEY_ID=FAKEKEYID0", "CASCADE_APPLE_NOTARY_KEY_ISSUER=fake-issuer", "CASCADE_APPLE_NOTARY_KEY_PATH=" + key}
}

// hrNoOut fails when CASCADE_DIST_OUT exists.
func hrNoOut(f *hrFx, leg string) {
	f.t.Helper()
	if _, err := os.Lstat(f.out); !os.IsNotExist(err) {
		f.t.Errorf("%s: CASCADE_DIST_OUT was written (%v)", leg, err)
	}
}

func hrWant(t *testing.T, leg, out, msg string) {
	t.Helper()
	if !strings.Contains(out, msg) {
		t.Errorf("%s: want %q in:\n%s", leg, msg, out)
	}
}

func TestAssembleNotaryGuards(t *testing.T) {
	f := hrNew(t)
	_, key := f.key()
	realEnv := append(hrReal(f), key)
	hrWant(t, "Invalid", f.script("all", 1, append(realEnv, "SHIM_STATUS=Invalid")...), "notarization 0f0e0d0c-0b0a-4908-8706-050403020100: Invalid")
	hrNoOut(f, "Invalid")
	hrWant(t, "In Progress", f.script("all", 75, append(realEnv, "SHIM_STATUS=In Progress")...), "In Progress (run finish again)")
	hrNoOut(f, "In Progress")
	f.script("finish", 0, realEnv...) // the same submission, now Accepted
	ishMust(t, os.RemoveAll(f.out))
	f.script("sign", 0, realEnv...)
	hrWant(t, "real finish without submit", f.script("finish", 1, realEnv...), "no submission confirmed")
	f.script("sign", 0, key)
	hrWant(t, "skip finish without submit", f.script("finish", 1, key), "needs a skip-mode submit first")
	f.script("submit", 0, key)
	ishMust(t, os.WriteFile(filepath.Join(f.out+".work", "stage", "darwin_arm64", "cascade"), []byte("swapped after signing\n"), 0o755))
	hrWant(t, "changed after signing", f.script("finish", 1, key), "a signed executable changed after signing")
	hrNoOut(f, "changed after signing")
	g := hrNew(t)
	_, key = g.key()
	hrWant(t, "verify after re-pack", g.assemble(false, key, "SHIM_VERIFY_FAIL_UNDER=.work/check/"), "does not verify")
	hrNoOut(g, "verify after re-pack")
}

func TestAssembleRehearsalOnlyModes(t *testing.T) {
	f := hrNew(t)
	_, key := f.key()
	final := "CASCADE_RELEASE_VERSION=2.0.0"
	hrWant(t, "skip on a release", f.assemble(false, key, final), "only for a -SNAPSHOT- rehearsal")
	hrWant(t, "ad-hoc on a release", f.assemble(false, append(hrReal(f), key, final, "CASCADE_APPLE_SIGNING_IDENTITY=-")...), "only for a -SNAPSHOT- rehearsal")
	if f.calls("codesign", "") != 0 {
		t.Error("a refused release run signed")
	}
	hrWant(t, "skip marked", f.assemble(true, key), "a "+hrVersion+" rehearsal, never a release")
}

func TestAssembleOutputSafety(t *testing.T) {
	f := hrNew(t)
	_, key := f.key()
	f.assemble(true, key, "CASCADE_DIST_OUT=rel-dist") // relative to the fixture directory
	if _, err := os.Stat(filepath.Join(f.dir, "rel-dist", "cascade_checksums.txt.minisig")); err != nil {
		t.Fatalf("relative CASCADE_DIST_OUT: %v", err)
	}
	marker := filepath.Join(f.out+".work", "pending-notarization")
	ishMust(t, os.MkdirAll(f.out+".lock", 0o700))
	ishMust(t, os.MkdirAll(filepath.Dir(marker), 0o700))
	ishMust(t, os.WriteFile(marker, []byte("an assembly in flight\n"), 0o600))
	hrWant(t, "lock held", f.assemble(false, key), "another assembly holds")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a second invocation removed the first one's work directory: %v", err)
	}
	ishMust(t, os.RemoveAll(f.out+".lock"))
	ishMust(t, os.RemoveAll(f.out+".work"))
	hrWant(t, "output appears", f.assemble(false, key, "SHIM_MKDIR="+f.out), "appeared during the assembly")
	if got := strings.Fields(f.run("ls", "-A", f.out)); len(got) != 1 || got[0] != "sentinel" {
		t.Errorf("the payload landed in a directory another writer created: %v", got)
	}
	if _, err := os.Stat(f.out + ".lock"); !os.IsNotExist(err) {
		t.Errorf("the lock outlived the run: %v", err)
	}
}

func TestHelperBuildBinding(t *testing.T) {
	for _, c := range []struct{ env, msg string }{
		{"SHIM_HELPER_REV=2222222222222222222222222222222222222222", "is not built from commit 1111111111111111111111111111111111111111"},
		{"SHIM_DIRTY=1", "helper was built from a modified tree"},
		{"SHIM_CASCADE_DIRTY=1", "was built from a modified tree"},
		{"SHIM_REV=3333333333333333333333333333333333333333", "metadata.json names another commit"},
		{"SHIM_GOOS=linux", "is not a darwin build"},
		{"SHIM_GOARCH=386", "has the wrong GOARCH"},
		{"SHIM_PATH=example.invalid/fake", "is not built from ./cmd/cascade-elevate-helper"},
		{"SHIM_TAGS=1", "was built with -tags"},
	} {
		f := hrNew(t)
		_, key := f.key()
		hrWant(t, c.env, f.assemble(false, key, c.env), c.msg)
		hrNoOut(f, c.env)
		if f.calls("codesign", "--force") != 0 {
			t.Errorf("%s: signing ran before the refusal", c.env)
		}
	}
	f := hrNew(t)
	_, key := f.key()
	f.assemble(true, key, "GOFLAGS=-tags=devkeys", "CGO_LDFLAGS=-fake")
	if f.calls("go-build-env", "GOFLAGS=[]") != 2 {
		t.Errorf("the helper build inherited the caller's GOFLAGS")
	}
}

// hrCheckCommitTime: every member of a re-packed darwin archive carries the
// commit time of the verified cascade (the fake shim's vcs.time), not the
// input archive's entry time.
func hrCheckCommitTime(f *hrFx, archive string) {
	x := f.t.TempDir()
	f.run("tar", "-xzf", archive, "-C", x)
	want := time.Date(2002, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, n := range []string{"LICENSE", "README.md", "THIRD_PARTY_LICENSES", "cascade", "cascade-github", "cascade-elevate-helper"} {
		st, err := os.Stat(filepath.Join(x, n))
		ishMust(f.t, err)
		if !st.ModTime().Equal(want) {
			f.t.Errorf("%s in %s has mtime %v, want the commit time %v", n, filepath.Base(archive), st.ModTime().UTC(), want)
		}
	}
}

func TestSyftPinMatchesRelease(t *testing.T) {
	root := filecapModuleRoot(t)
	pin := regexp.MustCompile(`(?m)^SYFT_PIN=([0-9.]+) `).FindStringSubmatch(ishRead(t, filepath.Join(root, ".github", "scripts", "assemble-rc-dist.sh")))
	used := regexp.MustCompile(`syft/releases/download/v([0-9.]+)/`).FindAllStringSubmatch(ishRead(t, filepath.Join(root, ".github", "workflows", "release.yml")), -1)
	if pin == nil || len(used) == 0 {
		t.Fatalf("no syft pin found (script %v, release.yml %d)", pin, len(used))
	}
	for _, u := range used {
		if u[1] != pin[1] {
			t.Errorf("assemble-rc-dist.sh pins syft %s, release.yml installs %s", pin[1], u[1])
		}
	}
}
