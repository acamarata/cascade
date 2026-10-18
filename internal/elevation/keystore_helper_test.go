//go:build !windows

// Purpose: tests for the darwin elevation helper keystore: sibling-only
//
//	resolution, unsafe-file refusal, the real exec path against a built fake
//	helper, timeouts and the response bound, and the custody source wiring.
//
// Inputs: the fake helper in testdata/fakehelper, built with `go build` into
//
//	t.TempDir (no real keychain, no real key).
//
// Outputs: assertions; the exported *ForTest names let the external test
//
//	package (custody_helper_test.go, which imports internal/rpc and so cannot
//	live in this package without an import cycle) reuse the fixtures.
//
// Environment: run with the temp dir off any ignored-ownership volume, e.g.
//
//	env -u TMPDIR -u GOTMPDIR TMPDIR=/private/tmp GOTMPDIR= go test ./internal/elevation/
//	GOTMPDIR must be cleared too: Go 1.27 puts t.TempDir under GOTMPDIR, which
//	can sit on a volume that is group-writable (the helper refuses it).
//
// SPORT: internal/elevation helperKeystore tests/ADDED (P1-SHIP-15).
package elevation

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SigVerifierForTest lets the external test package implement the seam.
type SigVerifierForTest = sigVerifier

// NewHelperKeystoreForTest builds the production keystore with an injected
// executable resolver and signature verifier.
func NewHelperKeystoreForTest(exe func() (string, error), v sigVerifier) ElevationKeystore {
	return newHelperKeystoreWith(exe, platformHelperRun, v)
}

// OKVerifier accepts every signature check with a fixed fake team.
type OKVerifier struct{}

func (OKVerifier) SelfTeam() (string, error)    { return "ABCDE12345", nil }
func (OKVerifier) VerifyPath(_, _ string) error { return nil }
func (OKVerifier) VerifyPID(_ int, _ string) error {
	return nil
}

// TrustAnyVolumeForTest makes the volume-ownership rule accept every volume
// for one test (see install).
func TrustAnyVolumeForTest(t *testing.T) {
	t.Helper()
	old := platformVolumeTrusted
	platformVolumeTrusted = func(string) bool { return true }
	t.Cleanup(func() { platformVolumeTrusted = old })
}

// BuildFakeHelperForTest builds testdata/fakehelper to dir/HelperName.
func BuildFakeHelperForTest(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, HelperName)
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fakehelper")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake helper: %v\n%s", err, b)
	}
	return out
}

// install puts a fake cascade binary and (when withHelper) the built fake
// helper in a fresh dir and returns the dir and the cascade path.
func install(t *testing.T, withHelper bool) (dir, exe string) {
	t.Helper()
	// Test hosts may keep their temp dirs on a volume that ignores ownership
	// (which production refuses); the volume rule has its own test.
	TrustAnyVolumeForTest(t)
	dir = resolvedTempDir(t)
	exe = filepath.Join(dir, "cascade")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withHelper {
		BuildFakeHelperForTest(t, dir)
	}
	return dir, exe
}

func newKS(exe string, v sigVerifier) ElevationKeystore {
	return newHelperKeystoreWith(func() (string, error) { return exe, nil }, countingRun, v)
}

func wantHelperKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != want {
		t.Fatalf("error kind = %v (ok=%v), want %v; err=%v", got, ok, want, err)
	}
}

func TestHelperKeystoreResolvesOnlySibling(t *testing.T) {
	pathDir, _ := install(t, true)
	helper := filepath.Join(pathDir, HelperName)
	tmp := os.TempDir()
	for _, kv := range os.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				t.Setenv(kv[:i], helper)
				break
			}
		}
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("CASCADE_ELEVATE_HELPER", helper)
	t.Setenv("TMPDIR", tmp) // a usable temp dir, so a stray exec would get as far as running
	appDir, exe := install(t, false)
	ks := newKS(exe, OKVerifier{})
	if ks.IsAvailable() {
		t.Fatal("helper reported available with no sibling")
	}
	_, err := ks.Sign([]byte("payload"))
	wantHelperKind(t, err, cascade.KindUnavailable)
	if n := execCount(pathDir) + execCount(appDir); n != 0 {
		t.Fatalf("a helper outside the sibling slot was executed %d times", n)
	}
	copyMode(t, helper, filepath.Join(appDir, HelperName), 0o755)
	if !ks.IsAvailable() || execCount(appDir) == 0 {
		t.Fatal("control: the sibling helper was not used")
	}
}

func TestHelperKeystoreResolvesThroughSymlinkedExecutable(t *testing.T) {
	realDir, realExe := install(t, true)
	linkDir, linkExe := resolvedTempDir(t), ""
	linkExe = filepath.Join(linkDir, "cascade")
	if err := os.Symlink(realExe, linkExe); err != nil {
		t.Fatal(err)
	}
	if !newKS(linkExe, OKVerifier{}).IsAvailable() {
		t.Fatal("helper beside the real binary was not found through the symlink")
	}
	if execCount(realDir) == 0 || execCount(linkDir) != 0 {
		t.Fatalf("execs real=%d link=%d", execCount(realDir), execCount(linkDir))
	}
}

