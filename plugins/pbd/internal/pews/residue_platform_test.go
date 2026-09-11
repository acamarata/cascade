// Purpose: TestResidueCarryForwardPlatformParity is the ticket's Art.5
// proof: residue.go touches nothing platform-conditional (path/filepath,
// os.ReadFile/WriteFile/Rename/Remove, and gopkg.in/yaml.v3 only, all of
// them already cross-platform in this same package's other files), so
// there is no unsupported-platform branch to assert a refusal for. This
// test reruns CarryForward's full behavior unconditionally — no build
// tag, no platform-gated skip — so the same assertions run and pass
// identically on every CI-matrix platform (macOS, Linux, Windows); a
// platform on which the behavior ever diverged would fail this test
// there rather than pass silently.
// SPORT: plugins/pbd/internal/pews residue-carry-forward (ADD) — P1-E14-W3-S29-T3.
package pews

import "testing"

func TestResidueCarryForwardPlatformParity(t *testing.T) {
	src, dst := newSrcDst(t)
	mkTicketFile(t, src.Root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	mkTicketFile(t, src.Root, "N", 3, 29, 2, "P1-E14-W3-S29-T2", []string{"P1-E14-W3-S29-T1"})

	if err := CarryForward(src, dst); err != nil {
		t.Fatalf("CarryForward: %v", err)
	}

	dstTree, err := NewStore(dst.Root, "P2").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("Load(dst): %v", err)
	}
	if len(dstTree.Tickets) != 2 {
		t.Fatalf("dst Tickets = %+v, want both residue tickets carried", dstTree.Tickets)
	}
	if report, verr := Validate(dstTree); verr != nil || !report.OK() {
		t.Fatalf("Validate(dst) after carry-forward: report=%+v err=%v, want a clean tree", report, verr)
	}

	srcTree, serr := NewStore(src.Root, "P1").Load()
	if serr != nil {
		t.Fatalf("Load(src): %v", serr)
	}
	if report, verr := Validate(srcTree); verr != nil || !report.OK() {
		t.Fatalf("Validate(src) after carry-forward: report=%+v err=%v, want a clean tree "+
			"(tombstoned originals never left as a live ticket file, validate.go's "+
			"ViolationTombstoneLive)", report, verr)
	}
}
