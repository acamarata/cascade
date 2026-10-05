// installsh_test.go drives the root install.sh against releases built in-test
// (archive/tar, signed through the real minisign; skipped, naming it, without
// it unless CASCADE_REQUIRE_MINISIGN is set, which makes its absence a failure). PATH shims from testdata/installsh stand in for curl, uname and mv; HOME
// is t.TempDir(). Refusals assert HOME stays empty. Download-lane and tamper
// legs live in testdata/installsh/legs.sh (TestInstallVerifyLegs).
package build

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	ishArchive = "cascade_1.0.0_linux_amd64.tar.gz"
	ishSums    = "cascade_1.0.0_checksums.txt"
)

// ishMember is one archive entry; body is the link target for non-regular types.
type ishMember struct {
	name, body string
	typ        byte
}

func ishExe(name string) ishMember {
	return ishMember{name, "#!/bin/sh\necho " + name + "\n", tar.TypeReg}
}

type ishFx struct {
	t                                  *testing.T
	root, dist, bin, home, key, pubKey string
	env                                []string
}

// ishNew builds a fixture with the curl and uname shims plus the named extras.
func ishNew(t *testing.T, shims ...string) *ishFx {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is POSIX-only (set -eu, sh); Windows installs are the manual download in docs/install.md")
	}
	r := t.TempDir()
	f := &ishFx{t: t, root: r, dist: filepath.Join(r, "dist"), bin: filepath.Join(r, "bin")}
	ishMust(t, os.MkdirAll(f.bin, 0o755))
	for _, s := range append([]string{"curl", "uname"}, shims...) {
		b := ishRead(t, filepath.Join(filecapModuleRoot(t), "internal", "build", "testdata", "installsh", s+".sh"))
		ishMust(t, os.WriteFile(filepath.Join(f.bin, s), []byte(b), 0o755))
	}
	f.reset()
	return f
}

func ishMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ishRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	ishMust(t, err)
	return string(b)
}

func ishSHA(t *testing.T, p string) string {
	s := sha256.Sum256([]byte(ishRead(t, p)))
	return hex.EncodeToString(s[:])
}

// reset gives the fixture a fresh HOME and an empty dist dir.
func (f *ishFx) reset() {
	f.home = f.t.TempDir()
	ishMust(f.t, errors.Join(os.RemoveAll(f.dist), os.MkdirAll(f.dist, 0o755)))
}

// sign generates the ephemeral release key; without minisign it skips, or fails
// when CASCADE_REQUIRE_MINISIGN is set (see ishNeedMinisign).
func (f *ishFx) sign() {
	ishNeedMinisign(f.t)
	f.key, f.pubKey = filepath.Join(f.root, "k.key"), filepath.Join(f.root, "k.pub")
	f.run("minisign", "-G", "-W", "-s", f.key, "-p", f.pubKey)
	f.env = []string{"CASCADE_MINISIGN_PUBKEY=" + f.pubKey}
}

