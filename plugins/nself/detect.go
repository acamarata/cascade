// Purpose (this file): project detection at the honest floor — an
//
//	ancestor scan for a REAL nself project marker (a `.nself` DIRECTORY
//	holding a file nself itself writes), bounded at the repository root or
//	the home directory, then (only on a miss) the bounded subprocess probe
//	probe.go runs INSIDE the scanned directory. Detection never fails: a
//	workspace that is not an nself project is an ordinary answer, not an
//	error (AC-1's "non-nself workspaces are cleanly ignored").
//
// Inputs: a statFS (real os.Stat by default, a fake in tests), a
//
//	subprocessRunner (probe.go's execRunner by default), the scan root,
//	and the home directory — injected, never read inside the scan, so a
//	test pins both bounds without touching this machine's real home.
//
// Outputs: a result reporting whether a project was detected and by which
//
//	method, plus the typed probe outcome for the doctor leg only.
//
// Constraints: this package never imports internal/** (Art.10.2,
//
//	plugins-providers-boundary depguard) in a production file.
//
// SPORT: plugins/nself detect (ADD) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// markerDirName is the ONE marker this plugin scans for. It is a
// directory, never a file, and never `nself.toml`: no such file exists
// anywhere in a real nself project (the draft this replaced invented it).
const markerDirName = ".nself"

// vcsDirName bounds the walk: the first directory that carries one is a
// repository root, and a project's marker is at or below it.
const vcsDirName = ".git"

// projectFiles are the files a real nself project's `.nself` directory
// holds. A `.nself` directory carrying NONE of them is not accepted as a
// project, which is what keeps nself's own global state directory
// ($HOME/.nself — bin/, cache/, license/, plugins/ and no project file)
// from reading as a project in every directory under the home directory.
//
// PROVENANCE (read-only directory listings of real projects on the build
// machine, 2026-09-21; recorded in testdata/README.md): four independent
// backend projects each carry build-version, compose-files.txt,
// build-state and .first-run-complete in their `.nself` directory; one
// non-backend project carries nself.yml there instead. $HOME/.nself
// carries none of the five.
var projectFiles = []string{
	"build-version",
	"compose-files.txt",
	"build-state",
	".first-run-complete",
	"nself.yml",
}

// maxAncestorLevels is a hard bound on the walk, independent of the home
// and repository bounds: a filesystem that never reports a self-parent
// cannot spin this loop.
const maxAncestorLevels = 64

// statFS abstracts the filesystem calls the marker scan needs, so
// detect_test.go proves the walk's bounds against a fake tree.
type statFS interface {
	// Stat reports whether path exists and whether it is a directory.
	// (false, false, nil) means "does not exist"; a non-nil error is any
	// other stat failure, including a permission denial.
	Stat(path string) (exists, isDir bool, err error)
}

// osStatFS is the real statFS, backed by os.Stat.
type osStatFS struct{}

func (osStatFS) Stat(path string) (bool, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, false, nil
		}
		return false, false, err
	}
	return true, info.IsDir(), nil
}

// result is one detection outcome.
type result struct {
	Detected bool
	// Method is "marker-dir", "subprocess" or "none".
	Method string
	// MarkerPath is the `.nself` directory that matched, set only when
	// Method == "marker-dir".
	MarkerPath string
	// ProbeErr is the typed subprocess outcome, for the doctor probe ONLY.
	// It is never a detection failure: the installed CLI exits non-zero in
	// any directory that is not a project, which is the ordinary case.
	ProbeErr error
	// ScanErr is the FIRST ancestor stat failure the walk hit (a
	// permission denial, typically). PINNED DECISION: the walk CONTINUES
	// past it — one unreadable ancestor says nothing about the
	// directories above it, and hard-failing would make detection depend
	// on the permissions of directories the caller never named — but it is
	// reported here rather than swallowed.
	ScanErr error
}

// detector holds one memoized detection result for a daemon session.
// Detect is safe for concurrent use; invalidate clears the memo so the
// next Detect re-runs the scan (the contract's "re-run after config
// reload").
type detector struct {
	fs      statFS
	runner  subprocessRunner
	binary  string
	rootDir string
	homeDir string

	mu     sync.Mutex
	cached *result
}

// activeRunner is the subprocessRunner every dispatch forks through: the
// real execRunner in production. Same-package test files assign it
// directly; no host bridge configures it, so there is no exported setter.
var activeRunner subprocessRunner = execRunner{}

