// Purpose: coverage for the second marker-scan branch: a `.nself` directory
//
//	is a project when its PARENT directory holds one of nself's own project
//	files (nself 1.3.5 `init --non-interactive` writes an EMPTY `.nself/`
//	beside `.env`). Every case runs the real Detect over a fake tree with a
//	probe that always fails, so only the marker scan can detect.
//
// SPORT: plugins/nself detect (TEST) — P1-PLG-13.

package nself

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeHome is a resolved home directory that is not an ancestor of any fake
// tree below: the parent-marker branch only runs when the home is resolved.
var fakeHome = filepath.FromSlash("/Users/someone")

// detectFake runs the real Detect over a fake tree with a probe that always
// fails, so a "not detected" answer cannot come from the subprocess leg and
// a detection can only come from the marker scan. It returns the runner so
// a test can assert the probe was (not) forked.
func detectFake(t *testing.T, fs fakeFS, root, home string) (result, *recordingRunner) {
	t.Helper()
	rec := &recordingRunner{err: errors.New("probe: not a project")}
	d := &detector{fs: fs, runner: rec, binary: nselfBinary, rootDir: root, homeDir: home}
	return d.Detect(context.Background()), rec
}

// freshInit lays down what `nself init --non-interactive` writes in dir
// (testdata/README.md § fresh init): an EMPTY `.nself/` plus these files.
func (f fakeFS) freshInit(dir string, files ...string) fakeFS {
	f.dirs[filepath.Join(dir, markerDirName)] = true
	for _, name := range files {
		f.files[filepath.Join(dir, name)] = true
	}
	return f
}

// TestDetectFreshInitProject: nself's own marker rule, not the build
// artefacts, makes a freshly initialised project (empty .nself/ with a .env
// beside it) detect; the same tree without the .env does not.
func TestDetectFreshInitProject(t *testing.T) {
	root := filepath.FromSlash("/work/fresh")
	with := newFakeFS().freshInit(root, ".env", ".env.example", ".env.secrets", ".gitignore")
	got, rec := detectFake(t, with, root, fakeHome)
	if want := filepath.Join(root, markerDirName); !got.Detected || got.Method != "marker-dir" || got.MarkerPath != want {
		t.Fatalf("Detect(fresh init) = %+v, want marker-dir at %s", got, want)
	}
	if rec.calls != 0 {
		t.Fatalf("probe forked %d times, want 0: the marker scan alone must answer", rec.calls)
	}
	without := newFakeFS().freshInit(root, ".env.example", ".env.secrets", ".gitignore")
	if got, _ := detectFake(t, without, root, fakeHome); got.Detected || got.Method != "none" {
		t.Fatalf("Detect(.nself without .env) = %+v, want not detected (method none)", got)
	}
}

// TestDetectInitMarkerFiles pins nself's four project markers (each beside
// an empty .nself/) and the shapes that must NOT count: the never-committed
// overlays nself excludes, and a `.env` that is a directory (a virtualenv).
func TestDetectInitMarkerFiles(t *testing.T) {
	root := filepath.FromSlash("/work/p")
	for _, name := range []string{".env", ".env.dev", ".env.staging", ".env.prod"} {
		if got, _ := detectFake(t, newFakeFS().freshInit(root, name), root, fakeHome); !got.Detected || got.Method != "marker-dir" {
			t.Errorf("empty .nself beside %s: %+v, want marker-dir", name, got)
		}
	}
	for _, name := range []string{".env.secrets", ".env.local", ".env.example", "env", ".gitignore"} {
		if got, _ := detectFake(t, newFakeFS().freshInit(root, name), root, fakeHome); got.Detected {
			t.Errorf("empty .nself beside %s: %+v, want not detected", name, got)
		}
	}
	venv := newFakeFS().freshInit(root)
	venv.dirs[filepath.Join(root, ".env")] = true
	if got, _ := detectFake(t, venv, root, fakeHome); got.Detected {
		t.Errorf("empty .nself beside a .env DIRECTORY: %+v, want not detected", got)
	}
}

// TestDetectHomeNselfStillRejected: the home bound wins over the parent
// marker branch. $HOME/.nself (nself's state dir) with a $HOME/.env beside
// it never detects; a project under the same home with the same layout does.
func TestDetectHomeNselfStillRejected(t *testing.T) {
	home := filepath.FromSlash("/Users/someone")
	fs := newFakeFS().freshInit(home, ".env")
	fs.dirs[filepath.Join(home, markerDirName, "bin")] = true
	fs.dirs[filepath.Join(home, markerDirName, "cache")] = true
	if got, _ := detectFake(t, fs, filepath.Join(home, "Downloads"), home); got.Detected {
		t.Fatalf("Detect($HOME/Downloads) = %+v, want not detected: $HOME/.nself is state, not a project", got)
	}
	proj := filepath.Join(home, "proj")
	fs.freshInit(proj, ".env")
	if got, _ := detectFake(t, fs, proj, home); !got.Detected || got.Method != "marker-dir" {
		t.Fatalf("Detect($HOME/proj) = %+v, want marker-dir (positive control)", got)
	}
}

