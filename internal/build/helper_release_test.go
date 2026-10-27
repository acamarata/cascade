// helper_release_test.go checks contract:elevation-helper-release. The CI
// release config never builds or packs cascade-elevate-helper; the build-Mac
// scripts (build-elevate-helper.sh, assemble-rc-dist.sh) run against a fake
// build payload (testdata/seeded-violations/helper-release/mkbuild.sh) with
// PATH shims for go, codesign, syft, ditto and xcrun, and the real minisign.
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	hrSeeded  = "internal/build/testdata/seeded-violations/helper-release"
	hrVersion = "2.0.0-test.1-SNAPSHOT-0123abcd"
	hrPass    = "fake-test-passphrase-0000"
)

// wrScanned is every release script the static checks of
// widget_release_test.go and this file scan.
var wrScanned = []string{wrBuild, wrSig, "notary-lib.sh", "build-elevate-helper.sh", "assemble-rc-dist.sh"}

// hrConfigViolations lists every way .goreleaser.yaml text lets CI build,
// archive or ship the helper.
func hrConfigViolations(t *testing.T, text string) []string {
	cfg := relDecode[map[string]any](t, text)
	var bad []string
	for _, sec := range []string{"before", "builds", "archives", "dockers", "homebrew_casks"} {
		b, err := yaml.Marshal(cfg[sec])
		ishMust(t, err)
		if strings.Contains(string(b), "elevate-helper") {
			bad = append(bad, sec+" names the elevate helper")
		}
		if sec == "builds" && strings.Contains(string(b), "CGO_ENABLED=1") {
			bad = append(bad, "a CI build enables cgo")
		}
	}
	return bad
}

func TestHelperReleaseConfig(t *testing.T) {
	relSeeds(t, ".goreleaser.yaml", hrConfigViolations, map[string][2]string{
		"archives names": {"      - src: docs/THIRD_PARTY_LICENSES", "      - cascade-elevate-helper\n      - src: docs/THIRD_PARTY_LICENSES"},
		"before names":   {"    - sh -c 'test -s docs/THIRD_PARTY_LICENSES'", "    - bash .github/scripts/build-elevate-helper.sh"},
		"builds names":   {"    main: ./plugins/github", "    main: ./cmd/cascade-elevate-helper"},
		"enables cgo":    {"    main: ./cmd/cascade\n    binary: cascade\n    env:\n      - CGO_ENABLED=0", "    main: ./cmd/cascade\n    binary: cascade\n    env:\n      - CGO_ENABLED=1"},
	})
}

type hrFx struct {
	t                         *testing.T
	root, dir, in, out, shLog string
	repo                      string // a fake clone holding copies of the release scripts
	env                       []string
}

// hrNew writes a fake build payload and installs the shims; extra names
// more shimmed tools (minisign for the canary run).
func hrNew(t *testing.T, extra ...string) *hrFx {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("assemble-rc-dist.sh runs on the macOS build host (bsdtar, codesign)")
	}
	d, err := filepath.EvalSymlinks(t.TempDir())
	ishMust(t, err)
	f := &hrFx{t: t, root: filecapModuleRoot(t), dir: d, in: filepath.Join(d, "build"), out: filepath.Join(d, "dist"), shLog: filepath.Join(d, "shim.log")}
	f.run("bash", filepath.Join(f.root, filepath.FromSlash(hrSeeded), "mkbuild.sh"), f.in, hrVersion)
	bin := filepath.Join(d, "bin")
	ishMust(t, os.MkdirAll(bin, 0o755))
	shim := ishRead(t, filepath.Join(f.root, filepath.FromSlash(hrSeeded), "shims", "shim.sh"))
	f.repo = hrRepo(f)
	for _, n := range append([]string{"go", "git", "codesign", "syft", "ditto", "xcrun"}, extra...) {
		ishMust(t, os.WriteFile(filepath.Join(bin, n), []byte(shim), 0o755))
	}
	f.env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CASCADE_") || strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "SHIM_")
	})
	f.env = append(f.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SHIM_LOG="+f.shLog,
		"CASCADE_BUILD_DIR="+f.in, "CASCADE_DIST_OUT="+f.out, "CASCADE_RELEASE_VERSION="+hrVersion,
		"CASCADE_APPLE_SIGNING_IDENTITY=-", "CASCADE_NOTARY_SKIP=1")
	return f
}

