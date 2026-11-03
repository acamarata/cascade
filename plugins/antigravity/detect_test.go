package antigravity

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAntigravityDetectReportsAgyPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })
	for _, tc := range []struct {
		name string
		path string
		err  error
	}{
		{"present", "/x/agy", nil},
		{"absent", "", os.ErrNotExist},
		{"permission", "", os.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookPath = func(name string) (string, error) {
				if name != "agy" {
					t.Fatalf("lookup = %q, want agy", name)
				}
				return tc.path, tc.err
			}
			got, err := Detect()
			if tc.err == os.ErrPermission {
				if !errors.Is(err, tc.err) || !errors.Is(err, cascade.ErrPermissionDenied) ||
					!strings.Contains(err.Error(), tc.err.Error()) {
					t.Fatalf("permission error lost: %v", err)
				}
				if runErr := (handlers{}).RunCommand(t.Context(), cmdDetect, nil); !errors.Is(runErr, tc.err) {
					t.Fatalf("command lost permission error: %v", runErr)
				}
				return
			}
			if err != nil || got.Present != (tc.err == nil) || got.Path != tc.path {
				t.Fatalf("Detect() = %+v, %v; want path %q", got, err, tc.path)
			}
		})
	}
}

func TestDefaultLookPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", string(filepath.ListSeparator)+dir)
	t.Setenv("PATHEXT", ".EXE")
	name := "agy"
	if runtime.GOOS == "windows" {
		name += ".EXE"
	}
	path := filepath.Join(dir, name)
	if _, err := defaultLookPath("agy"); !errors.Is(err, cascade.ErrNotFound) {
		t.Fatalf("missing executable: %v", err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := defaultLookPath("agy"); err != nil || got != path {
		t.Fatalf("lookup = %q, %v; want %q", got, err, path)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })
	lookPath = defaultLookPath
	if _, err := Detect(); !errors.Is(err, os.ErrPermission) ||
		!errors.Is(err, cascade.ErrPermissionDenied) {
		t.Fatalf("non-executable file must report permission error: %v", err)
	}
}

func TestDetectPreservesUnexpectedError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })
	cause := errors.New("lookup fixture failure")
	lookPath = func(string) (string, error) { return "", cause }
	_, err := Detect()
	if !errors.Is(err, cause) || !errors.Is(err, cascade.ErrInternal) ||
		!strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("unexpected error lost its cause or kind: %v", err)
	}
}
