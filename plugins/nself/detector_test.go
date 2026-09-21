// Purpose: the detector's own behaviour — how a probe outcome folds into a
//
//	result, that the probe is told which directory to inspect, the memo and
//	its invalidation, and the two production defaults (the real os.Stat
//	filesystem and the real home-directory bound). The marker scan's
//	semantics and bounds are pinned next door in detect_test.go, which also
//	declares the fakeFS and recordingRunner doubles used here.
//
// SPORT: plugins/nself detect (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// TestDetect_EveryProbeFailureFoldsToNotDetected is the review's FIRST
// input generalized: `nself status --json` in a non-project directory RUNS
// and exits 1. The draft propagated that as a tool error, so
// nself_project_info failed on every non-nself workspace of a machine with
// nself installed. Detect now has no error return at all.
func TestDetect_EveryProbeFailureFoldsToNotDetected(t *testing.T) {
	cases := map[string]error{
		"ran and exited non-zero": &probeFailedError{Binary: "nself", ExitCode: 1},
		"binary absent":           &binaryAbsentError{Binary: "nself", GOOS: "darwin"},
		"timed out":               &probeTimeoutError{Binary: "nself", Timeout: probeTimeout},
		"could not start":         errors.New("permission denied"),
	}
	for name, probeErr := range cases {
		t.Run(name, func(t *testing.T) {
			d := &detector{fs: newFakeFS(), runner: &recordingRunner{err: probeErr},
				binary: "nself", rootDir: "/private/tmp"}
			res := d.Detect(context.Background())
			if res.Detected || res.Method != "none" {
				t.Fatalf("Detect = %+v, want {Detected:false Method:none}", res)
			}
			if !errors.Is(res.ProbeErr, probeErr) {
				t.Fatalf("Detect ProbeErr = %v, want the typed outcome kept for the doctor probe", res.ProbeErr)
			}
		})
	}
}

// TestDetect_PassesRootDirToTheProbe is the review's THIRD input at unit
// level: the probe must be told which directory to run in.
func TestDetect_PassesRootDirToTheProbe(t *testing.T) {
	runner := &recordingRunner{out: []byte(`{}`)}
	d := &detector{fs: newFakeFS(), runner: runner, binary: "nself", rootDir: "/tmp/empty"}
	if res := d.Detect(context.Background()); !res.Detected || res.Method != "subprocess" {
		t.Fatalf("Detect = %+v, want a subprocess detection on a zero-exit probe", res)
	}
	if runner.dir != "/tmp/empty" {
		t.Fatalf("probe ran with dir = %q, want the scanned root /tmp/empty", runner.dir)
	}
	if len(runner.args) != 2 || runner.args[0] != "status" || runner.args[1] != "--json" {
		t.Fatalf("probe args = %v, want [status --json] (the verb the installed CLI has)", runner.args)
	}
}

func TestDetect_MarkerHitSkipsTheProbeEntirely(t *testing.T) {
	runner := &recordingRunner{err: errors.New("must never run")}
	d := &detector{fs: newFakeFS().project("/repo"), runner: runner, binary: "nself", rootDir: "/repo"}
	res := d.Detect(context.Background())
	if !res.Detected || res.Method != "marker-dir" || res.MarkerPath != "/repo/.nself" || runner.calls != 0 {
		t.Fatalf("Detect = %+v, runner.calls = %d; want a marker-dir hit and no subprocess", res, runner.calls)
	}
}

func TestDetect_MemoizesAndInvalidates(t *testing.T) {
	runner := &recordingRunner{out: []byte(`{}`)}
	d := &detector{fs: newFakeFS(), runner: runner, binary: "nself", rootDir: "/repo"}
	d.Detect(context.Background())
	d.Detect(context.Background())
	if runner.calls != 1 {
		t.Fatalf("probe ran %d times across two Detect calls, want 1 (memoized)", runner.calls)
	}
	d.invalidate()
	d.Detect(context.Background())
	if runner.calls != 2 {
		t.Fatalf("probe ran %d times after invalidate, want 2 (re-run)", runner.calls)
	}
}

