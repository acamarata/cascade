//go:build !windows

package secrets

import (
	"os"
	"testing"
)

// makeDirGenuinelyUnwritable removes write access to dir for the owner via
// chmod, restoring it in t.Cleanup so t.TempDir's own removal still
// succeeds. POSIX permission bits reliably block writes for an
// unprivileged process; callers must skip this helper when running as
// root (os.Geteuid() == 0), since root ignores the mode bits entirely.
func makeDirGenuinelyUnwritable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}