func (f *hrFx) run(name string, args ...string) string {
	f.t.Helper()
	b, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("%s %v: %v\n%s", name, args, err, b)
	}
	return string(b)
}

// key makes an encrypted minisign test key in the fixture and returns the
// public key path and the env naming the secret key.
func (f *hrFx) key() (pub, env string) {
	ishNeedMinisign(f.t)
	pub, sec := filepath.Join(f.dir, "test.pub"), filepath.Join(f.dir, "test.key")
	c := exec.Command("minisign", "-G", "-p", pub, "-s", sec)
	c.Stdin = strings.NewReader(hrPass + "\n" + hrPass + "\n")
	if b, err := c.CombinedOutput(); err != nil {
		f.t.Fatalf("minisign -G: %v\n%s", err, b)
	}
	return pub, "CASCADE_MINISIGN_KEY=" + sec
}

// assemble runs the whole assembly and wants exit 0, or exit 1 (a refusal).
func (f *hrFx) assemble(wantOK bool, envs ...string) string {
	f.t.Helper()
	code := 1
	if wantOK {
		code = 0
	}
	return f.script("all", code, envs...)
}

// script runs one mode from the fixture directory with the passphrase on
// stdin and fails unless it exits with code; envs as in wrFx.cmd.
func (f *hrFx) script(mode string, code int, envs ...string) string {
	f.t.Helper()
	c := exec.Command("bash", filepath.Join(f.repo, ".github", "scripts", "assemble-rc-dist.sh"), mode)
	c.Dir = f.dir
	all := append(append([]string{}, f.env...), envs...)
	for _, kv := range all {
		if !strings.HasPrefix(kv, "!") && !slices.Contains(all, "!"+strings.SplitN(kv, "=", 2)[0]) {
			c.Env = append(c.Env, kv)
		}
	}
	f.writeShimEnv(c.Env)
	c.Stdin = strings.NewReader(hrPass + "\n")
	b, _ := c.CombinedOutput() // the exit code below is the verdict
	if got := c.ProcessState.ExitCode(); got != code {
		log, _ := os.ReadFile(f.shLog)
		f.t.Fatalf("assemble %s %v: exit %d, want %d\n%s\nshims:\n%s", mode, envs, got, code, b, log)
	}
	return string(b)
}

func (f *hrFx) calls(tool, sub string) int {
	b, _ := os.ReadFile(f.shLog)
	return len(regexp.MustCompile(`(?m)^`+regexp.QuoteMeta(tool)+` .*`+regexp.QuoteMeta(sub)).FindAllString(string(b), -1))
}

func TestAssembleRefusesUnsafeHelper(t *testing.T) {
	for _, c := range []struct{ name, env, tamper, neverRan, msg string }{
		{"helper without cgo", "SHIM_NOCGO=1", "", "codesign --force", "not a cgo build"},
		{"helper failing nodevkeys", "SHIM_DEVKEYS=1", "", "codesign --force", "fails the devkeys guard"},
		{"input digest mismatch", "", "cascade_" + hrVersion + "_linux_amd64.tar.gz", "go build", "does not match its cascade_checksums.txt"},
		{"codesign verify failure", "SHIM_FAIL=codesign:--verify", "", "syft scan", "codesign --verify --strict refused"},
		{"helper already in an archive", "", "darwin-helper", "go build", "does not hold exactly"},
	} {
		f := hrNew(t)
		_, env := f.key()
		if c.tamper == "darwin-helper" {
			hrAddHelperToInput(f)
		} else if c.tamper != "" {
			ishMust(t, os.WriteFile(filepath.Join(f.in, c.tamper), []byte("tampered after the checksums\n"), 0o644))
		}
		out := f.assemble(false, env, c.env)
		if !strings.Contains(out, c.msg) {
			t.Errorf("%s: refused for another reason than %q:\n%s", c.name, c.msg, out)
		}
		if _, err := os.Lstat(f.out); !os.IsNotExist(err) {
			t.Errorf("%s: CASCADE_DIST_OUT was written (%v)\n%s", c.name, err, out)
		}
		if _, err := os.Lstat(f.out + ".work"); !os.IsNotExist(err) {
			t.Errorf("%s: the work directory with unsigned material was left behind", c.name)
		}
		if f.calls(strings.SplitN(c.neverRan, " ", 2)[0], strings.SplitN(c.neverRan, " ", 2)[1]) != 0 {
			t.Errorf("%s: %q ran before the refusal", c.name, c.neverRan)
		}
	}
}