func (f *ishFx) run(name string, args ...string) {
	f.t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		f.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// archive writes a tar.gz of ms to dir/name and returns its sha256.
func (f *ishFx) archive(dir, name string, ms ...ishMember) string {
	p := filepath.Join(dir, name)
	out, err := os.Create(p)
	ishMust(f.t, err)
	zw := gzip.NewWriter(out)
	tw := tar.NewWriter(zw)
	for _, m := range ms {
		h := &tar.Header{Name: m.name, Typeflag: m.typ, Mode: 0o755, Linkname: m.body, ModTime: time.Unix(1e9, 0)}
		if m.typ == tar.TypeReg {
			h.Size, h.Linkname = int64(len(m.body)), ""
		}
		ishMust(f.t, tw.WriteHeader(h))
		_, err = tw.Write([]byte(m.body)[:h.Size])
		ishMust(f.t, err)
	}
	ishMust(f.t, errors.Join(tw.Close(), zw.Close(), out.Close()))
	return ishSHA(f.t, p)
}

// sums writes dir/name holding lines and signs it with the release key.
func (f *ishFx) sums(dir, name string, lines ...string) {
	p := filepath.Join(dir, name)
	ishMust(f.t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	f.run("minisign", "-S", "-s", f.key, "-m", p, "-x", p+".minisig", "-t", "test checksums")
}

// release builds a signed linux/amd64 local-lane release and returns the archive sha256.
func (f *ishFx) release(ms ...ishMember) string {
	sum := f.archive(f.dist, ishArchive, ms...)
	f.sums(f.dist, ishSums, sum+"  "+ishArchive)
	return sum
}

// expect runs install.sh with a clean environment and asserts its exit code.
func (f *ishFx) expect(code int, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(filecapModuleRoot(f.t), "install.sh")}, args...)...)
	cmd.Env = append([]string{"PATH=" + f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + f.home, "USERPROFILE=" + f.home, "CASCADE_HOME=" + filepath.Join(f.home, ".cascade"),
		"CASCADE_INSTALL_DIST=" + f.dist, "SHIM_LOG=" + filepath.Join(f.root, "curl.log"),
		"SHIM_MVLOG=" + filepath.Join(f.root, "mv.log"), "SHIM_UNAME_S=Linux", "SHIM_UNAME_M=x86_64"}, f.env...)
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != code {
		f.t.Fatalf("install.sh %v: %v, want exit %d\n%s", args, err, code, out)
	}
	return string(out)
}

// assertNothing: a refused run leaves HOME empty (no install dir, no receipt).
func (f *ishFx) assertNothing(leg string) {
	if entries, err := os.ReadDir(f.home); err != nil || len(entries) != 0 {
		f.t.Errorf("%s: HOME not empty after a refusal: %v %v", leg, entries, err)
	}
}

// installed asserts exactly names (sorted) sit in the install dir, each 0755
// and listed in the parsed receipt with its digest; it returns the receipt.
func (f *ishFx) installed(names ...string) string {
	f.t.Helper()
	dir := filepath.Join(f.home, ".local", "bin")
	rec := ishRead(f.t, filepath.Join(f.home, ".cascade", "install-receipt.json"))
	realDir, err := filepath.EvalSymlinks(dir)
	entries, err2 := os.ReadDir(dir)
	var parsed map[string]any
	ishMust(f.t, errors.Join(err, err2, json.Unmarshal([]byte(rec), &parsed)))
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
		p := filepath.Join(dir, e.Name())
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o755 || !strings.Contains(rec, `{"name":"`+e.Name()+`","sha256":"`+ishSHA(f.t, p)+`"}`) {
			f.t.Errorf("%s: mode or digest differs from the receipt %s (%v)", e.Name(), rec, err)
		}
	}
	if !strings.HasPrefix(rec, `{"schema_version":1,"channel":"script",`) || !strings.Contains(rec, `"install_dir":"`+realDir+`"`) ||
		strings.Join(got, " ") != strings.Join(names, " ") || strings.Count(rec, `"name":`) != len(names) {
		f.t.Fatalf("installed %v, receipt %s; want files %v in %s", got, rec, names, realDir)
	}
	return rec
}

// TestInstallScriptPlatformTable: unsupported pairs exit 3 writing nothing; supported pairs pick their archive.
func TestInstallScriptPlatformTable(t *testing.T) {
	f := ishNew(t)
	for _, u := range [][2]string{{"FreeBSD", "amd64"}, {"Linux", "i686"}, {"MINGW64_NT-10.0-19045", "x86_64"}} {
		f.env = []string{"SHIM_UNAME_S=" + u[0], "SHIM_UNAME_M=" + u[1]}
		if out := f.expect(3); !strings.Contains(out, "Install by hand") || !strings.Contains(out, "minisign -Vm") {
			t.Errorf("%v: no manual-download text:\n%s", u, out)
		}
		f.assertNothing(u[0])
	}
	f.sign()
	pairs := [][4]string{{"Darwin", "arm64", "darwin", "arm64"}, {"Darwin", "x86_64", "darwin", "amd64"},
		{"Linux", "x86_64", "linux", "amd64"}, {"Linux", "aarch64", "linux", "arm64"}}
	var lines []string
	for _, p := range pairs {
		n := "cascade_1.0.0_" + p[2] + "_" + p[3] + ".tar.gz"
		lines = append(lines, f.archive(f.dist, n, ishMember{"cascade", "#!/bin/sh\necho " + n + "\n", tar.TypeReg})+"  "+n)
	}
	f.sums(f.dist, ishSums, lines...)
	for _, p := range pairs {
		f.home = t.TempDir()
		f.env = []string{"CASCADE_MINISIGN_PUBKEY=" + f.pubKey, "SHIM_UNAME_S=" + p[0], "SHIM_UNAME_M=" + p[1]}
		f.expect(0)
		want := "cascade_1.0.0_" + p[2] + "_" + p[3] + ".tar.gz"
		if r := f.installed("cascade"); !strings.Contains(r, `"archive_name":"`+want+`"`) ||
			!strings.Contains(ishRead(t, filepath.Join(f.home, ".local", "bin", "cascade")), want) {
			t.Errorf("%v: installed %s, want %s", p, r, want)
		}
	}
}

