// Purpose: OS-independent tests for the widget installer's shared helpers
//
//	(bundle resolution, install validation, plist path, self-report read,
//	the exec output runner). The installer itself is darwin-only, so these
//	keep the shared code covered on every platform the package builds on.
//
// Constraints: only t.TempDir() and the test binary's own environment.
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newPortableConfig is a complete install config over a temp HOME.
func newPortableConfig(t *testing.T) WidgetConfig {
	t.Helper()
	home := t.TempDir()
	paths, err := cruntime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	return WidgetConfig{
		HomeDir: home, Executable: filepath.Join(home, "cascade"), Paths: paths,
		Clock:  cruntime.NewFixedClock(time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)),
		Runner: &fakeRunner{},
	}
}

// portableBundle creates dir/<name> with a regular executable at the
// bundle's executable path and returns the bundle path.
func portableBundle(t *testing.T, dir, name string) string {
	t.Helper()
	bundle := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(widgetExecutablePath(bundle), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test executable stand-in
		t.Fatal(err)
	}
	return bundle
}

func skipWithoutUnixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits and symlinks are not meaningful here")
	}
}

func wantKind(t *testing.T, err error, kind cascade.Kind) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("error = %v, want kind %v", err, kind)
	}
}

func TestWidgetCandidates_DefaultOrderAndOverride(t *testing.T) {
	cfg := WidgetConfig{HomeDir: filepath.Join("h", "me"), SystemRoot: filepath.Join("s", "root")}
	got := widgetCandidates(cfg)
	want := []string{
		filepath.Join("s", "root", "/Applications", WidgetBundleName),
		filepath.Join("h", "me", "Applications", WidgetBundleName),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	cfg.Candidates = []string{"only"}
	if got := widgetCandidates(cfg); len(got) != 1 || got[0] != "only" {
		t.Fatalf("override ignored: %v", got)
	}
}

func TestResolveWidgetBundle(t *testing.T) {
	skipWithoutUnixModes(t)
	t.Run("none found names every path tried", func(t *testing.T) {
		root := t.TempDir()
		cfg := WidgetConfig{HomeDir: filepath.Join(root, "home"), SystemRoot: filepath.Join(root, "sys")}
		_, err := resolveWidgetBundle(cfg)
		wantKind(t, err, cascade.KindNotFound)
		for _, c := range widgetCandidates(cfg) {
			if !strings.Contains(err.Error(), c) {
				t.Errorf("error %q does not name %s", err, c)
			}
		}
		if !strings.Contains(err.Error(), " and ") {
			t.Errorf("error %q does not join the candidates", err)
		}
	})
	t.Run("system bundle wins over the home bundle", func(t *testing.T) {
		root := t.TempDir()
		cfg := WidgetConfig{HomeDir: filepath.Join(root, "home"), SystemRoot: filepath.Join(root, "sys")}
		sys := portableBundle(t, filepath.Join(cfg.SystemRoot, "Applications"), WidgetBundleName)
		portableBundle(t, filepath.Join(cfg.HomeDir, "Applications"), WidgetBundleName)
		got, err := resolveWidgetBundle(cfg)
		if err != nil || got != sys {
			t.Fatalf("resolve = %q, %v; want %q", got, err, sys)
		}
	})
	t.Run("falls through to the home bundle", func(t *testing.T) {
		root := t.TempDir()
		cfg := WidgetConfig{HomeDir: filepath.Join(root, "home"), SystemRoot: filepath.Join(root, "sys")}
		home := portableBundle(t, filepath.Join(cfg.HomeDir, "Applications"), WidgetBundleName)
		got, err := resolveWidgetBundle(cfg)
		if err != nil || got != home {
			t.Fatalf("resolve = %q, %v; want %q", got, err, home)
		}
	})
}

func TestResolveWidgetBundle_Refusals(t *testing.T) {
	skipWithoutUnixModes(t)
	t.Run("a plain file candidate is skipped", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, WidgetBundleName)
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := resolveWidgetBundle(WidgetConfig{Candidates: []string{file}})
		wantKind(t, err, cascade.KindNotFound)
	})
	t.Run("a symlink candidate is refused", func(t *testing.T) {
		root := t.TempDir()
		target := portableBundle(t, root, "target"+WidgetBundleName)
		link := filepath.Join(root, WidgetBundleName)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := resolveWidgetBundle(WidgetConfig{Candidates: []string{link}})
		wantKind(t, err, cascade.KindInvalidInput)
	})
	t.Run("a wrong base name is refused", func(t *testing.T) {
		wrong := portableBundle(t, t.TempDir(), "Other.app")
		_, err := resolveWidgetBundle(WidgetConfig{Candidates: []string{wrong}})
		wantKind(t, err, cascade.KindInvalidInput)
	})
	t.Run("a bundle without an executable is refused", func(t *testing.T) {
		bundle := portableBundle(t, t.TempDir(), WidgetBundleName)
		if err := os.Chmod(widgetExecutablePath(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := resolveWidgetBundle(WidgetConfig{Candidates: []string{bundle}})
		wantKind(t, err, cascade.KindNotFound)
		if err := os.Remove(widgetExecutablePath(bundle)); err != nil {
			t.Fatal(err)
		}
		_, err = resolveWidgetBundle(WidgetConfig{Candidates: []string{bundle}})
		wantKind(t, err, cascade.KindNotFound)
	})
}

func TestWidgetPlistPath(t *testing.T) {
	got := widgetPlistPath(filepath.Join("h", "me"))
	want := filepath.Join("h", "me", "Library", "LaunchAgents", WidgetLabel+".plist")
	if got != want {
		t.Fatalf("plist path = %q, want %q", got, want)
	}
}

func TestJoinPaths(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "a,b": "a and b", "a,b,c": "a and b and c"} {
		var paths []string
		if in != "" {
			paths = strings.Split(in, ",")
		}
		if got := joinPaths(paths); got != want {
			t.Errorf("joinPaths(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateWidgetInstall(t *testing.T) {
	full := newPortableConfig(t)
	if err := validateWidgetInstall(full); err != nil {
		t.Fatalf("complete config refused: %v", err)
	}
	cases := map[string]func(*WidgetConfig){
		"home":       func(c *WidgetConfig) { c.HomeDir = "" },
		"executable": func(c *WidgetConfig) { c.Executable = "" },
		"paths":      func(c *WidgetConfig) { c.Paths = nil },
		"clock":      func(c *WidgetConfig) { c.Clock = nil },
		"runner":     func(c *WidgetConfig) { c.Runner = nil },
	}
	for name, mutate := range cases {
		cfg := full
		mutate(&cfg)
		if err := validateWidgetInstall(cfg); err == nil {
			t.Errorf("%s missing: install accepted", name)
		} else {
			wantKind(t, err, cascade.KindInvalidInput)
		}
	}
}

func TestReadWidgetSelfReport(t *testing.T) {
	t.Run("empty path does nothing", func(t *testing.T) {
		var rep WidgetStatusReport
		readWidgetSelfReport("", &rep)
		if rep.StatusPath != "" || rep.SelfReport != nil || rep.SelfReportError != "" {
			t.Fatalf("report touched: %+v", rep)
		}
	})
	t.Run("absent file is not an error", func(t *testing.T) {
		sp := filepath.Join(t.TempDir(), "widget-status.json")
		var rep WidgetStatusReport
		readWidgetSelfReport(sp, &rep)
		if rep.StatusPath != sp || rep.SelfReport != nil || rep.SelfReportError != "" {
			t.Fatalf("absent file report: %+v", rep)
		}
	})
	t.Run("unreadable path is reported", func(t *testing.T) {
		var rep WidgetStatusReport
		readWidgetSelfReport(t.TempDir(), &rep) // a directory cannot be read as a file
		if rep.SelfReportError == "" || rep.SelfReport != nil {
			t.Fatalf("unreadable path not reported: %+v", rep)
		}
	})
	t.Run("malformed file is reported", func(t *testing.T) {
		sp := filepath.Join(t.TempDir(), "widget-status.json")
		if err := os.WriteFile(sp, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		var rep WidgetStatusReport
		readWidgetSelfReport(sp, &rep)
		if !strings.HasPrefix(rep.SelfReportError, "parse "+sp+": ") || rep.SelfReport != nil {
			t.Fatalf("malformed file report: %+v", rep)
		}
	})
	t.Run("good file fills the report and classifies placement", func(t *testing.T) {
		sp := filepath.Join(t.TempDir(), "widget-status.json")
		body := `{"bundle_path":"/elsewhere/` + WidgetBundleName + `","written_at":"2026-10-12T08:30:00Z"}`
		if err := os.WriteFile(sp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		rep := WidgetStatusReport{BundlePath: "/Applications/" + WidgetBundleName}
		readWidgetSelfReport(sp, &rep)
		if rep.SelfReport == nil || rep.SelfReportError != "" || rep.Placement != PlacementMoved {
			t.Fatalf("good file report: %+v", rep)
		}
	})
}

func TestExecOutputRunner(t *testing.T) {
	echo, err := exec.LookPath("echo")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no echo binary")
	}
	out, err := ExecOutputRunner{}.Output(echo, "widget")
	if err != nil || strings.TrimSpace(string(out)) != "widget" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if _, err := (ExecOutputRunner{}).Output(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing command did not fail")
	}
}
