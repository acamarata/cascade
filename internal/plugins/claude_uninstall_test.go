package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
	claude "github.com/acamarata/cascade/plugins/claude"
)

// Purpose (this file): the host half of the uninstall hook — it declines
//   for other plugins, it records what happened, and it records it under
//   the kind R-14.248 ruled rather than a fifteenth one.
// SPORT: internal/plugins cascade-claude uninstall tests (ADD) — P1-E16-W4-S34-T1.

// recordingWriter captures every appended event.
type recordingWriter struct{ events []audit.Event }

func (w *recordingWriter) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	w.events = append(w.events, e)
	return audit.Record{}, nil
}

// teardownEnv wires a teardown over a temp harness tree.
func teardownEnv(t *testing.T) (*ClaudeTeardown, *recordingWriter, string) {
	t.Helper()
	root := t.TempDir()
	cwd := t.TempDir()
	instr := filepath.Join(cwd, "CLAUDE.md")
	content := []byte("# instructions\n")
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instr, content, 0o600); err != nil {
		t.Fatal(err)
	}
	prev := claude.Generate
	claude.Generate = func(context.Context, string) ([]claude.GeneratedFile, error) {
		return []claude.GeneratedFile{{Path: instr, Content: content}}, nil
	}
	t.Cleanup(func() { claude.Generate = prev })

	w := &recordingWriter{}
	return NewClaudeTeardown(claude.Paths{
		ConfigRoot: root,
		MCPConfig:  filepath.Join(root, "mcp.json"),
		HookConfig: filepath.Join(root, "hooks"),
	}, cwd, w), w, instr
}

// TestTheTeardownDeclinesForOtherPlugins is how one implementation can be
// handed to RemovePlugin for every plugin: it answers "not mine" rather
// than removing another plugin's files.
func TestTheTeardownDeclinesForOtherPlugins(t *testing.T) {
	td, w, instr := teardownEnv(t)
	if err := td.Teardown(context.Background(), "some-other-plugin"); err != nil {
		t.Fatalf("declining should not error: %v", err)
	}
	if _, err := os.Stat(instr); err != nil {
		t.Error("another plugin's removal deleted cascade-claude's files")
	}
	if len(w.events) != 0 {
		t.Errorf("%d audit rows written for a plugin this hook does not own", len(w.events))
	}
}

// TestTheUninstallIsAudited holds R-14.248's ruling in code: the row lands
// under KindConfigReload — the taxonomy stays at fourteen — and the Actor
// and Outcome are what tell an uninstall apart from a reload.
func TestTheUninstallIsAudited(t *testing.T) {
	td, w, instr := teardownEnv(t)
	if err := td.Teardown(context.Background(), claudePackName); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if _, err := os.Stat(instr); !os.IsNotExist(err) {
		t.Error("the instruction file survived the teardown")
	}
	if len(w.events) != 1 {
		t.Fatalf("%d audit rows, want exactly 1", len(w.events))
	}
	ev := w.events[0]
	if ev.Kind != audit.KindConfigReload {
		t.Errorf("kind = %q, want the reused %q (R-14.248: the taxonomy stays at fourteen)",
			ev.Kind, audit.KindConfigReload)
	}
	if ev.Actor != claudeUninstallActor {
		t.Errorf("actor = %q, want %q", ev.Actor, claudeUninstallActor)
	}
	if ev.Outcome != claudeUninstallOutcome {
		t.Errorf("outcome = %q, want %q — this is what separates an uninstall from a reload",
			ev.Outcome, claudeUninstallOutcome)
	}
	var row uninstallRow
	if err := json.Unmarshal(ev.Explain, &row); err != nil {
		t.Fatalf("the row's payload is not JSON: %v", err)
	}
	if len(row.Removed) == 0 {
		t.Errorf("the row names no removed file: %+v", row)
	}
}