// TestDetectUnresolvedHomeFailsClosed: with no resolvable home the
// parent-marker branch never detects (nothing to bound $HOME/.nself against),
// while the five-artefact rule still does.
func TestDetectUnresolvedHomeFailsClosed(t *testing.T) {
	root := filepath.FromSlash("/work/fresh")
	got, _ := detectFake(t, newFakeFS().freshInit(root, ".env"), root, "")
	if got.Detected {
		t.Fatalf("Detect(fresh init, home unresolved) = %+v, want not detected", got)
	}
	home := filepath.FromSlash("/Users/someone")
	state := newFakeFS().freshInit(home, ".env")
	if got, _ := detectFake(t, state, filepath.Join(home, "sub"), ""); got.Detected {
		t.Fatalf("Detect($HOME/sub, home unresolved) = %+v, want not detected", got)
	}
	built := newFakeFS().project(root)
	if got, _ := detectFake(t, built, root, ""); !got.Detected {
		t.Fatalf("Detect(built project, home unresolved) = %+v, want detected (five-artefact rule)", got)
	}
}

// TestProjectMarkerAtRefusesHome reaches the per-directory home refusal on
// its own (the walk's own stop never lets scanAncestors get there): the home
// directory with a `.nself` and a `.env` beside it must not match, a sibling
// with the same layout must.
func TestProjectMarkerAtRefusesHome(t *testing.T) {
	home := filepath.FromSlash("/Users/someone")
	fs := newFakeFS().freshInit(home, ".env")
	if ok, err := projectMarkerAt(fs, home, home); ok || err != nil {
		t.Fatalf("projectMarkerAt(home, home) = (%v, %v), want (false, nil)", ok, err)
	}
	other := filepath.Join(home, "proj")
	fs.freshInit(other, ".env")
	if ok, err := projectMarkerAt(fs, other, home); !ok || err != nil {
		t.Fatalf("projectMarkerAt(proj, home) = (%v, %v), want (true, nil) (positive control)", ok, err)
	}
}

// homeStateTree builds, on the real filesystem under a fresh temp dir, a
// home directory named name holding nself's state dir (.nself/bin) and a
// .env beside it, plus a project directory below it. It returns the home
// path and a sibling project path (with its own fresh init) for the control.
func homeStateTree(t *testing.T, name string) (home, sub, proj string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), name)
	sub = filepath.Join(home, "sub")
	proj = filepath.Join(home, "proj")
	for _, dir := range []string{filepath.Join(home, markerDirName, "bin"), sub} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatalf("write home .env: %v", err)
	}
	materializeFreshInit(t, proj)
	return home, sub, proj
}

// detectReal runs the real os.Stat scan with the given HOME spelling.
func detectReal(root, home string) result {
	rec := &recordingRunner{err: errors.New("probe: not a project")}
	d := &detector{fs: osStatFS{}, runner: rec, binary: nselfBinary, rootDir: root, homeDir: home}
	return d.Detect(context.Background())
}

// TestDetectSymlinkedHomeRejected: HOME spelled as a symlink to the real home
// directory still bounds the walk by identity, so $HOME/.nself + $HOME/.env
// never detects from below it; a project beside it still does.
func TestDetectSymlinkedHomeRejected(t *testing.T) {
	realHome, sub, proj := homeStateTree(t, "real")
	link := filepath.Join(filepath.Dir(realHome), "link")
	if err := os.Symlink(realHome, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := detectReal(sub, link); got.Detected {
		t.Fatalf("Detect(real/sub, HOME=symlink) = %+v, want not detected", got)
	}
	if got := detectReal(proj, link); !got.Detected || got.Method != "marker-dir" {
		t.Fatalf("Detect(real/proj, HOME=symlink) = %+v, want marker-dir (positive control)", got)
	}
}

// TestDetectCaseDifferentHomeRejected: on a case-insensitive filesystem
// (macOS default) HOME spelled in a different case is the same directory.
func TestDetectCaseDifferentHomeRejected(t *testing.T) {
	realHome, sub, _ := homeStateTree(t, "Homedir")
	odd := filepath.Join(filepath.Dir(realHome), "hOMEDIR")
	if _, err := os.Stat(odd); err != nil {
		t.Skipf("case-sensitive filesystem (%s): %v", runtime.GOOS, err)
	}
	if got := detectReal(sub, odd); got.Detected {
		t.Fatalf("Detect(sub, HOME=case-different) = %+v, want not detected", got)
	}
}