// userHomeDir resolves the home directory the walk is bounded at. A package
// var so detect_test.go can drive the resolution-failure branch: on a host
// with no HOME the bound simply widens to the repository root, which is a
// real behaviour worth pinning rather than an impossible one.
var userHomeDir = os.UserHomeDir

// newDetector builds a detector over the real filesystem, the active
// runner and the real home directory. A home directory that cannot be
// resolved yields an empty bound, which only widens the walk to the
// repository root — it never turns into a detection failure.
func newDetector(rootDir string) *detector {
	home, err := userHomeDir()
	if err != nil {
		home = ""
	}
	return &detector{
		fs:      osStatFS{},
		runner:  activeRunner,
		binary:  nselfBinary,
		rootDir: rootDir,
		homeDir: home,
	}
}

// invalidate clears the memoized result.
func (d *detector) invalidate() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cached = nil
}

// Detect returns the memoized result, computing it on the first call (or
// the first call since the last invalidate). It has no error return by
// construction: see result.ProbeErr.
func (d *detector) Detect(ctx context.Context) result {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached != nil {
		return *d.cached
	}
	res := d.detectOnce(ctx)
	d.cached = &res
	return res
}

// detectOnce runs the marker scan, then (only on a miss) the subprocess
// probe. Every probe failure — including one that RAN and exited
// non-zero, which is exactly what the installed CLI does outside a
// project — folds to Detected=false.
func (d *detector) detectOnce(ctx context.Context) result {
	marker, scanErr := scanAncestors(d.fs, d.rootDir, d.homeDir)
	if marker != "" {
		return result{Detected: true, Method: "marker-dir", MarkerPath: marker, ScanErr: scanErr}
	}
	if _, err := d.runner.Run(ctx, d.rootDir, d.binary, probeArgs, probeTimeout); err != nil {
		return result{Method: "none", ProbeErr: err, ScanErr: scanErr}
	}
	return result{Detected: true, Method: "subprocess", ScanErr: scanErr}
}

// scanAncestors walks from start upward and returns the first `.nself`
// directory that holds one of projectFiles, plus the first stat failure it
// saw. The walk stops AT the home directory (exclusive — the home
// directory's own `.nself` is nself's global state directory, never a
// project) and at the first repository root it examines, whichever comes
// first.
func scanAncestors(fsys statFS, start, home string) (markerDir string, firstStatErr error) {
	dir := filepath.Clean(start)
	if dir == "" || dir == "." {
		return "", nil
	}
	homeDir := ""
	if home != "" {
		homeDir = filepath.Clean(home)
	}
	for range maxAncestorLevels {
		if homeDir != "" && dir == homeDir {
			return "", firstStatErr
		}
		found, err := projectMarkerAt(fsys, dir, homeDir)
		firstStatErr = firstErr(firstStatErr, err)
		if found {
			return filepath.Join(dir, markerDirName), firstStatErr
		}
		atRoot, rootErr := isVCSRoot(fsys, dir)
		firstStatErr = firstErr(firstStatErr, rootErr)
		parent := filepath.Dir(dir)
		if atRoot || parent == dir {
			return "", firstStatErr
		}
		dir = parent
	}
	return "", firstStatErr
}

// projectMarkerAt reports whether dir holds a `.nself` DIRECTORY that
// holds one of projectFiles. The home directory's own marker is refused
// outright, before any stat, so no content there can ever make it match.
func projectMarkerAt(fsys statFS, dir, homeDir string) (bool, error) {
	candidate := filepath.Join(dir, markerDirName)
	if homeDir != "" && candidate == filepath.Join(homeDir, markerDirName) {
		return false, nil
	}
	exists, isDir, err := fsys.Stat(candidate)
	if err != nil || !exists || !isDir {
		return false, err
	}
	var firstSeen error
	for _, name := range projectFiles {
		ok, _, ferr := fsys.Stat(filepath.Join(candidate, name))
		firstSeen = firstErr(firstSeen, ferr)
		if ok {
			return true, firstSeen
		}
	}
	return false, firstSeen
}

// isVCSRoot reports whether dir carries a VCS directory.
func isVCSRoot(fsys statFS, dir string) (bool, error) {
	exists, _, err := fsys.Stat(filepath.Join(dir, vcsDirName))
	return exists, err
}

// firstErr keeps the first non-nil of two errors.
func firstErr(first, next error) error {
	if first != nil {
		return first
	}
	return next
}
