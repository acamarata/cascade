package main

// Purpose: `cascade init`'s own wiring (P1-E16-W4-S35-T6) — the flag
//   surface, the mode resolution, the exit code, and the adapters that
//   must reach the REAL implementations.
// Constraints: Art.7.1 — HOME and the working directory are temp trees.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestInitIsMountedWithItsRatifiedFlags pins 07 line 16's flag surface.
// A flag that silently disappeared would make every script using it fall
// back to interactive, which in CI means hanging.
func TestInitIsMountedWithItsRatifiedFlags(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"init"})
	if err != nil || cmd.Name() != "init" {
		t.Fatalf("`cascade init` is not mounted: %v", err)
	}
	for _, name := range []string{"yes", "check", "profile", "harness", "no-daemon"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`cascade init` has no --%s flag", name)
		}
	}
}

// TestCheckWinsOverYes: the two disagree about whether to write, and the
// safe reading of a contradictory instruction is the one that changes
// nothing.
func TestCheckWinsOverYes(t *testing.T) {
	for name, tc := range map[string]struct {
		flags initFlags
		want  cascadeinit.Mode
	}{
		"neither": {initFlags{}, cascadeinit.ModeInteractive},
		"--yes":   {initFlags{yes: true}, cascadeinit.ModeYes},
		"--check": {initFlags{check: true}, cascadeinit.ModeCheck},
		"both":    {initFlags{yes: true, check: true}, cascadeinit.ModeCheck},
	} {
		t.Run(name, func(t *testing.T) {
			if got := initOptions(&tc.flags).Mode; got != tc.want {
				t.Errorf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckFoundWorkExitsThree pins 08 §2's ratified exit code. An
// operator's CI branches on this number, so it is asserted rather than
// assumed.
func TestCheckFoundWorkExitsThree(t *testing.T) {
	kind, ok := cascade.KindOf(ErrInitCheckFoundWork)
	if !ok {
		t.Fatal("ErrInitCheckFoundWork carries no taxonomy kind")
	}
	if got := kind.ExitCode(); got != 3 {
		t.Errorf("`init --check` with work to do exits %d, want the ratified 3", got)
	}
}

// TestTheCatalogComesFromTheLiveRegistry is the Article-1 assertion for
// step 4: the rows are the plugins this build actually registers, so a
// checklist can never offer one it cannot install.
func TestTheCatalogComesFromTheLiveRegistry(t *testing.T) {
	entries := initPluginCatalog{}.Entries()
	if len(entries) == 0 {
		t.Fatal("the live registry produced no catalog rows")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Name == "" {
			t.Error("a catalog row has no name")
		}
		if seen[e.Name] {
			t.Errorf("the catalog lists %q twice", e.Name)
		}
		seen[e.Name] = true
	}
	// Every harness this build ships an adapter for must be offerable,
	// or step 6 could wire a harness step 4 never mentioned.
	for _, want := range []string{"cascade-claude", "cascade-codex", "cascade-opencode"} {
		if !seen[want] {
			t.Errorf("the catalog omits %q, which this build does ship", want)
		}
	}
}

// TestTheWizardUsesTheRealHarnessDetector asserts the adapter reaches
// internal/context's own detector — the wizard must never re-implement
// path probing, and a second implementation would drift from the one
// `cascade context harness list` and `doctor --harness` both use.
func TestTheWizardUsesTheRealHarnessDetector(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, kind := range cascadecontext.SupportedHarnesses() {
		if name := cascadecontext.OverrideVarFor(kind); name != "" {
			t.Setenv(name, "")
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o750); err != nil {
		t.Fatalf("seeding a harness: %v", err)
	}

	states, err := initHarnessDetector().Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(states) != len(cascadecontext.SupportedHarnesses()) {
		t.Fatalf("detected %d harnesses, want all %d", len(states), len(cascadecontext.SupportedHarnesses()))
	}
	found := map[string]bool{}
	for _, s := range states {
		if s.Detected {
			found[s.Kind] = true
		}
		if s.InstallPath == "" {
			t.Errorf("%s reports no probed path", s.Kind)
		}
	}
	if !found["claude"] {
		t.Error("the seeded harness was not detected through the wizard's adapter")
	}
}

// TestEveryDetectedHarnessHasAnInstallAdapter is the pairing assertion:
// the wizard only offers to wire what the detector reported, so a kind
// the detector knows and the wirer does not would reach a refusal at the
// worst moment — after the operator said yes.
func TestEveryDetectedHarnessHasAnInstallAdapter(t *testing.T) {
	wirer := initHarnessWirer{}
	for _, kind := range cascadecontext.SupportedHarnesses() {
		err := wirer.Wire(context.Background(), string(kind), t.TempDir())
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), "has no install adapter") {
			t.Errorf("%s is a supported harness with no install adapter", kind)
		}
	}
	// The negative half: an unknown kind refuses rather than silently
	// wiring nothing, which is what makes the assertion above meaningful.
	err := wirer.Wire(context.Background(), "gemini", t.TempDir())
	if err == nil {
		t.Fatal("the wirer accepted a harness this build does not support")
	}
	if !strings.Contains(err.Error(), "has no install adapter") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// TestTheSecretGuardRequiresAReference states the rule positively: a
// connection setting must BE a vault reference. A denylist of
// secret-shaped strings would pass the first credential format nobody
// thought of, and this value lands in a plaintext config file.
func TestTheSecretGuardRequiresAReference(t *testing.T) {
	guard := initSecretGuard{}
	for _, ok := range []string{"", "vault://pg", "env:PG_URL", "${PG_URL}"} {
		if err := guard.Check("postgres", ok); err != nil {
			t.Errorf("Check(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"postgres://user:hunter2@db/cascade",
		"redis://localhost:6379",
		"plain-text",
	} {
		err := guard.Check("postgres", bad)
		if err == nil {
			t.Errorf("Check(%q) accepted a literal value", bad)
			continue
		}
		if !strings.Contains(err.Error(), "vault set") {
			t.Errorf("Check(%q) does not redirect to the vault: %v", bad, err)
		}
	}
}

// TestTheProbeDoesNotCreateTheHomeWhenItMayNot is the production half of
// the wizard's own --check contract.
func TestTheProbeDoesNotCreateTheHomeWhenItMayNot(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "never", "created")

	if err := (initStorageProbe{}).Probe(context.Background(), home, false); err != nil {
		t.Fatalf("Probe(mayCreate=false): %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "never")); !os.IsNotExist(err) {
		t.Error("a --check probe created part of the cascade home")
	}

	if err := (initStorageProbe{}).Probe(context.Background(), home, true); err != nil {
		t.Fatalf("Probe(mayCreate=true): %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("a writing probe did not create the home: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the probe left %d file(s) behind", len(entries))
	}
}

// TestTheFingerprintIsReadFromTheEnrollOutput covers the one piece of
// parsing in the adapters: an enrollment that printed no recognisable
// fingerprint yields the empty string, and the summary card then omits
// the line rather than showing somebody else's output.
func TestTheFingerprintIsReadFromTheEnrollOutput(t *testing.T) {
	for name, tc := range map[string]struct{ out, want string }{
		"a normal enrollment": {"enrolled helper key\n  SHA256:abc123  (file-backed)\n", "SHA256:abc123  (file-backed)"},
		"no fingerprint":      {"nothing to do\n", ""},
		"empty":               {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := fingerprintIn(tc.out); got != tc.want {
				t.Errorf("fingerprintIn(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

// TestTheStepNamesMatchTheHelpText keeps the help's "nine setup steps"
// from drifting from the enum.
func TestTheStepNamesMatchTheHelpText(t *testing.T) {
	names := initWizardStepNames()
	if len(names) != 9 {
		t.Fatalf("the wizard has %d steps, and the help text says nine", len(names))
	}
	long := newInitCmd().Long
	for _, name := range names {
		if !strings.Contains(long, name) {
			t.Errorf("the help text does not mention step %q", name)
		}
	}
}
