// Purpose: unit coverage for the marker scan's SEMANTICS and BOUNDS — the
//
//	two defects the adversarial review proved against the real filesystem
//	(a walk that ran to "/" and a `.nself` match that accepted nself's own
//	global state directory), plus the pinned decision for an unreadable
//	ancestor. Every case here runs against a fake tree; the real-filesystem
//	re-run of the review's own three inputs is the env-gated probe at the
//	bottom of this file.
//
// SPORT: plugins/nself detect (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeFS is a statFS over an in-memory tree: dirs and files are separate
// sets, so a test can make `.nself` a FILE and prove it is refused.
type fakeFS struct {
	dirs  map[string]bool
	files map[string]bool
	// statErr is returned for these exact paths, standing in for EACCES.
	statErr map[string]error
}

func (f fakeFS) Stat(path string) (bool, bool, error) {
	if err := f.statErr[path]; err != nil {
		return false, false, err
	}
	if f.dirs[path] {
		return true, true, nil
	}
	return f.files[path], false, nil
}

func newFakeFS() fakeFS {
	return fakeFS{dirs: map[string]bool{}, files: map[string]bool{}, statErr: map[string]error{}}
}

// project registers a real nself project layout at dir.
func (f fakeFS) project(dir string) fakeFS {
	f.dirs[filepath.Join(dir, markerDirName)] = true
	f.files[filepath.Join(dir, markerDirName, "build-version")] = true
	return f
}

// recordingRunner records the arguments of the one Run it is given.
type recordingRunner struct {
	dir, binary string
	args        []string
	calls       int
	out         []byte
	err         error
}

func (r *recordingRunner) Run(_ context.Context, dir, binary string, args []string, _ time.Duration) ([]byte, error) {
	r.calls++
	r.dir, r.binary, r.args = dir, binary, args
	return r.out, r.err
}

func TestScanAncestors_FindsRealBackendLayout(t *testing.T) {
	fs := newFakeFS().project("/repo/backend")
	got, err := scanAncestors(fs, "/repo/backend/apps/web", "/Users/someone")
	if err != nil {
		t.Fatalf("scanAncestors err = %v, want nil", err)
	}
	if want := filepath.FromSlash("/repo/backend/.nself"); got != want {
		t.Fatalf("scanAncestors = %q, want %q", got, want)
	}
}

// TestScanAncestors_RefusesHomeGlobalStateDir is the review's second input
// verbatim: a root under the home directory while $HOME/.nself exists (it
// does on the build machine: bin/, cache/, license/, plugins/). The draft
// reported detected=true method=marker-file marker=$HOME for every
// directory under the home directory.
func TestScanAncestors_RefusesHomeGlobalStateDir(t *testing.T) {
	const home = "/Users/someone"
	fs := newFakeFS().project(home) // the global state dir, dressed as a project
	got, err := scanAncestors(fs, filepath.Join(home, "Downloads"), home)
	if got != "" || err != nil {
		t.Fatalf("scanAncestors = (%q, %v), want (\"\", nil): $HOME/.nself is nself's own state dir", got, err)
	}
}

func TestScanAncestors_StopsAtHome(t *testing.T) {
	const home = "/Users/someone"
	fs := newFakeFS().project("/Users") // above the bound
	got, _ := scanAncestors(fs, filepath.Join(home, "code", "app"), home)
	if got != "" {
		t.Fatalf("scanAncestors = %q, want \"\": the walk must stop at the home directory", got)
	}
}

func TestScanAncestors_StopsAtVCSRoot(t *testing.T) {
	fs := newFakeFS().project("/srv")
	fs.dirs[filepath.FromSlash("/srv/repo/.git")] = true
	got, _ := scanAncestors(fs, "/srv/repo/apps/web", "")
	if got != "" {
		t.Fatalf("scanAncestors = %q, want \"\": the walk must stop at the repository root", got)
	}
}

func TestScanAncestors_VCSRootItselfIsStillExamined(t *testing.T) {
	fs := newFakeFS().project("/srv/repo")
	fs.dirs[filepath.FromSlash("/srv/repo/.git")] = true
	if got, _ := scanAncestors(fs, "/srv/repo/apps", ""); got != filepath.FromSlash("/srv/repo/.nself") {
		t.Fatalf("scanAncestors = %q, want the repository root's own marker", got)
	}
}

// TestScanAncestors_RequiresProjectFileInsideMarkerDir is the real
// ~/Sites/<project>/.nself case: a `.nself` directory holding something
// else entirely (a pipelines/ directory) is not a project.
func TestScanAncestors_RequiresProjectFileInsideMarkerDir(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["/repo/.nself"] = true
	fs.dirs["/repo/.nself/pipelines"] = true
	if got, _ := scanAncestors(fs, "/repo", ""); got != "" {
		t.Fatalf("scanAncestors = %q, want \"\": a .nself with no project file is not a project", got)
	}
}