// TestInstallScriptLocalLaneAmbiguity: ambiguous or missing local-lane files exit 2 before any write.
func TestInstallScriptLocalLaneAmbiguity(t *testing.T) {
	f := ishNew(t)
	for leg, files := range map[string][]string{
		"two archives":  {ishArchive, "cascade_1.0.1_linux_amd64.tar.gz", ishSums, ishSums + ".minisig"},
		"zero archives": {"cascade_1.0.0_darwin_arm64.tar.gz", ishSums, ishSums + ".minisig"},
		"two checksums": {ishArchive, "a_checksums.txt", "a_checksums.txt.minisig", ishSums, ishSums + ".minisig"},
		"no signature":  {ishArchive, ishSums},
	} {
		f.reset()
		for _, n := range files {
			ishMust(t, os.WriteFile(filepath.Join(f.dist, n), []byte("x\n"), 0o644))
		}
		t.Log(leg, ": ", f.expect(2))
		f.assertNothing(leg)
	}
}

// TestInstallScriptInstallsEveryExecutable: all three install 0755; helper, plugin, cascade, then the receipt.
func TestInstallScriptInstallsEveryExecutable(t *testing.T) {
	f := ishNew(t, "mv")
	f.sign()
	f.release(ishExe("cascade"), ishExe("cascade-github"), ishExe("cascade-elevate-helper"), ishMember{"README.md", "r", tar.TypeReg})
	f.expect(0)
	f.installed("cascade", "cascade-elevate-helper", "cascade-github")
	if got := ishRead(t, filepath.Join(f.root, "mv.log")); got != "cascade-elevate-helper\ncascade-github\ncascade\ninstall-receipt.json\n" {
		t.Fatalf("rename order:\n%s", got)
	}
}

// TestInstallScriptInstallsOnlyKnownExecutables: executables outside the fixed set are never installed.
func TestInstallScriptInstallsOnlyKnownExecutables(t *testing.T) {
	f := ishNew(t)
	f.sign()
	f.release(ishExe("evil"), ishExe("cascade"), ishExe("cascade-github"), ishExe("cascade-x"))
	f.expect(0)
	f.installed("cascade", "cascade-github")
}

// TestInstallScriptSelectsArchiveAmongSBOMLines: only the single matching VERIFIED entry is used.
func TestInstallScriptSelectsArchiveAmongSBOMLines(t *testing.T) {
	f := ishNew(t)
	f.sign()
	sum := f.archive(f.dist, ishArchive, ishExe("cascade"))
	sbom := f.archive(f.dist, ishArchive+".sbom.json", ishExe("cascade"))
	f.sums(f.dist, ishSums, sbom+"  "+ishArchive+".sbom.json", sum+"  "+ishArchive,
		sbom+"  cascade_1.0.0_darwin_arm64.tar.gz", sbom+"  cascade_1.0.0_linux_amd64.tar.gz.sig")
	f.expect(0)
	if r := f.installed("cascade"); !strings.Contains(r, `"release_tag":"local","archive_name":"`+ishArchive+`","archive_sha256":"`+sum+`"`) {
		t.Fatalf("local lane selected %s", r)
	}
}
