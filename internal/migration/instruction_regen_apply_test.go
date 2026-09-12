// Purpose: apply-mode gating tests (P1-E26-W10-S53-T3 acceptance criteria):
//
//	--check-equivalent behavior, TTY confirmation, and the
//	CASCADE_NO_INPUT=1 / --yes truth table. Split from
//	instruction_regen_test.go to keep that file under Art.10.3's
//	300-line cap.
//
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).
package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
)

// claudeMDPath is where realProject's single PRI-tier record's CLAUDE.md
// lands, mirroring the real path CCInstructionWriter's HarnessFile.Name
// (".claude/CLAUDE.md") resolves to under a PRI root.
func claudeMDPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "CLAUDE.md")
}

// TestRegenApplyGatingDeclined drives an interactive (non-NoInput,
// non-Yes) run whose Confirm callback declines: no file may be written,
// and the callback must actually have been consulted.
func TestRegenApplyGatingDeclined(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	called := false
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		Confirm: func(string) (bool, error) {
			called = true
			return false, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Fatal("Confirm was never consulted")
	}
	if len(report.Applied) != 0 {
		t.Fatalf("want no applied files, got %+v", report.Applied)
	}
	if _, err := os.Stat(claudeMDPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("declined apply must not write; stat err=%v", err)
	}
}

// TestRegenApplyGatingConfirmed drives the same run with Confirm accepting:
// the file must land on disk with real generator content, and the report
// must record the write.
func TestRegenApplyGatingConfirmed(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		Confirm:      func(string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("want exactly 2 applied files (CLAUDE.md + AGENTS.md), got %+v", report.Applied)
	}
	got, err := os.ReadFile(claudeMDPath(dir))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != wantFreshCLAUDEmd {
		t.Fatalf("written content mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, wantFreshCLAUDEmd)
	}
}

// TestRegenCheckOnlyNeverWrites is the "no silent overwrite" acceptance
// criterion's --check half: even with entries present, CheckOnly must
// leave the filesystem untouched and never call Confirm at all.
func TestRegenCheckOnlyNeverWrites(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		CheckOnly:    true,
		Confirm:      func(string) (bool, error) { t.Fatal("Confirm must not be called under --check"); return false, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Projects) != 1 || len(report.Projects[0].Drift) == 0 {
		t.Fatalf("want drift reported under --check, got %+v", report.Projects)
	}
	if _, err := os.Stat(claudeMDPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("--check must not write; stat err=%v", err)
	}
}

// TestRegenCASCADE_NO_INPUTWithoutYesPrintsAndExitsZero: CASCADE_NO_INPUT=1 without
// --yes must produce the report, write nothing, and return no error
// (exit 0) — never a refusal error, per this ticket's full_desc contract.
func TestRegenCASCADE_NO_INPUTWithoutYesPrintsAndExitsZero(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		NoInput:      true,
		Confirm:      func(string) (bool, error) { t.Fatal("Confirm must not be called under NoInput"); return false, nil },
	})
	if err != nil {
		t.Fatalf("want nil error under NoInput without --yes, got %v", err)
	}
	if len(report.Applied) != 0 {
		t.Fatalf("want no applied files, got %+v", report.Applied)
	}
	if len(report.Projects) != 1 || len(report.Projects[0].Drift) == 0 {
		t.Fatal("want the report to still carry the drift, only the write suppressed")
	}
	if _, err := os.Stat(claudeMDPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("NoInput without --yes must not write; stat err=%v", err)
	}
}

// TestRegenCASCADE_NO_INPUTWithYesWrites: CASCADE_NO_INPUT=1 WITH --yes applies
// without ever consulting Confirm.
func TestRegenCASCADE_NO_INPUTWithYesWrites(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		NoInput:      true,
		Yes:          true,
		Confirm:      func(string) (bool, error) { t.Fatal("Confirm must not be called when --yes is set"); return false, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("want exactly 2 applied files (CLAUDE.md + AGENTS.md), got %+v", report.Applied)
	}
	if _, err := os.Stat(claudeMDPath(dir)); err != nil {
		t.Fatalf("want the file written, stat err=%v", err)
	}
}

// TestRegenNilConfirmDeclines is the CLI layer's own safety fallback: a
// caller that reaches apply mode without wiring a Confirm callback at all
// (should never happen in production — cmd/cascade always supplies one)
// must still refuse to write, never panic and never apply by default.
func TestRegenNilConfirmDeclines(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Applied) != 0 {
		t.Fatalf("want no applied files with a nil Confirm, got %+v", report.Applied)
	}
}

// TestRegenApplyConfirmError propagates a Confirm callback's own error
// (e.g. a stdin read failure) as the run's error, without panicking and
// without writing anything.
func TestRegenApplyConfirmError(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	wantErr := errors.New("stdin closed")
	_, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		Confirm:      func(string) (bool, error) { return false, wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want it to wrap %v", err, wantErr)
	}
	if _, err := os.Stat(claudeMDPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("a Confirm error must not write; stat err=%v", err)
	}
}

// handEditedManagedBlock builds a managed block whose recorded digest does
// not match its body — internal/context's own definition of a hand edit —
// so cascadecontext.Sync's real write path refuses it (KindConflict),
// giving TestRegenApplySyncErrorSurfacesPartial a genuine write failure to
// observe rather than a synthetic one.
const handEditedManagedBlock = "<!-- cascade:generate-instructions digest=sha256:deadbeef -->\n" +
	"someone edited this by hand\n" +
	"<!-- /cascade:generate-instructions -->\n"

// TestRegenApplySyncErrorSurfacesPartial: when the real write path refuses
// a hand-edited file, Run must still report that project's error (Partial)
// rather than silently dropping it, and must still record whichever other
// files in the same project DID get written.
func TestRegenApplySyncErrorSurfacesPartial(t *testing.T) {
	dir, homeDir := realProject(t, "Short sentences.")
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(claudeMDPath(dir), []byte(handEditedManagedBlock), 0o600); err != nil {
		t.Fatalf("seed hand-edited file: %v", err)
	}
	report, err := Run(context.Background(), RunOptions{
		ProjectPaths: []string{dir},
		HomeDir:      homeDir,
		Confirm:      func(string) (bool, error) { return true, nil },
	})
	if err == nil {
		t.Fatal("want a non-nil error: the hand-edited file must refuse")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("kind=%v ok=%v, want KindConflict (hand-edit refusal)", kind, ok)
	}
	if !report.Partial {
		t.Fatal("want report.Partial=true")
	}
	// GenerateHarnessInstructions stops at its first error (sync.go's own
	// doc: a crash mid-sync leaves each file fully old or fully new, never
	// torn) — the claude writer's refusal on CLAUDE.md means AGENTS.md
	// (codex/opencode, generated after it) is never even attempted.
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("want AGENTS.md untouched (generation stopped at CLAUDE.md's refusal), stat err=%v", err)
	}
}

// TestActionName covers every cascadecontext.WriteAction this ticket's
// report can render, including the default ("unchanged") branch.
func TestActionName(t *testing.T) {
	cases := []struct {
		action cascadecontext.WriteAction
		want   string
	}{
		{cascadecontext.ActionCreated, "created"},
		{cascadecontext.ActionUpdated, "updated"},
		{cascadecontext.ActionAppended, "appended"},
		{cascadecontext.ActionBackedUp, "backed-up"},
		{cascadecontext.ActionUnchanged, "unchanged"},
	}
	for _, c := range cases {
		if got := actionName(c.action); got != c.want {
			t.Errorf("actionName(%v) = %q, want %q", c.action, got, c.want)
		}
	}
}
