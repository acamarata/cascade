// installsh_fix_test.go holds install.sh tests split out of installsh_test.go
// to stay under the 300-line file cap: the unsafe-archive refusal-reason
// assertions, the legs.sh per-leg PASS assertions and the symlinked-
// destination refusal, all fixtures shared from installsh_test.go (ishFx,
// ishExe, ishMust; same package).
package build

import (
	"archive/tar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// unsafeLeg pairs a fixture's archive members with the substrings install.sh's
// own refusal message must contain, so a script that leaves the check to tar
// (which may refuse some of these on its own, e.g. "../x") fails each leg on
// its own rather than passing because a neighboring leg's check caught it.
type unsafeLeg struct {
	ms   []ishMember
	want []string
}

// TestInstallScriptRejectsUnsafeArchive: signed, checksummed archives with unsafe entries exit 1,
// nothing installed (the valid helper in the link and directory legs installs first if checks lag),
// and install.sh's own refusal reason names the specific problem.
func TestInstallScriptRejectsUnsafeArchive(t *testing.T) {
	f := ishNew(t)
	f.sign()
	helper := ishExe("cascade-elevate-helper")
	for leg, tc := range map[string]unsafeLeg{
		"parent path":   {[]ishMember{ishExe("cascade"), {"../x", "x", tar.TypeReg}}, []string{"unsafe path in archive: ../x"}},
		"absolute path": {[]ishMember{ishExe("cascade"), {"/abs", "x", tar.TypeReg}}, []string{"unsafe path in archive: /abs"}},
		"symlink cascade": {[]ishMember{helper, {"cascade", "/bin/sh", tar.TypeSymlink}},
			[]string{"archive holds a link or special file", "-> "}},
		"hard link cascade": {[]ishMember{helper, {"cascade", "cascade-elevate-helper", tar.TypeLink}},
			[]string{"archive holds a link or special file", "link to"}},
		"directory cascade": {[]ishMember{helper, {"cascade/", "", tar.TypeDir}, {"cascade/x", "x", tar.TypeReg}},
			[]string{"cascade in the archive is not a regular file"}},
	} {
		f.reset()
		f.release(tc.ms...)
		out := f.expect(1)
		t.Log(leg, ": ", out)
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: refusal message %q missing %q", leg, out, want)
			}
		}
		f.assertNothing(leg)
	}
}

// ishLegNames are every leg legs.sh must run and pass. legs.sh's own gate
// ("legs: 0 failed") is not enough: a `setup && run NAME` chain whose setup
// step fails never reaches `run`, so the leg vanishes without a FAIL line and
// "0 failed" still holds (legs.sh's `failsetup` closes that gap, but this list
// is the independent check that the leg actually ran).
var ishLegNames = []string{
	"local-ok", "download-latest-tag", "archive-checksum-mismatch", "garbage-signature",
	"missing-signature-local", "signature-by-other-key", "checksums-edited-after-signing",
	"key-file-in-dist-ignored", "two-matching-archives", "missing-signature-download", "partial-download",
	"http-base-url", "http-key-url", "unreachable-key-url", "minisign-absent",
	"renamed-archive-from-verified-entry", "key-from-https-url",
	"archive-swapped-after-verification", "idempotent-rerun",
}

// TestInstallVerifyLegs runs testdata/installsh/legs.sh: tamper, wrong-key,
// partial/plain-HTTP download, missing-minisign and TOCTOU legs, download-lane
// selection and idempotence (the script lists every leg).
func TestInstallVerifyLegs(t *testing.T) {
	f := ishNew(t)
	f.sign()
	root := filecapModuleRoot(t)
	out, err := exec.Command("sh", filepath.Join(root, "internal", "build", "testdata", "installsh", "legs.sh"),
		filepath.Join(root, "install.sh"), t.TempDir()).CombinedOutput()
	got := string(out)
	if err != nil || !strings.Contains(got, "legs: 0 failed\n") {
		t.Fatalf("legs.sh: %v\n%s", err, out)
	}
	names := ishLegNames
	if os.Geteuid() != 0 {
		names = append(append([]string{}, names...), "unwritable-install-dir")
	}
	for _, name := range names {
		if !strings.Contains(got, "PASS "+name+" (rc=") {
			t.Errorf("leg %s: no PASS line in legs.sh output\n%s", name, got)
		}
	}
}

// TestInstallScriptRefusesDestSymlinkedToDirectory: one executable's
// destination (~/.local/bin/cascade, installed last) already being a symlink
// to a directory outside the install dir refuses before writing anything for
// ANY file, including into the link target (mv -f otherwise moves the new
// file INTO the linked directory) and including the helper and plugin that
// install before cascade and would otherwise land first.
func TestInstallScriptRefusesDestSymlinkedToDirectory(t *testing.T) {
	f := ishNew(t)
	f.sign()
	f.release(ishExe("cascade"), ishExe("cascade-github"), ishExe("cascade-elevate-helper"))
	binDir := filepath.Join(f.home, ".local", "bin")
	ishMust(t, os.MkdirAll(binDir, 0o755))
	target := filepath.Join(f.root, "outside")
	ishMust(t, os.MkdirAll(target, 0o755))
	ishMust(t, os.Symlink(target, filepath.Join(binDir, "cascade")))
	out := f.expect(1)
	if !strings.Contains(out, "is a directory") {
		t.Fatalf("refusal message: %s", out)
	}
	outside, err := os.ReadDir(target)
	ishMust(t, err)
	if len(outside) != 0 {
		t.Fatalf("wrote into the symlink target outside the install dir: %v", outside)
	}
	inDir, err := os.ReadDir(binDir)
	ishMust(t, err)
	if len(inDir) != 1 || inDir[0].Name() != "cascade" {
		t.Fatalf("install dir changed (helper/plugin installed ahead of the refused cascade): %v", inDir)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".cascade", "install-receipt.json")); err == nil {
		t.Fatalf("receipt written despite the refusal")
	}
}

// ishMinisignVerdict decides what a missing minisign means: "" (present),
// "skip" (absent, not required) or "fail" (absent while require is non-empty).
func ishMinisignVerdict(present bool, require string) string {
	switch {
	case present:
		return ""
	case require != "":
		return "fail"
	}
	return "skip"
}

// ishNeedMinisign skips the calling test when minisign is not on PATH, unless
// CASCADE_REQUIRE_MINISIGN is set: the required CI lane sets it so a lane that
// lost minisign fails loudly instead of skipping every verification test.
func ishNeedMinisign(t *testing.T) {
	t.Helper()
	_, err := exec.LookPath("minisign")
	switch ishMinisignVerdict(err == nil, os.Getenv("CASCADE_REQUIRE_MINISIGN")) {
	case "fail":
		t.Fatal("minisign not on PATH and CASCADE_REQUIRE_MINISIGN is set: install minisign (brew install minisign, apt install minisign)")
	case "skip":
		t.Skip("minisign not on PATH: install minisign to run the install.sh verification tests (set CASCADE_REQUIRE_MINISIGN=1 to make its absence a failure)")
	}
}

// TestInstallMinisignGuard: the skip-or-fail decision for a missing minisign.
func TestInstallMinisignGuard(t *testing.T) {
	for _, c := range []struct {
		present bool
		require string
		want    string
	}{{true, "", ""}, {true, "1", ""}, {false, "", "skip"}, {false, "1", "fail"}} {
		if got := ishMinisignVerdict(c.present, c.require); got != c.want {
			t.Errorf("present=%v require=%q: got %q, want %q", c.present, c.require, got, c.want)
		}
	}
}
