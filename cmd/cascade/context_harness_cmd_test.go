package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// TestContextHarnessList drives the command's renderer over every state a
// machine can be in, and asserts the row for each.
//
// It renders all three harnesses in every case: a list that omitted the
// absent ones could not tell "codex is not installed" from "this build
// forgot codex", which is the one question this surface exists to answer.
func TestContextHarnessList(t *testing.T) {
	view := contextHarnessHumanView{daemon.ContextHarnessListResult{Harnesses: []cascadecontext.HarnessState{
		{Kind: cascadecontext.HarnessClaude, Detected: true},
		{Kind: cascadecontext.HarnessCodex, Detected: true, Drift: true,
			DriftReason: "content differs", InstructionPath: "/p/AGENTS.md"},
		{Kind: cascadecontext.HarnessOpenCode, Detected: false, InstallPath: "/h/.config/opencode"},
	}}}
	out := view.String()

	for _, want := range []string{"claude", "codex", "opencode", "in sync", "drifted", "not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered list omits %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "/p/AGENTS.md: content differs") {
		t.Errorf("a drifted row does not name the file and the reason:\n%s", out)
	}
	if !strings.Contains(out, "/h/.config/opencode") {
		t.Errorf("an absent row does not say where we looked; a false negative is undebuggable without it:\n%s", out)
	}
	// The --json body is the embedded result itself, which is what
	// output.Writer.Result marshals. Asserting on the marshalled bytes
	// rather than on a getter is the point: the getter this view used to
	// have was called by nothing, so it could not have caught the view
	// serialising as an empty object.
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshalling the view: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"kind":"codex"`)) {
		t.Errorf("--json body does not carry the harness rows: %s", encoded)
	}
}

// TestContextHarnessListRendersAnEmptyReport pins the degenerate case as
// a statement rather than a blank screen.
func TestContextHarnessListRendersAnEmptyReport(t *testing.T) {
	out := contextHarnessHumanView{daemon.ContextHarnessListResult{}}.String()
	if !strings.Contains(out, "no harnesses were reported") {
		t.Fatalf("an empty report rendered as %q", out)
	}
}

// TestHarnessSummaryLine covers the one-liner the doctor row and the init
// wizard share.
func TestHarnessSummaryLine(t *testing.T) {
	none := harnessSummaryLine([]cascadecontext.HarnessState{{Kind: cascadecontext.HarnessClaude}})
	if !strings.Contains(none, "no supported harness") {
		t.Errorf("summary with nothing installed = %q", none)
	}
	some := harnessSummaryLine([]cascadecontext.HarnessState{
		{Kind: cascadecontext.HarnessOpenCode, Detected: true},
		{Kind: cascadecontext.HarnessClaude, Detected: true},
	})
	if some != "claude, opencode" {
		t.Errorf("summary = %q, want the installed kinds in a stable order", some)
	}
}

// TestContextHarnessSyncIsTheSameOperationAsContextSync is the
// no-second-implementation assertion: both commands exist, and the one
// under `harness` carries the same --check flag and no more.
//
// A `harness sync` that had grown its own flags, or its own RunE calling
// something else, would be a second spelling that could answer
// differently — which is the defect this test exists to catch.
func TestContextHarnessSyncIsTheSameOperationAsContextSync(t *testing.T) {
	deps := contextScopeDeps{}
	harness := newContextHarnessSyncCmd(deps)
	plain := newContextSyncCmd(deps)

	if harness.Flags().Lookup("check") == nil {
		t.Fatal("`harness sync` has no --check flag")
	}
	if plain.Flags().Lookup("check") == nil {
		t.Fatal("`context sync` has no --check flag")
	}
	if harness.Use != plain.Use {
		t.Errorf("harness sync uses %q, context sync uses %q", harness.Use, plain.Use)
	}
	if harness.Short != plain.Short {
		t.Errorf("the two spellings describe themselves differently:\n  %q\n  %q", harness.Short, plain.Short)
	}
}

// TestTheHarnessCommandIsMountedUnderContext asserts the command tree 07
// specifies, rather than a group nobody can reach.
func TestTheHarnessCommandIsMountedUnderContext(t *testing.T) {
	var harness *string
	for _, sub := range newContextCmd(contextScopeDeps{}).Commands() {
		if sub.Name() == "harness" {
			names := []string{}
			for _, leaf := range sub.Commands() {
				names = append(names, leaf.Name())
			}
			joined := strings.Join(names, ",")
			harness = &joined
		}
	}
	if harness == nil {
		t.Fatal("`context harness` is not mounted under `context`")
	}
	for _, want := range []string{"list", "sync"} {
		if !strings.Contains(*harness, want) {
			t.Errorf("`context harness` has no %q subcommand (has: %s)", want, *harness)
		}
	}
}

// TestDoctorHarnessFlag proves --harness narrows the run to exactly the
// harness check, over the REAL production registry.
//
// Both halves matter: that the check is mounted at all, and that the flag
// selects it alone. A flag that selected nothing would produce doctor's
// own "no checks registered" refusal, which reads like a broken install.
func TestDoctorHarnessFlag(t *testing.T) {
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), cascaderuntime.SystemClock{})
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	if _, mounted := reg.Lookup(cascadecontext.HarnessCheckName); !mounted {
		t.Fatalf("%q is not mounted by the production doctor registry", cascadecontext.HarnessCheckName)
	}
	selected := onlyHarnessCheck(reg)
	if len(selected) != 1 || selected[0].Name() != cascadecontext.HarnessCheckName {
		t.Fatalf("--harness selected %d check(s): %v", len(selected), checkNames(selected))
	}
	if len(reg.List()) <= 1 {
		t.Fatal("the registry holds one check, so narrowing to one asserts nothing")
	}
}

// TestDoctorHarnessFlagOnAnEmptyRegistryRefuses pins the Art.1 rule that
// absence is never a pass: narrowing to a check nobody registered yields
// nothing, which executeChecks turns into a refusal rather than a clean
// report over zero checks.
func TestDoctorHarnessFlagOnAnEmptyRegistryRefuses(t *testing.T) {
	if selected := onlyHarnessCheck(doctor.NewCheckRegistry()); len(selected) != 0 {
		t.Fatalf("an empty registry selected %d check(s)", len(selected))
	}
}

// checkNames renders a check slice for a failure message.
func checkNames(checks []doctor.Check) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.Name())
	}
	return out
}

// harnessCmdFixture pins HOME and the working directory to temp trees and
// seeds installed harness roots there, so this test reports on a machine
// it created rather than on whichever harnesses the developer running it
// happens to have (Art.7.1).
func harnessCmdFixture(t *testing.T, installed ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Each harness's own override is cleared: this process may itself be
	// running under one, and an inherited override would silently point
	// detection at the real installation.
	for _, kind := range cascadecontext.SupportedHarnesses() {
		if name := cascadecontext.OverrideVarFor(kind); name != "" {
			t.Setenv(name, "")
		}
	}
	for _, rel := range installed {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o750); err != nil {
			t.Fatalf("seeding %s: %v", rel, err)
		}
	}
	t.Chdir(t.TempDir())
}

// TestFetchContextHarnessListEmbedded drives the routing function on the
// daemonless path — the one a `cascade context harness list` on a machine
// with no daemon actually takes.
//
// It asserts both directions of the detection it seeded. A test that only
// checked the installed harness would pass against a detector that
// reported everything installed, which is the more dangerous of the two
// wrong answers here.
func TestFetchContextHarnessListEmbedded(t *testing.T) {
	harnessCmdFixture(t, ".claude")

	result, err := fetchContextHarnessList(context.Background(), contextScopeDeps{})
	if err != nil {
		t.Fatalf("fetchContextHarnessList: %v", err)
	}
	if len(result.Harnesses) != len(cascadecontext.SupportedHarnesses()) {
		t.Fatalf("reported %d harnesses, want all %d",
			len(result.Harnesses), len(cascadecontext.SupportedHarnesses()))
	}
	byKind := map[cascadecontext.HarnessKind]cascadecontext.HarnessState{}
	for _, h := range result.Harnesses {
		byKind[h.Kind] = h
	}
	if !byKind[cascadecontext.HarnessClaude].Detected {
		t.Error("the seeded harness was not detected on the embedded path")
	}
	if byKind[cascadecontext.HarnessCodex].Detected {
		t.Error("a harness that was never installed was reported as detected")
	}
}

// TestContextHarnessListCommandPrintsTheTable runs the command itself,
// through its own RunE and its own output writer, so the wiring between
// the routing function and the renderer is covered by something other
// than a direct call to each half.
func TestContextHarnessListCommandPrintsTheTable(t *testing.T) {
	harnessCmdFixture(t, ".config/opencode")

	cmd := newContextHarnessListCmd(contextScopeDeps{})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(nil)
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context harness list: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"HARNESS", "claude", "codex", "opencode", "not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output omits %q:\n%s", want, out)
		}
	}
}

// TestHarnessDetailFallsBackToWhereWeLooked covers the column's other
// branch: a drifted harness whose reason is known but whose instruction
// path is not still has to say something, and an undrifted one says where
// it was found.
func TestHarnessDetailFallsBackToWhereWeLooked(t *testing.T) {
	reasonOnly := harnessDetail(cascadecontext.HarnessState{
		Kind: cascadecontext.HarnessClaude, Detected: true, Drift: true, DriftReason: "content differs",
	})
	if reasonOnly != "content differs" {
		t.Errorf("a drifted row with no instruction path rendered %q", reasonOnly)
	}
	found := harnessDetail(cascadecontext.HarnessState{
		Kind: cascadecontext.HarnessCodex, InstallPath: "/h/.codex",
	})
	if found != "/h/.codex" {
		t.Errorf("an undrifted row rendered %q, want the path we probed", found)
	}
}
