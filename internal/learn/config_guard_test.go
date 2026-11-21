package learn

import (
	"os"
	"testing"
)

// TestRequireIsolatedHomeAcceptsGOTMPDIRRoot verifies that the home guard
// accepts a HOME directory created under $GOTMPDIR even when it is outside
// os.TempDir(), and still refuses a HOME outside both roots.
func TestRequireIsolatedHomeAcceptsGOTMPDIRRoot(t *testing.T) {
	gotmpdir, err := os.MkdirTemp(".", "gotmpdir-outside")
	if err != nil {
		t.Fatalf("failed to create gotmpdir: %v", err)
	}
	defer os.RemoveAll(gotmpdir)

	homeDir, err := os.MkdirTemp(gotmpdir, "home-under-gotmpdir")
	if err != nil {
		t.Fatalf("failed to create home dir under GOTMPDIR: %v", err)
	}

	t.Setenv("GOTMPDIR", gotmpdir)
	for _, k := range homeVars {
		t.Setenv(k, homeDir)
	}

	requireIsolatedHome(t, homeDir)

	baseOutside, err := os.MkdirTemp(".", "outside-home-base")
	if err != nil {
		t.Fatalf("failed to create outside base: %v", err)
	}
	defer os.RemoveAll(baseOutside)

	outsideHome, err := os.MkdirTemp(baseOutside, "outside-home")
	if err != nil {
		t.Fatalf("failed to create outside home: %v", err)
	}

	for _, k := range homeVars {
		t.Setenv(k, outsideHome)
	}

	for _, k := range homeVars {
		v := os.Getenv(k)
		if isAllowedTempHome(v, outsideHome) {
			t.Fatalf("guard should have refused HOME %q under %q", v, outsideHome)
		}
	}
}
