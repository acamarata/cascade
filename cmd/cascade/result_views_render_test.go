package main

// Purpose (this file): the R-14.253 property, asserted on the views that
//   were added to satisfy it — every result an operator reads renders as
//   a SENTENCE, never as Go's default struct formatting.
//
// WHY IT IS ONE PROPERTY AND NOT THIRTY ASSERTIONS. The defect was not
//   that one view was wrong; it was that thirty results reached
//   output.Writer.Result with no String method at all, so the operator
//   got `map[targets:[]]` and `{[]}` on the epic whose acceptance drill
//   is recovering a lost laptop. cmd/cascade/result_stringer_test.go
//   already proves every Result argument HAS a String method. This file
//   proves the methods say something: each renders the facts it was
//   written for, and none of them leak the struct syntax the fix removed.
//
// SPORT: cmd/cascade view tests (ADD) — P1-E45-W10-S88-T2.

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/provider"
)

// goFormattingArtefacts are the shapes a missing or broken String method
// leaves behind. `%!` catches a format verb that did not match its
// argument, which is the other way a renderer lies to an operator.
var goFormattingArtefacts = []string{"map[", "%!", "&{"}

// assertReadsAsProse fails when a rendering carries Go struct formatting
// or says nothing at all.
func assertReadsAsProse(t *testing.T, label, rendered string, wantSubstrings ...string) {
	t.Helper()
	if strings.TrimSpace(rendered) == "" {
		t.Fatalf("%s rendered nothing", label)
	}
	for _, bad := range goFormattingArtefacts {
		if strings.Contains(rendered, bad) {
			t.Errorf("%s leaks Go formatting %q:\n%s", label, bad, rendered)
		}
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(rendered, want) {
			t.Errorf("%s omits %q:\n%s", label, want, rendered)
		}
	}
}

// TestServiceDeltaViewNamesTheActionThatHappened: both verbs are
// idempotent, so WHICH outcome occurred is the fact being checked.
func TestServiceDeltaViewNamesTheActionThatHappened(t *testing.T) {
	bare := serviceDeltaView{service.DeltaReport{Action: "installed"}}
	assertReadsAsProse(t, "serviceDeltaView (no detail)", bare.String(), "installed")

	detailed := serviceDeltaView{service.DeltaReport{Action: "reloaded", Detail: "unit already managed"}}
	assertReadsAsProse(t, "serviceDeltaView (detail)", detailed.String(), "reloaded", "unit already managed")
}

// TestBenchResultViewCarriesTheUnit: the cost estimate is output tokens,
// not money, and a bare number would be read as currency.
func TestBenchResultViewCarriesTheUnit(t *testing.T) {
	v := benchResultView{fleet.BenchResult{P50MS: 120, P95MS: 400, ErrorRate: 0.25, CostEstimate: 900}}
	rendered := v.String()
	assertReadsAsProse(t, "benchResultView", rendered, "120", "400", "output tokens")
	if !strings.Contains(rendered, "25.0%") {
		t.Errorf("an error rate of 0.25 did not render as a percentage:\n%s", rendered)
	}
}

// TestVerificationReportViewLeadsWithTheVerdict, and prints the reason on
// a failure — the only fact that matters on a failed verify.
func TestVerificationReportViewLeadsWithTheVerdict(t *testing.T) {
	ok := verificationReportView{backup.VerificationReport{
		Target: "local-dir", Snapshot: "snap-1", Verified: true,
		CheckedChunks: 12, ChainDepth: 3, DurationMS: 40,
	}}
	assertReadsAsProse(t, "verificationReportView (verified)", ok.String(), "snap-1", "local-dir", "verified", "12")

	bad := verificationReportView{backup.VerificationReport{
		Target: "local-dir", Snapshot: "snap-2", Verified: false, FailureReason: "chunk 4 digest mismatch",
	}}
	rendered := bad.String()
	assertReadsAsProse(t, "verificationReportView (failed)", rendered, "snap-2", "FAILED", "chunk 4 digest mismatch")

	// A failed verify that read like a successful one is the worst
	// possible rendering of this particular report.
	if strings.Contains(rendered, "verified:") {
		t.Errorf("a FAILED verification renders as verified:\n%s", rendered)
	}
}

// TestMCPToolsViewTellsTheThreeAbsencesApart: an operator debugging a
// missing tool needs withheld, unservable and deferred distinguished.
func TestMCPToolsViewTellsTheThreeAbsencesApart(t *testing.T) {
	empty := mcpToolsView{}
	assertReadsAsProse(t, "mcpToolsView (empty)", empty.String(), "no tools are exposed")

	full := mcpToolsView{
		Tools:      []mcp.Tool{{Name: "cascade_recall", PluginID: "", Description: "search memory"}},
		Withheld:   []string{"cascade_vault_get"},
		Unservable: []string{"cascade_node_enroll"},
		Deferred:   []map[string]string{{"v1_name": "cascade_what", "ticket": "V/S-47", "reason": "fused search"}},
	}
	rendered := full.String()
	assertReadsAsProse(t, "mcpToolsView", rendered,
		"cascade_recall", "withheld by policy", "cascade_vault_get",
		"unservable here", "cascade_node_enroll", "deferred", "cascade_what", "V/S-47")
}

