package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// setBuildHash overrides the package-level stamp for one test and restores
// it afterwards. BuildHash is a method over a package var rather than an
// injected field, so this is the only seam available.
func setBuildHash(t *testing.T, v string) {
	t.Helper()
	prev := buildHash
	buildHash = v
	t.Cleanup(func() { buildHash = prev })
}

// TestCheckSkew_UnstampedBuildReportsNoSkew pins the defence at the source.
// An unstamped build reports a sentinel, not a digest, and the sentinel can
// never equal a hex sum. Comparing them directly reported skew on every
// call, so a daemon built without a stamp decided it had been upgraded every
// time it was asked and relaunched itself on an ordinary shutdown signal.
//
// The composition root also declines to wire the manager into an unstamped
// build. This test covers the engine rather than that call site, because a
// guard at one call site stops working as soon as a second one appears.
func TestCheckSkew_UnstampedBuildReportsNoSkew(t *testing.T) {
	setBuildHash(t, unstampedBuildHash)
	m := &UpgradeManager{}
	skew, err := m.CheckSkew("/nonexistent/path/that/is/never/hashed")
	if err != nil {
		t.Fatalf("an unstamped build must not error, and must not reach the hash: %v", err)
	}
	if skew {
		t.Error("an unstamped build must report no skew; reporting skew makes it relaunch on every shutdown")
	}
}

// TestCheckSkew_StampedBuildStillComparesRealHashes guards the other
// direction, so the sentinel check above cannot be widened into "never
// detect skew at all".
func TestCheckSkew_StampedBuildStillComparesRealHashes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cascade")
	if err := os.WriteFile(bin, []byte("new binary contents"), 0o600); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	onDisk, err := hashFile(bin)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	setBuildHash(t, onDisk)
	m := &UpgradeManager{}
	skew, err := m.CheckSkew(bin)
	if err != nil {
		t.Fatalf("CheckSkew: %v", err)
	}
	if skew {
		t.Error("a stamped build matching the on-disk binary must report no skew")
	}

	setBuildHash(t, "0000000000000000000000000000000000000000000000000000000000000000")
	skew, err = m.CheckSkew(bin)
	if err != nil {
		t.Fatalf("CheckSkew: %v", err)
	}
	if !skew {
		t.Error("a stamped build differing from the on-disk binary must report skew")
	}
}