// hrAddHelperToInput rebuilds the darwin arm64 input archive with a helper
// inside and fixes its checksum line, so only the member check can refuse it.
func hrAddHelperToInput(f *hrFx) {
	a := "cascade_" + hrVersion + "_darwin_arm64.tar.gz"
	x := filepath.Join(f.dir, "x")
	ishMust(f.t, os.MkdirAll(x, 0o755))
	f.run("tar", "-xzf", filepath.Join(f.in, a), "-C", x)
	ishMust(f.t, os.WriteFile(filepath.Join(x, "cascade-elevate-helper"), []byte("smuggled helper\n"), 0o755))
	f.run("tar", "-czf", filepath.Join(f.in, a), "-C", x, "LICENSE", "README.md", "THIRD_PARTY_LICENSES", "cascade", "cascade-github", "cascade-elevate-helper")
	sum := strings.Fields(f.run("shasum", "-a", "256", filepath.Join(f.in, a)))[0]
	sums := filepath.Join(f.in, "cascade_checksums.txt")
	text := regexp.MustCompile(`(?m)^[0-9a-f]{64}  `+regexp.QuoteMeta(a)+`$`).ReplaceAllString(ishRead(f.t, sums), sum+"  "+a)
	ishMust(f.t, os.WriteFile(sums, []byte(text), 0o644))
}

func TestAssembleProducesDistLayout(t *testing.T) {
	f := hrNew(t)
	pub, env := f.key()
	f.assemble(true, env)
	sums := ishRead(t, filepath.Join(f.out, "cascade_checksums.txt"))
	want := []string{"Casks/cascade.rb", "cascade_checksums.txt", "cascade_checksums.txt.minisig"}
	for _, l := range strings.Split(strings.TrimSpace(sums), "\n") {
		want = append(want, strings.Fields(l)[1])
	}
	slices.Sort(want)
	got := strings.Fields(f.run("bash", "-c", `cd "$1" && find . -type f | sed 's|^\./||' | LC_ALL=C sort`, "_", f.out))
	if !slices.Equal(got, want) || !strings.Contains(sums, "  cascade_images.txt\n") {
		t.Fatalf("dist layout %q, want %q", got, want)
	}
	f.run("bash", "-c", `cd "$1" && shasum -a 256 -c -q cascade_checksums.txt`, "_", f.out)
	f.run("minisign", "-Vm", filepath.Join(f.out, "cascade_checksums.txt"), "-p", pub)
	if tc := strings.Split(ishRead(t, filepath.Join(f.out, "cascade_checksums.txt.minisig")), "\n")[2]; tc != "trusted comment: cascade "+hrVersion+" checksums" {
		t.Fatalf("trusted comment %q", tc)
	}
	for _, n := range []string{"linux_amd64.tar.gz", "linux_arm64.tar.gz", "windows_amd64.zip", "linux_amd64.tar.gz.sbom.json"} {
		if a := "cascade_" + hrVersion + "_" + n; ishRead(t, filepath.Join(f.out, a)) != ishRead(t, filepath.Join(f.in, a)) {
			t.Errorf("%s is not byte-identical to the build input", a)
		}
	}
	hrCheckDarwin(f)
	first := filepath.Join(f.dir, "dist-first")
	ishMust(t, os.Rename(f.out, first))
	f.assemble(true, env)
	for _, arch := range []string{"arm64", "amd64"} {
		if a := "cascade_" + hrVersion + "_darwin_" + arch + ".tar.gz"; ishRead(t, filepath.Join(first, a)) != ishRead(t, filepath.Join(f.out, a)) {
			t.Errorf("%s differs between two runs on the same input", a)
		}
	}
}