func TestHelperKeystoreRefusesUnsafeFile(t *testing.T) {
	cases := map[string]func(t *testing.T, dir, good string){
		"symlink": func(t *testing.T, dir, good string) {
			if err := os.Symlink(good, filepath.Join(dir, HelperName)); err != nil {
				t.Fatal(err)
			}
		},
		"group-writable": func(t *testing.T, dir, good string) { copyMode(t, good, filepath.Join(dir, HelperName), 0o775) },
		"world-writable": func(t *testing.T, dir, good string) { copyMode(t, good, filepath.Join(dir, HelperName), 0o757) },
		"directory": func(t *testing.T, dir, _ string) {
			if err := os.Mkdir(filepath.Join(dir, HelperName), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	goodDir, _ := install(t, true)
	good := filepath.Join(goodDir, HelperName)
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir, exe := install(t, false)
			setup(t, dir, good)
			ks := newKS(exe, OKVerifier{})
			if ks.IsAvailable() {
				t.Fatal("unsafe helper reported available")
			}
			_, err := ks.Sign([]byte("payload"))
			wantHelperKind(t, err, cascade.KindUnavailable)
			if n := execCount(dir) + execCount(goodDir); n != 0 {
				t.Fatalf("unsafe helper was executed %d times", n)
			}
		})
	}
}

func copyMode(t *testing.T, from, to string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(to, mode); err != nil {
		t.Fatal(err)
	}
}

func setMode(t *testing.T, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shortTimeouts(t *testing.T) {
	t.Helper()
	oldSign, oldOp := helperSignTimeout, helperOpTimeout
	helperSignTimeout, helperOpTimeout = 400*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { helperSignTimeout, helperOpTimeout = oldSign, oldOp })
}

func TestHelperKeystoreTimeoutIsUnavailable(t *testing.T) {
	shortTimeouts(t)
	dir, exe := install(t, true)
	setMode(t, dir, "sleep")
	start := time.Now()
	_, err := newKS(exe, OKVerifier{}).Sign([]byte("payload"))
	wantHelperKind(t, err, cascade.KindUnavailable)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("hung helper held the caller for %v", time.Since(start))
	}
}

func TestHelperKeystoreBoundsResponse(t *testing.T) {
	dir, exe := install(t, true)
	setMode(t, dir, "flood")
	start := time.Now()
	_, err := newKS(exe, OKVerifier{}).Sign([]byte("payload"))
	wantHelperKind(t, err, cascade.KindUnavailable)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("flooding helper held the caller for %v", time.Since(start))
	}
}

func TestNoHelperSourceOffDarwin(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		srcs := sourcesForOS(goos, NewKeystore)
		for _, s := range srcs {
			if s.Name == "darwin-helper" {
				t.Fatalf("%s lists the darwin helper source", goos)
			}
		}
		if goos == "linux" && len(srcs) == 0 {
			t.Fatal("control: linux lost its sources")
		}
	}
	if len(sourcesForOS("darwin", NewKeystore)) < 2 {
		t.Fatal("control: darwin lost its sources")
	}
}

// The helper source waits for docs/elevation-helper.md "Registration".
func TestHelperSourceUnregisteredOnDarwin(t *testing.T) {
	srcs := sourcesForOS("darwin", NewKeystore)
	if len(srcs) != 2 || srcs[0].Name != "platform" || srcs[1].Name != "file" {
		t.Fatalf("darwin sources are not exactly [platform file]: %d", len(srcs))
	}
}

// unavailableKS stands in for the in-process platform keystore of a
// release-shaped (CGO_ENABLED=0) build.
type unavailableKS struct{ ElevationKeystore }

func (unavailableKS) IsAvailable() bool { return false }

func TestDarwinHelperSourceRanksPlatform(t *testing.T) {
	dir, exe := install(t, true)
	oldExe, oldVer := helperExecutable, helperVerifier
	helperExecutable, helperVerifier = func() (string, error) { return exe, nil }, OKVerifier{}
	t.Cleanup(func() { helperExecutable, helperVerifier = oldExe, oldVer })
	sel := func() Selector {
		srcs := sourcesForOS("darwin", func() ElevationKeystore { return unavailableKS{} })
		return Selector{Sources: append([]CustodySource{helperSource()}, srcs...)}
	}
	c := sel().Select()
	if c.Tier() != CustodyPlatform || c.Source() != "darwin-helper" {
		t.Fatalf("with a usable helper: tier=%s source=%s reason=%s", c.Tier(), c.Source(), c.Reason())
	}
	if err := os.Remove(filepath.Join(dir, HelperName)); err != nil {
		t.Fatal(err)
	}
	c = sel().Select()
	if c.Tier() != CustodyNone {
		t.Fatalf("with the helper absent: tier=%s source=%s", c.Tier(), c.Source())
	}
	if _, err := c.Signer(); err == nil {
		t.Fatal("Signer() allowed an elevated path with no custody")
	} else if tier, ok := CustodyTierOf(err); !ok || tier != CustodyNone {
		t.Fatalf("Signer error is not the SEC-00 tier error: %v", err)
	}
}

func writeFileForTest(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// WriteFileForTest creates a stand-in cascade executable.
func WriteFileForTest(t *testing.T, path string) { writeFileForTest(t, path) }
