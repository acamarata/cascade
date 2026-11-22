//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSocketPlaceholderRenameStaysOnOneDevice proves the bind root sits under
// GOTMPDIR when it is set, so the rename into the destination never crosses
// devices, and that the helper leaves a socket at the destination.
func TestSocketPlaceholderRenameStaysOnOneDevice(t *testing.T) {
	// Use a short temp directory as the root so that on macOS (sun_path < 104)
	// the socket path never overflows. We still prove the "stays on one device"
	// invariant by setting GOTMPDIR to this same short root.
	root, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatalf("create short root: %v", err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	t.Setenv("GOTMPDIR", root)
	if got := placeholderBindRoot(); got != root {
		t.Fatalf("bind root = %q, want %q", got, root)
	}
	dest := filepath.Join(root, "dest.sock")
	writeSocketPlaceholder(t, dest)
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("destination mode %v is not a socket", fi.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(root, "rsp*"))
	if len(left) != 0 {
		t.Fatalf("bind root not cleaned: %s", strings.Join(left, ","))
	}
}