// hrCheckDarwin: each darwin archive holds the signed helper beside cascade,
// and the cask names the new digests and the helper binary.
func hrCheckDarwin(f *hrFx) {
	cask := ishRead(f.t, filepath.Join(f.out, "Casks", "cascade.rb"))
	for _, arch := range []string{"arm64", "amd64"} {
		a := filepath.Join(f.out, "cascade_"+hrVersion+"_darwin_"+arch+".tar.gz")
		list := f.run("tar", "-tvzf", a)
		if !regexp.MustCompile(`(?m)^-rwxr-xr-x +\d+ +0 +0 .* cascade-elevate-helper$`).MatchString(list) || strings.Count(list, "\n") != 6 {
			f.t.Errorf("darwin/%s archive lacks an executable root-owned helper or holds extra members:\n%s", arch, list)
		}
		hrCheckCommitTime(f, a)
		helper := f.run("tar", "-xOzf", a, "cascade-elevate-helper")
		if helper != "fake helper "+arch+" trimpath\nsignature cascade-elevate-helper by -\n" {
			f.t.Errorf("darwin/%s helper is not the built and signed one: %q", arch, helper)
		}
		if !strings.Contains(cask, `sha256 "`+strings.Fields(f.run("shasum", "-a", "256", a))[0]+`"`) {
			f.t.Errorf("the cask does not carry the new darwin/%s digest", arch)
		}
	}
	if !strings.Contains(cask, "  on_macos do\n    binary \"cascade-elevate-helper\"\n") {
		f.t.Errorf("the cask does not install the helper on macOS:\n%s", cask)
	}
}

func TestHelperScriptsNoKeychainWritesNoXtrace(t *testing.T) {
	bad := regexp.MustCompile(`store-credentials|allowProvisioning|security +(import|add-|create-keychain|delete-|set-key-partition-list|unlock-keychain|default-keychain|list-keychains +-s)`)
	xtrace := regexp.MustCompile(`(?m)set -[A-Za-z]*x|xtrace|^#!.*\s-[A-Za-z]*x`)
	for _, s := range wrScanned {
		text := ishRead(t, filepath.Join(filecapModuleRoot(t), ".github", "scripts", s))
		if m := bad.FindString(text); m != "" || xtrace.MatchString(text) {
			t.Errorf("%s holds a keychain write %q or enables xtrace", s, m)
		}
	}
	f := hrNew(t, "minisign")
	keyID, issuer, body := "CANARYKEYID7", "canary-issuer-5d1c", strings.Repeat("Q0FOQVJZS0VZTUFURVJJQUw", 3)[:64]
	ishMust(t, os.WriteFile(filepath.Join(f.dir, "key.p8"), []byte("-----BEGIN "+"PRIVATE KEY-----\n"+body+"\n-----END "+"PRIVATE KEY-----\n"), 0o600))
	ishMust(t, os.WriteFile(filepath.Join(f.dir, "mini.key"), []byte("CANARYMINISIGNSECRET\n"), 0o600))
	seen := f.assemble(true, "!CASCADE_NOTARY_SKIP", "CASCADE_APPLE_SIGNING_IDENTITY=Developer ID Application: Example (FAKETEAM00)",
		"CASCADE_APPLE_NOTARY_KEY_ID="+keyID, "CASCADE_APPLE_NOTARY_KEY_ISSUER="+issuer,
		"CASCADE_APPLE_NOTARY_KEY_PATH="+filepath.Join(f.dir, "key.p8"), "CASCADE_MINISIGN_KEY="+filepath.Join(f.dir, "mini.key"))
	files, err := exec.Command("find", f.out, "-type", "f", "-exec", "cat", "{}", "+").Output()
	ishMust(t, err)
	if f.calls("notarytool", "submit") != 1 || f.calls("minisign", "-S") != 1 || len(files) == 0 {
		t.Fatalf("the canary run did not notarize and sign through the shims:\n%s", seen)
	}
	if b, _ := os.ReadFile(f.shLog); regexp.MustCompile(`(?m)^fd3-open (go|codesign|syft|ditto|notarytool)$`).Match(b) {
		t.Errorf("the passphrase descriptor reached a child other than minisign:\n%s", b)
	}
	for _, canary := range []string{keyID, issuer, body[:40], "PRIVATE KEY", "CANARYMINISIGNSECRET", hrPass} {
		if strings.Contains(seen+string(files), canary) {
			t.Errorf("canary %q leaked into output or $OUT", canary)
		}
	}
}