func TestScanAncestors_MarkerMustBeADirectory(t *testing.T) {
	fs := newFakeFS()
	fs.files["/repo/.nself"] = true
	if got, _ := scanAncestors(fs, "/repo", ""); got != "" {
		t.Fatalf("scanAncestors = %q, want \"\": a .nself FILE is not the marker", got)
	}
}

// TestScanAncestors_NselfTomlIsNotAMarker pins the deleted invention: no
// nself.toml exists anywhere in a real nself project.
func TestScanAncestors_NselfTomlIsNotAMarker(t *testing.T) {
	fs := newFakeFS()
	fs.files["/repo/nself.toml"] = true
	if got, _ := scanAncestors(fs, "/repo", ""); got != "" {
		t.Fatalf("scanAncestors = %q, want \"\": nself.toml is not an nself marker", got)
	}
}

// TestScanAncestors_ContinuesPastUnreadableAncestor pins the decision for
// the branch the review's mutation proved untested: an EACCES ancestor does
// NOT stop detection, and the failure is still reported.
func TestScanAncestors_ContinuesPastUnreadableAncestor(t *testing.T) {
	fs := newFakeFS().project("/a")
	fs.statErr[filepath.FromSlash("/a/b/.nself")] = syscall.EACCES
	got, err := scanAncestors(fs, "/a/b/c", "")
	if want := filepath.FromSlash("/a/.nself"); got != want {
		t.Fatalf("scanAncestors = %q, want %s: the walk must continue past an unreadable ancestor", got, want)
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("scanAncestors err = %v, want the EACCES it walked past to be reported", err)
	}
}

func TestScanAncestors_AllUnreadableReportsRatherThanPanics(t *testing.T) {
	fs := newFakeFS()
	for _, p := range []string{
		filepath.FromSlash("/a/b/.nself"),
		filepath.FromSlash("/a/.nself"),
		filepath.FromSlash("/.nself"),
	} {
		fs.statErr[p] = syscall.EACCES
	}
	got, err := scanAncestors(fs, "/a/b", "")
	if got != "" || !errors.Is(err, syscall.EACCES) {
		t.Fatalf("scanAncestors = (%q, %v), want (\"\", EACCES)", got, err)
	}
}

// TestScanAncestors_DetectsAProjectInsideHome pins the POSITIVE direction the
// confirming review's mutation proved untested: with $HOME's own marker
// refused by PREFIX rather than by exact path, every project under the home
// directory (~/code/app, ~/Sites/app) silently stops detecting, and the whole
// repository suite stayed green. It runs against the REAL filesystem and the
// real os.Stat under a temp home — no environment variable is touched,
// because both bounds are injected.
func TestScanAncestors_DetectsAProjectInsideHome(t *testing.T) {
	home := t.TempDir()
	writeProjectMarker(t, filepath.Join(home, markerDirName)) // $HOME/.nself, dressed as a project
	proj := filepath.Join(home, "proj")
	writeProjectMarker(t, filepath.Join(proj, markerDirName))

	fsys := osStatFS{}
	got, err := scanAncestors(fsys, filepath.Join(proj, "apps", "web"), home)
	if want := filepath.Join(proj, markerDirName); got != want || err != nil {
		t.Fatalf("scanAncestors = (%q, %v), want (%q, nil): a project INSIDE $HOME must still detect", got, err, want)
	}
	if ok, err := projectMarkerAt(fsys, proj, home); !ok || err != nil {
		t.Fatalf("projectMarkerAt($HOME/proj) = (%v, %v), want (true, nil)", ok, err)
	}
	// …while the home directory's own marker stays refused, by exact path.
	if ok, err := projectMarkerAt(fsys, home, home); ok || err != nil {
		t.Fatalf("projectMarkerAt($HOME) = (%v, %v), want (false, nil): that is nself's global state dir", ok, err)
	}
	if got, _ := scanAncestors(fsys, filepath.Join(home, "Downloads"), home); got != "" {
		t.Fatalf("scanAncestors = %q, want \"\": $HOME/.nself is never a project", got)
	}
}

// writeProjectMarker creates a real `.nself` directory holding a real
// project file at markerDir.
func writeProjectMarker(t *testing.T, markerDir string) {
	t.Helper()
	if err := os.MkdirAll(markerDir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", markerDir, err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, projectFiles[0]), []byte("1.3.5\n"), 0o600); err != nil {
		t.Fatalf("write project file: %v", err)
	}
}

func TestScanAncestors_EmptyStartIsNotADetection(t *testing.T) {
	if got, err := scanAncestors(newFakeFS(), "", ""); got != "" || err != nil {
		t.Fatalf("scanAncestors(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}
}
