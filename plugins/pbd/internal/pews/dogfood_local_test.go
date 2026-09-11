// Purpose: TestDogfoodConvertRealPlan is the ticket's non-interactive
// equivalent of the full local dogfood run (06-FORGE-SPEC §5 rule 8):
// skipped unless CASCADE_PBD_DOGFOOD_SRC names the real forged P1 tree
// (gitignored, never present in CI). It converts that real tree, proves
// the idempotent re-run, and — when CASCADE_PBD_DOGFOOD_PLAN_AUDIT names
// the authoritative .plan-audit.py script — cross-checks Convert's active
// ticket count against it. This is local verification only, documented in
// the ticket's journal, never a CI-path check (the source tree is
// gitignored so CI cannot read it).
// SPORT: plugins/pbd/internal/pews dogfood-convert (ADD) — P1-E14-W3-S30-T3.
package pews

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

func TestDogfoodConvertRealPlan(t *testing.T) {
	src := os.Getenv("CASCADE_PBD_DOGFOOD_SRC")
	if src == "" {
		t.Skip("CASCADE_PBD_DOGFOOD_SRC not set — local-only verification of the real, gitignored P1 plan tree; never run in CI")
	}
	dst := t.TempDir()

	result, err := Convert(src, dst)
	if err != nil {
		t.Fatalf("Convert(real P1 tree): %v", err)
	}
	t.Logf("Convert: %d tickets, changed=%v, %d file(s) written", result.TicketCount, result.Changed, len(result.ChangedFiles))

	second, serr := Convert(dst, dst)
	if serr != nil {
		t.Fatalf("idempotent re-run over the emitted target: %v", serr)
	}
	if second.Changed {
		t.Errorf("idempotent re-run reported changes: %v, want none", second.ChangedFiles)
	}
	if second.TicketCount != result.TicketCount {
		t.Errorf("idempotent re-run TicketCount = %d, want %d", second.TicketCount, result.TicketCount)
	}

	auditPath := os.Getenv("CASCADE_PBD_DOGFOOD_PLAN_AUDIT")
	if auditPath == "" {
		t.Log("CASCADE_PBD_DOGFOOD_PLAN_AUDIT not set, skipping the .plan-audit.py active-count cross-check")
		return
	}
	wantActive := runPlanAudit(t, auditPath)
	if result.TicketCount != wantActive {
		t.Errorf("Convert active ticket count = %d, .plan-audit.py authoritative active count = %d, MISMATCH", result.TicketCount, wantActive)
	} else {
		t.Logf(".plan-audit.py cross-check: %d active tickets, MATCH", wantActive)
	}
}

// runPlanAuditActiveCountRe matches .plan-audit.py's own summary line,
// e.g. "W1 43 · W2 52 ... = 405 active + 7 tombstoned".
var runPlanAuditActiveCountRe = regexp.MustCompile(`=\s*(\d+)\s+active`)

// runPlanAudit runs auditPath (with its own directory as cwd, since it
// opens its FILES list by relative path) and returns its reported active
// ticket count, failing the test on a non-zero exit or unparseable output.
func runPlanAudit(t *testing.T, auditPath string) int {
	t.Helper()
	cmd := exec.Command("python3", auditPath)
	cmd.Dir = filepath.Dir(auditPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf(".plan-audit.py exited non-zero: %v\n%s", err, out)
	}
	m := runPlanAuditActiveCountRe.FindSubmatch(out)
	if m == nil {
		t.Fatalf(".plan-audit.py output did not contain an active-count summary line:\n%s", out)
	}
	n, cerr := strconv.Atoi(string(m[1]))
	if cerr != nil {
		t.Fatalf("parsing .plan-audit.py active count %q: %v", m[1], cerr)
	}
	return n
}
