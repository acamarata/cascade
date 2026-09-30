//go:build doctruthrelease

package build

import "testing"

// TestDocTruth_Release runs behind the doctruthrelease tag (never in the
// default `go test ./...` gate, per the packet's ship_requires: release
// mode is the Phase-completion check, not a per-commit gate). It proves
// release mode reports every baselined finding as New — the baseline is
// honest, not silently widening what release mode accepts.
func TestDocTruth_Release(t *testing.T) {
	root := sweepModuleRoot(t)
	baseline, err := loadBaseline(root)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}
	if len(baseline) == 0 {
		t.Fatal("baseline is empty; this test needs a non-empty baseline to prove release mode ignores it")
	}
	rep, err := CheckDocTruth(root, DocTruthRelease)
	if err != nil {
		t.Fatalf("CheckDocTruth: %v", err)
	}
	if len(rep.New) != len(baseline) {
		t.Errorf("release mode: New=%d, baseline=%d, want equal (every baselined finding still reproduces and release ignores the baseline)",
			len(rep.New), len(baseline))
	}
}
