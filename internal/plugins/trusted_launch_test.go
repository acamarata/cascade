//go:build !windows

// Purpose: LaunchInstalled's refusals start nothing (an untrusted or
// missing record, a changed, missing, writable or non-regular executable),
// and a trusted child, including its respawn, sees only the allowlisted
// environment.
// SPORT: internal/plugins trusted launch tests (P1-PLG-03).
package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLaunchInstalledRefusesUntrustedRecord(t *testing.T) {
	f := newLaunchFix(t)
	exe := f.installMarker(t, "trustdemo")
	ctx := context.Background()
	res, err := AddPlugin(ctx, f.store, []byte(trustManifest), "", nil, true)
	if err != nil || res.Outcome != AddOutcomeElevationRequired {
		t.Fatalf("AddPlugin without a witness = %+v, %v; want elevation required", res, err)
	}
	if h, err := LaunchInstalled(ctx, "trustdemo"); h != nil || !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("LaunchInstalled after an unwitnessed add = %v, %v; want KindNotFound", h, err)
	}
	// A witnessed record later demoted in the store (as a tampered or
	// partial write would leave it) keeps its digest but not its trust.
	f.grant(t, "trustdemo", trustManifest)
	rec, _, _ := LoadMetadata(ctx, f.store, "trustdemo")
	rec.TrustTier = ""
	if err := SaveMetadata(ctx, f.store, rec); err != nil {
		t.Fatal(err)
	}
	h, err := LaunchInstalled(ctx, "trustdemo")
	requireNotStarted(t, h, err, exe, "cascade plugin add trustdemo")
	// A trusted record disabled through the real path does not start either.
	g := newLaunchFix(t)
	exe = g.installMarker(t, "trustdemo")
	g.grant(t, "trustdemo", trustManifest)
	if _, err := SetEnabled(ctx, g.store, "trustdemo", false); err != nil {
		t.Fatal(err)
	}
	h, err = LaunchInstalled(ctx, "trustdemo")
	requireNotStarted(t, h, err, exe, "cascade plugin add trustdemo")
}

func TestLaunchTrustRequiresDigestMatch(t *testing.T) {
	f := newLaunchFix(t)
	exe := f.installMarker(t, "trustdemo")
	f.grant(t, "trustdemo", trustManifest)
	appendByte(t, exe)
	h, err := LaunchInstalled(context.Background(), "trustdemo")
	requireNotStarted(t, h, err, exe, "executable changed since it was trusted", "cascade plugin update trustdemo --reverify")
}