func TestNewDetector_UsesTheActiveRunnerAndRealHome(t *testing.T) {
	runner := &recordingRunner{err: &probeFailedError{Binary: "nself", ExitCode: 1}}
	orig := activeRunner
	activeRunner = runner
	t.Cleanup(func() { activeRunner = orig })

	d := newDetector(t.TempDir())
	if d.runner != subprocessRunner(runner) {
		t.Fatal("newDetector did not take the active runner")
	}
	home, err := os.UserHomeDir()
	if err == nil && d.homeDir != home {
		t.Fatalf("newDetector homeDir = %q, want the real home %q", d.homeDir, home)
	}
	if res := d.Detect(context.Background()); res.Detected {
		t.Fatalf("Detect over a fresh temp dir = %+v, want detected=false", res)
	}
}

// TestOsStatFS_RealFilesystemOutcomes exercises the REAL statFS: a missing
// path is "does not exist" and not an error, a directory reports isDir, and
// a path THROUGH a regular file is a real stat failure (ENOTDIR) rather than
// a silent miss — the difference the scan's ScanErr branch depends on.
func TestOsStatFS_RealFilesystemOutcomes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if exists, isDir, err := (osStatFS{}).Stat(filepath.Join(dir, "nope")); exists || isDir || err != nil {
		t.Errorf("Stat(missing) = (%v, %v, %v), want (false, false, nil)", exists, isDir, err)
	}
	if exists, isDir, err := (osStatFS{}).Stat(dir); !exists || !isDir || err != nil {
		t.Errorf("Stat(dir) = (%v, %v, %v), want (true, true, nil)", exists, isDir, err)
	}
	if exists, isDir, err := (osStatFS{}).Stat(file); !exists || isDir || err != nil {
		t.Errorf("Stat(file) = (%v, %v, %v), want (true, false, nil)", exists, isDir, err)
	}
	_, _, err := (osStatFS{}).Stat(filepath.Join(file, "through-a-file"))
	if err == nil {
		t.Error("Stat(path through a regular file) = nil error, want the real stat failure")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(path through a regular file) err = %v, want it NOT classified as not-exist", err)
	}
}

// TestNewDetector_UnresolvableHomeWidensTheBoundRatherThanFailing pins the
// behaviour on a host with no home directory.
func TestNewDetector_UnresolvableHomeWidensTheBoundRatherThanFailing(t *testing.T) {
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home on this host") }
	t.Cleanup(func() { userHomeDir = orig })

	d := newDetector("/srv/app")
	if d.homeDir != "" {
		t.Fatalf("newDetector homeDir = %q, want \"\" when the home directory cannot be resolved", d.homeDir)
	}
}

// TestScanAncestors_StopsAtTheHardLevelCap proves the third bound: a path
// deeper than maxAncestorLevels stops rather than walking forever.
func TestScanAncestors_StopsAtTheHardLevelCap(t *testing.T) {
	deep := "/"
	for i := range maxAncestorLevels + 10 {
		deep = filepath.Join(deep, "d"+strconv.Itoa(i))
	}
	fs := newFakeFS().project("/") // reachable ONLY by exhausting the cap
	got, err := scanAncestors(fs, deep, "")
	if got != "" || err != nil {
		t.Fatalf("scanAncestors(deep) = (%q, %v), want (\"\", nil): the level cap must stop the walk", got, err)
	}
}

// TestProjectMarkerAt_UnreadableProjectFileIsReported covers the stat
// failure INSIDE the marker directory.
func TestProjectMarkerAt_UnreadableProjectFileIsReported(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["/repo/.nself"] = true
	for _, name := range projectFiles {
		fs.statErr[filepath.Join("/repo/.nself", name)] = syscall.EACCES
	}
	found, err := projectMarkerAt(fs, "/repo", "")
	if found {
		t.Error("projectMarkerAt = true, want false: no project file could be read")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("projectMarkerAt err = %v, want the EACCES reported", err)
	}
}
