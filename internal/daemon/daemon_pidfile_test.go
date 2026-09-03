package daemon

// Purpose: closes the remaining coverage gaps in daemon.go's
//   platform-independent pieces: PIDFilePath's path join, parseGraceValue's
//   full type switch (string/int64/float64/unsupported), the error
//   branches of writePIDFile/readPIDFile/removePIDFile (a target that
//   cannot os.WriteFile/os.ReadFile/os.Remove because the target path is
//   itself an unusable directory), and ResolveSettings's Extra-shape edge
//   cases the existing daemon_test.go table does not exercise (a non-nil
//   Extra map with no "daemon" key at all, and a "daemon" key present but
//   not a map). Split into its own file (not appended to daemon_test.go)
//   purely to stay clear of Art.10.3's 300-line file cap once these cases
//   are added — same package, no behaviour change.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestPIDFilePath_JoinsRootAndFileName(t *testing.T) {
	paths := fakePathsFor(t, "/tmp/socket-unused.sock")
	got := PIDFilePath(paths)
	want := filepath.Join(paths.Root(), "daemon.pid")
	if got != want {
		t.Errorf("PIDFilePath() = %q, want %q", got, want)
	}
}

// --- parseGraceValue: the full type switch ---

func TestParseGraceValue(t *testing.T) {
	cases := []struct {
		name    string
		raw     interface{}
		want    string // want.String(), only checked when wantErr is false
		wantErr bool
	}{
		{name: "valid duration string", raw: "5s", want: "5s"},
		{name: "invalid duration string", raw: "not-a-duration", wantErr: true},
		{name: "int64 seconds", raw: int64(7), want: "7s"},
		{name: "float64 seconds", raw: float64(1.5), want: "1.5s"},
		{name: "unsupported type", raw: true, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseGraceValue(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseGraceValue(%v) = %v, nil; want an error", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGraceValue(%v): %v", c.raw, err)
			}
			if got.String() != c.want {
				t.Errorf("parseGraceValue(%v) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

// --- writePIDFile / readPIDFile / removePIDFile: error branches ---

func TestWritePIDFile_UnwritablePathIsError(t *testing.T) {
	// The parent directory does not exist at all, so os.WriteFile's
	// underlying open(2) fails with ENOENT — writePIDFile must surface
	// that as a typed KindUnavailable error, not panic or silently drop
	// it.
	badPath := filepath.Join(t.TempDir(), "no-such-dir", "daemon.pid")
	err := writePIDFile(badPath, pidRecord{PID: 1})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("writePIDFile to missing dir: err = %v, want KindUnavailable", err)
	}
}

func TestReadPIDFile_UnreadablePathIsError(t *testing.T) {
	// A directory sitting where readPIDFile expects a regular file makes
	// os.ReadFile fail with something other than os.IsNotExist — the
	// "present but cannot be read" branch, distinct from "missing".
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := os.Mkdir(pidPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, ok, err := readPIDFile(pidPath)
	if ok {
		t.Fatal("readPIDFile reported ok=true against a directory")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("readPIDFile(directory): err = %v, want KindUnavailable", err)
	}
}

func TestRemovePIDFile_NonEmptyDirectoryIsError(t *testing.T) {
	// os.Remove refuses a non-empty directory (ENOTEMPTY on unix), which
	// is neither success nor os.IsNotExist — removePIDFile must surface
	// it rather than swallowing it as "already gone".
	dir := t.TempDir()
	target := filepath.Join(dir, "daemon.pid") // used as a directory here
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileHelper(filepath.Join(target, "child"), "x"); err != nil {
		t.Fatal(err)
	}
	err := removePIDFile(target)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("removePIDFile(non-empty dir): err = %v, want KindUnavailable", err)
	}
}

// --- ResolveSettings: Extra-shape edge cases the shared table omits ---

func TestResolveSettings_ExtraPresentButNoDaemonKey(t *testing.T) {
	paths := fakePathsFor(t, "/tmp/socket-default.sock")
	cfg := &runtime.Config{Extra: map[string]interface{}{"other": "section"}}
	s, err := ResolveSettings(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}
	if s.SocketPath != "/tmp/socket-default.sock" || s.GraceSet {
		t.Errorf("got %+v, want the PathProvider default and GraceSet=false", s)
	}
}

func TestResolveSettings_DaemonKeyWrongType(t *testing.T) {
	paths := fakePathsFor(t, "/tmp/socket-default.sock")
	cfg := &runtime.Config{Extra: map[string]interface{}{"daemon": "not-a-map"}}
	s, err := ResolveSettings(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}
	if s.SocketPath != "/tmp/socket-default.sock" || s.GraceSet {
		t.Errorf("got %+v, want the PathProvider default and GraceSet=false", s)
	}
}