func appendByte(t *testing.T, path string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchRefusesMissingExecutable(t *testing.T) {
	f := newLaunchFix(t)
	exe := f.installMarker(t, "trustdemo")
	f.grant(t, "trustdemo", trustManifest)
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	h, err := LaunchInstalled(context.Background(), "trustdemo")
	requireNotStarted(t, h, err, exe, "trustdemo")
}

func TestLaunchRefusesWritableExecutable(t *testing.T) {
	for _, mode := range []os.FileMode{0o775, 0o757} {
		t.Run(mode.String(), func(t *testing.T) {
			f := newLaunchFix(t)
			exe := f.installMarker(t, "trustdemo")
			f.grant(t, "trustdemo", trustManifest)
			if err := os.Chmod(exe, mode); err != nil {
				t.Fatal(err)
			}
			h, err := LaunchInstalled(context.Background(), "trustdemo")
			requireNotStarted(t, h, err, exe, "writable by group or others")
		})
	}
}

func TestLaunchRefusesNonRegularExecutable(t *testing.T) {
	replace := map[string]func(t *testing.T, exe string){
		"directory": func(t *testing.T, exe string) {
			if err := os.Mkdir(exe, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"symlink-to-fifo": func(t *testing.T, exe string) {
			fifo := exe + ".fifo"
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Skipf("no fifo support: %v", err)
			}
			if err := os.Symlink(fifo, exe); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, swap := range replace {
		t.Run(name, func(t *testing.T) {
			f := newLaunchFix(t)
			exe := f.installMarker(t, "trustdemo")
			f.grant(t, "trustdemo", trustManifest)
			if err := os.Remove(exe); err != nil {
				t.Fatal(err)
			}
			swap(t, exe)
			h, err := LaunchInstalled(context.Background(), "trustdemo")
			requireNotStarted(t, h, err, exe, "not a regular file")
		})
	}
}

// envProbeManifest is a process plugin without oauth:repo.
const envProbeManifest = `
id = "envprobe"
name = "Env Probe"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
requires = ["host.secret_ref"]
`

// shimSource execs the argv listed one per line in <self>.args, keeping the
// environment exactly as received: it lets the flag-driven lifecycleplugin
// run as an executable LaunchInstalled starts with no arguments.
const shimSource = `package main

import (
	"os"
	"strings"
	"syscall"
)

func main() {
	raw, err := os.ReadFile(os.Args[0] + ".args")
	if err != nil {
		os.Exit(9)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	_ = syscall.Exec(argv[0], argv, os.Environ())
	os.Exit(10)
}
`

// installEnvProbe installs the shim as "envprobe" over the P1-PLG-09
// lifecycleplugin run with -dir out -crash-first marker.
func (f *launchFix) installEnvProbe(t *testing.T, out string) {
	t.Helper()
	lp := buildInto(t, "./process/testdata/lifecycleplugin", t.TempDir(), "lifecycleplugin")
	src := t.TempDir()
	for name, body := range map[string]string{"main.go": shimSource, "go.mod": "module shim\n\ngo 1.26\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(f.exeDir, "envprobe"), ".")
	cmd.Dir, cmd.Env = src, append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the shim: %v\n%s", err, b)
	}
	args := strings.Join([]string{lp, "-dir", out, "-crash-first", filepath.Join(out, "crashed")}, "\n")
	if err := os.WriteFile(filepath.Join(f.exeDir, "envprobe.args"), []byte(args), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchInstalledChildEnvHasNoPlantedSecret(t *testing.T) {
	f := newLaunchFix(t)
	out := t.TempDir()
	f.installEnvProbe(t, out)
	for k, v := range map[string]string{"CASCADE_PRINCIPAL_TOKEN": "fake-principal-value", "ANTHROPIC_API_KEY": "fake-provider-value",
		"CASCADE_TEST_CANARY": "canary-value", "GITHUB_TOKEN": "fake-github-value", "LANG": "C"} {
		t.Setenv(k, v)
	}
	f.env = nil
	restore := SetLaunchEnvSource(func(ctx context.Context) (LaunchEnv, error) {
		env, err := f.source(ctx)
		env.Getenv = os.Getenv
		return env, err
	})
	defer restore()
	f.grant(t, "envprobe", envProbeManifest)
	h, err := LaunchInstalled(context.Background(), "envprobe")
	if err != nil {
		t.Fatalf("LaunchInstalled: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	files := waitEnvFiles(t, out, 2) // the first child crashes once; the respawn writes the second
	want := allowlistPresent(os.Getenv)
	for _, file := range files {
		if got := envNames(t, file); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: child environment names %v, want exactly %v", filepath.Base(file), got, want)
		}
	}
}

// allowlistPresent is the allowlisted base names getenv answers, sorted.
func allowlistPresent(getenv func(string) string) []string {
	var names []string
	for _, n := range childEnvBase {
		if getenv(n) != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// waitEnvFiles waits (bounded) until dir holds n env-<pid> files.
func waitEnvFiles(t *testing.T, dir string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		files, _ := filepath.Glob(filepath.Join(dir, "env-*"))
		if len(files) >= n {
			return files
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d env files after 30s, want %d", len(files), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// envNames reads an env file's variable names, sorted.
func envNames(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		if name, _, ok := strings.Cut(line, "="); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