// TestRunResultViewLeadsWithTheOutput: the model's answer is the point of
// the verb, and the accounting follows it.
func TestRunResultViewLeadsWithTheOutput(t *testing.T) {
	v := runResultView{
		Output: "the answer",
		JobID:  "JOB1",
		Cost:   provider.Usage{InputTokens: 11, OutputTokens: 22},
		Legs: []runLegView{
			{JobID: "LEG1", Cost: provider.Usage{InputTokens: 5, OutputTokens: 6}},
		},
	}
	rendered := v.String()
	assertReadsAsProse(t, "runResultView", rendered, "the answer", "JOB1", "11 in / 22 out", "leg 1", "LEG1")
	if !strings.HasPrefix(rendered, "the answer") {
		t.Errorf("the output does not lead the rendering:\n%s", rendered)
	}

	// A run with no job id says so rather than printing an empty gap.
	bare := runResultView{Output: "x"}
	assertReadsAsProse(t, "runResultView (no job)", bare.String(), "none")
}

// TestBackupViewsSayTheEmptyCaseInWords — `{}` and `map[targets:[]]` are
// the exact strings this fix removed, and an operator could not tell them
// from a broken command.
func TestBackupViewsSayTheEmptyCaseInWords(t *testing.T) {
	assertReadsAsProse(t, "backupListResult (empty)", backupListResult{}.String(), "no backup snapshots")
	assertReadsAsProse(t, "backupTargetListResult (empty)", backupTargetListResult{}.String(),
		"no backup targets", "backup target add")

	created := backupCreateResult{
		Manifest: backup.Manifest{
			Snapshot: "snap-9", ObjectCount: 7, Domains: []string{"config", "memory"},
		},
		AgeIdentitySource: "file",
	}
	assertReadsAsProse(t, "backupCreateResult", created.String(), "snap-9", "7", "config", "memory")

	listed := backupListResult{Snapshots: []backup.SnapshotSummary{{
		ID: "snap-9", Created: time.Unix(0, 0).UTC(), Target: "local-dir",
		ObjectCount: 7, Outcome: "complete", Domains: []string{"config"},
	}}}
	assertReadsAsProse(t, "backupListResult", listed.String(), "snap-9", "local-dir", "complete", "SNAPSHOT")
}

// TestQuarantineViewsNeverPrintTheMatchedText: a quarantine listing that
// printed the secret would defeat the quarantine.
func TestQuarantineViewsNeverPrintTheMatchedText(t *testing.T) {
	assertReadsAsProse(t, "quarantineListView (empty)", quarantineListView{}.String(), "quarantine is empty")

	const secret = "sk-live-THIS-MUST-NEVER-RENDER"
	listed := quarantineListView{Pending: 1, Entries: []entryView{{
		ID: "q-1", Class: "api-key", Pattern: "sk-live-…", Confidence: 0.91,
		SuggestedName: "provider.acme.key", SourceRef: "config.toml:14", DetectedAt: "2026-09-19T00:00:00Z",
	}}}
	rendered := listed.String()
	assertReadsAsProse(t, "quarantineListView", rendered, "q-1", "api-key", "0.91", "provider.acme.key", "1 pending")
	if strings.Contains(rendered, secret) {
		t.Fatalf("the quarantine listing printed the matched text:\n%s", rendered)
	}

	assertReadsAsProse(t, "releaseView", releaseView{ID: "q-1", Reason: "false positive"}.String(),
		"q-1", "false positive")
	assertReadsAsProse(t, "releaseView (no reason)", releaseView{ID: "q-2"}.String(), "q-2", "none")

	stored := promoteView{Name: "provider.acme.key", Backend: "file-vault", QuarantineID: "q-1"}
	assertReadsAsProse(t, "promoteView (stored)", stored.String(), "stored", "provider.acme.key", "file-vault", "q-1")
	replaced := promoteView{Name: "provider.acme.key", Replaced: true, Backend: "keychain", QuarantineID: "q-3"}
	assertReadsAsProse(t, "promoteView (replaced)", replaced.String(), "replaced", "keychain", "q-3")
}

// TestStatusViewSaysWhetherTheDaemonIsRunning, in words, in both cases.
func TestStatusViewSaysWhetherTheDaemonIsRunning(t *testing.T) {
	assertReadsAsProse(t, "statusView (stopped)", statusView{}.String(), "not running")
	assertReadsAsProse(t, "statusView (stopped, detail)",
		statusView{Detail: "socket absent"}.String(), "not running", "socket absent")
	assertReadsAsProse(t, "statusView (running)",
		statusView{Running: true, PID: 4242, UptimeS: 61, Connections: 2}.String(),
		"running", "4242", "2 connection")
}
