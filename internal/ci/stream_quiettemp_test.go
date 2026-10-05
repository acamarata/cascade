// Purpose: the temp directory helper for tests that run the go toolchain
// directly, outside the clean room.
//
// SPORT: internal.ci.cleanRoomEnv/TESTED (P1-CI-01).
package ci

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// realTempRoot is the temp directory before any test redirects TMPDIR.
var realTempRoot = os.TempDir()

// quietTemp is a temp directory for a go command's HOME, cache or scratch
// space. Go telemetry is turned off inside it, so the toolchain writes
// nothing after its command exits and one RemoveAll removes it. Module-cache
// directories are read-only, so permissions are restored before the single
// removal; a failure is a test failure, never retried or ignored.
func quietTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(realTempRoot, "ci-quiet-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if err := disableGoTelemetry(runtime.GOOS, dir); err != nil {
		t.Fatalf("disableGoTelemetry: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("removing %s: %v", dir, err)
		}
	})
	return dir
}
