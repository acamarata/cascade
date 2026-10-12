//go:build darwin

// Purpose: shared fixture for the darwin widget installer tests - an
//
//	injected HOME and system root under one symlink-resolved t.TempDir, a
//	symlinked cascade executable, a CascadeMenuBar.app bundle, a PathProvider
//	rooted in that HOME, a recording Runner and a canned OutputRunner.
//
// Constraints: nothing here touches the real HOME, ~/Library or launchctl.
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cruntime "github.com/acamarata/cascade/internal/runtime"
)

// fakeOutput is an OutputRunner returning canned output.
type fakeOutput struct {
	out   []byte
	err   error
	calls []string
}

func (f *fakeOutput) Output(name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	f.calls = append(f.calls, key)
	return f.out, f.err
}

// widgetFixture is one installer scenario.
type widgetFixture struct {
	root, home, sysRoot string
	bundle              string // the ~/Applications bundle
	realCLI             string
	cfg                 WidgetConfig
	runner              *fakeRunner
	output              *fakeOutput
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// mustBundle creates bundle with a regular executable Contents/MacOS file.
func mustBundle(t *testing.T, bundle string) {
	t.Helper()
	mustMkdir(t, filepath.Join(bundle, "Contents", "MacOS"))
	exe := widgetExecutablePath(bundle)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test executable stand-in
		t.Fatalf("write %s: %v", exe, err)
	}
}

// newWidgetFixture builds the fixture with a bundle in <home>/Applications.
func newWidgetFixture(t *testing.T) *widgetFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	f := &widgetFixture{root: root, home: filepath.Join(root, "home"), sysRoot: filepath.Join(root, "sys")}
	f.bundle = filepath.Join(f.home, "Applications", WidgetBundleName)
	mustBundle(t, f.bundle)
	mustMkdir(t, filepath.Join(f.home, "bin"))
	f.realCLI = filepath.Join(f.home, "bin", "cascade-real")
	if err := os.WriteFile(f.realCLI, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test executable stand-in
		t.Fatal(err)
	}
	link := filepath.Join(f.home, "bin", "cascade")
	if err := os.Symlink(f.realCLI, link); err != nil {
		t.Fatal(err)
	}
	paths, err := cruntime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return f.home, nil })
	if err != nil {
		t.Fatal(err)
	}
	f.runner, f.output = &fakeRunner{}, &fakeOutput{}
	f.cfg = WidgetConfig{
		HomeDir: f.home, SystemRoot: f.sysRoot, UID: 501, Executable: link, Paths: paths,
		Getenv: func(string) string { return "" },
		Clock:  cruntime.NewFixedClock(time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)),
		Runner: f.runner, Output: f.output,
	}
	return f
}