// TestAnUninstallThatFoundNothingIsStillRecorded is the rule that an
// operator may need the negative as much as the positive: a teardown that
// ran and found nothing is a fact.
func TestAnUninstallThatFoundNothingIsStillRecorded(t *testing.T) {
	td, w, instr := teardownEnv(t)
	if err := os.Remove(instr); err != nil {
		t.Fatal(err)
	}
	if err := td.Teardown(context.Background(), claudePackName); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(w.events) != 1 {
		t.Fatalf("%d audit rows for an empty uninstall, want 1", len(w.events))
	}
	var row uninstallRow
	if err := json.Unmarshal(w.events[0].Explain, &row); err != nil {
		t.Fatal(err)
	}
	if len(row.Absent) == 0 {
		t.Errorf("the row does not report the absent files: %+v", row)
	}
}

// TestTheFilesGoEvenWithNoAuditWriter is the degradation. Refusing to
// uninstall because the row could not be written would leave an operator
// unable to remove a plugin from a machine whose audit log is unavailable
// — which is exactly when they are most likely to be removing it.
func TestTheFilesGoEvenWithNoAuditWriter(t *testing.T) {
	td, _, instr := teardownEnv(t)
	td.writer = nil
	if err := td.Teardown(context.Background(), claudePackName); err != nil {
		t.Fatalf("teardown with no writer: %v", err)
	}
	if _, err := os.Stat(instr); !os.IsNotExist(err) {
		t.Error("the files were left because there was nowhere to record their removal")
	}
}

// refusingWriter fails every append.
type refusingWriter struct{}

func (refusingWriter) Append(context.Context, audit.Event) (audit.Record, error) {
	return audit.Record{}, cascade.New(cascade.KindUnavailable, "audit: log unavailable")
}

// TestAFailedAuditIsReportedAfterTheFilesAreGone holds the ORDER the hook
// commits to. The removal is attempted first and its outcome wins: an
// uninstall that succeeded but could not be recorded reports the recording
// failure, and an uninstall that FAILED reports that instead — because the
// operator's next action differs completely between the two.
func TestAFailedAuditIsReportedAfterTheFilesAreGone(t *testing.T) {
	td, _, instr := teardownEnv(t)
	td.writer = refusingWriter{}

	err := td.Teardown(context.Background(), claudePackName)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want the audit failure surfaced", err)
	}
	if _, statErr := os.Stat(instr); !os.IsNotExist(statErr) {
		t.Error("the files were left in place because the row could not be written")
	}
}

// TestARemovalFailureWinsOverAnAuditFailure is the other half of that
// order. Both went wrong; the one the operator has to act on is the
// removal, so that is the error returned.
func TestARemovalFailureWinsOverAnAuditFailure(t *testing.T) {
	td, _, _ := teardownEnv(t)
	td.writer = refusingWriter{}
	prev := claude.Generate
	claude.Generate = func(context.Context, string) ([]claude.GeneratedFile, error) {
		return nil, cascade.New(cascade.KindInternal, "generator exploded")
	}
	t.Cleanup(func() { claude.Generate = prev })

	err := td.Teardown(context.Background(), claudePackName)
	if err == nil {
		t.Fatal("no error at all")
	}
	if !strings.Contains(err.Error(), "which instruction files are ours") {
		t.Errorf("err = %q, want the REMOVAL failure rather than the audit one", err)
	}
}

// TestAFailedRemovalIsStillRecorded keeps the log honest about the failure
// case: a teardown that could not finish is exactly the row an operator
// needs later.
func TestAFailedRemovalIsStillRecorded(t *testing.T) {
	td, w, _ := teardownEnv(t)
	prev := claude.Generate
	claude.Generate = func(context.Context, string) ([]claude.GeneratedFile, error) {
		return nil, cascade.New(cascade.KindInternal, "generator exploded")
	}
	t.Cleanup(func() { claude.Generate = prev })

	if err := td.Teardown(context.Background(), claudePackName); err == nil {
		t.Fatal("a failed removal reported success")
	}
	if len(w.events) != 1 {
		t.Fatalf("%d rows for a failed uninstall, want 1", len(w.events))
	}
	var row uninstallRow
	if err := json.Unmarshal(w.events[0].Explain, &row); err != nil {
		t.Fatal(err)
	}
	if row.Error == "" {
		t.Error("the row does not carry the failure that stopped the uninstall")
	}
}
