//go:build !windows

package main

import (
	"os"
	"testing"
)

// makeDocsDirUnwritable removes the owner's write access to dir and restores
// it in t.Cleanup so t.TempDir can remove it. Callers skip under root, which
// ignores mode bits. The windows build uses a deny ACE instead.
func makeDocsDirUnwritable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
