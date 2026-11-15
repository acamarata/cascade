package init

// Purpose: the journal's contract (P1-E16-W4-S35-T6) and the V2 step order
//   and its replay. Constraints: Art.7.1 — every path is a t.TempDir().

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAnAbsentJournalIsNotAnError: a machine that never ran init has no
// journal, and that is the normal case, not a failure.
func TestAnAbsentJournalIsNotAnError(t *testing.T) {
	state, found, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState on a fresh machine: %v", err)
	}
	if found {
		t.Error("found = true with no journal on disk")
	}
	if state.NextStep() != StepPreflight {
		t.Errorf("NextStep = %v on a fresh machine, want %v", state.NextStep(), StepPreflight)
	}
}

// TestTheJournalRoundTripsEveryDecision: each step's DECISION survives a
// write and a read, not merely the step number.
func TestTheJournalRoundTripsEveryDecision(t *testing.T) {
	home := t.TempDir()
	want := State{
		CompletedStep: StepDaemon, Profile: ProfileLocal, StoragePath: "/db",
		Plugins: []string{"claude"}, Providers: []string{"p1"}, Harnesses: []string{"codex"},
		Telemetry: true, DaemonInstalled: true, HelperFingerprint: "SHA256:abc",
	}
	if err := SaveState(home, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, found, err := LoadState(home)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !found {
		t.Fatal("found = false right after a save")
	}
	if got.SchemaVersion != StateSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, StateSchemaVersion)
	}
	want.SchemaVersion = StateSchemaVersion
	if got.Profile != want.Profile || got.StoragePath != want.StoragePath ||
		got.Telemetry != want.Telemetry || got.DaemonInstalled != want.DaemonInstalled ||
		got.HelperFingerprint != want.HelperFingerprint || got.CompletedStep != want.CompletedStep {
		t.Errorf("round trip lost a decision:\n got %+v\nwant %+v", got, want)
	}
	for name, pair := range map[string][2][]string{
		"Plugins":   {got.Plugins, want.Plugins},
		"Providers": {got.Providers, want.Providers},
		"Harnesses": {got.Harnesses, want.Harnesses},
	} {
		if len(pair[0]) != len(pair[1]) || (len(pair[0]) > 0 && pair[0][0] != pair[1][0]) {
			t.Errorf("%s round-tripped as %v, want %v", name, pair[0], pair[1])
		}
	}
}

// TestTheJournalNeverHoldsACredential: step 5 records provider NAMES and
// nothing about how any of them authenticates.
func TestTheJournalNeverHoldsACredential(t *testing.T) {
	raw, err := json.Marshal(State{Providers: []string{"anthropic"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"key", "token", "secret", "password", "credential"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Errorf("the journal's own field names include %q: %s", forbidden, raw)
		}
	}
}

// TestAJournalFromTheFutureIsRefused: a record this build cannot fully read
// is never resumed from.
func TestAJournalFromTheFutureIsRefused(t *testing.T) {
	home := t.TempDir()
	writeJournal(t, home, `{"schema_version": 999, "completed_step": 3}`)

	_, _, err := LoadState(home)
	if err == nil {
		t.Fatal("LoadState accepted a journal from a newer build")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("kind = %v (typed %t), want KindUnsupported", kind, ok)
	}
}

// TestACorruptJournalIsRefusedRatherThanTreatedAsFresh: unreadable bytes
// read as "nothing happened" would re-run steps that changed the machine.
func TestACorruptJournalIsRefusedRatherThanTreatedAsFresh(t *testing.T) {
	for name, body := range map[string]string{
		"not json":            "{{{",
		"a step out of range": `{"schema_version":1,"completed_step":42}`,
		"a negative step":     `{"schema_version":1,"completed_step":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeJournal(t, home, body)
			state, found, err := LoadState(home)
			if err == nil {
				t.Fatalf("LoadState accepted %q as state %+v (found=%v)", body, state, found)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
			}
		})
	}
}

// TestSaveLeavesNoTemporaryFileBehind: after the atomic write the home
// holds the journal and nothing else.
func TestSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	home := t.TempDir()
	if err := SaveState(home, State{CompletedStep: StepProfile}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != StateFileName {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the cascade home holds %v, want only %s", names, StateFileName)
	}
	info, err := os.Stat(StatePath(home))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Windows has no POSIX permission bits (0600 reads back as 0666).
	if goruntime.GOOS == GOOSWindows {
		t.Logf("journal mode not asserted: %s has no POSIX permission bits", goruntime.GOOS)
		return
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("journal mode = %o, want 0600 (it sits in the cascade home)", mode)
	}
}

// TestDeletingAnAbsentJournalSucceeds: "no journal" is already true.
func TestDeletingAnAbsentJournalSucceeds(t *testing.T) {
	if err := DeleteState(t.TempDir()); err != nil {
		t.Fatalf("DeleteState on a machine with no journal: %v", err)
	}
}

// TestEveryStepHasAName: every step in run order has a distinct name.
func TestEveryStepHasAName(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range stepOrder {
		name := s.String()
		if name == "unknown" {
			t.Errorf("step %d has no name", int(s))
		}
		if seen[name] {
			t.Errorf("two steps are both called %q", name)
		}
		seen[name] = true
	}
	if got := Step(0).String(); got != "unknown" {
		t.Errorf("Step(0) = %q, want %q", got, "unknown")
	}
}

// writeJournal plants a raw journal body.
func writeJournal(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, StateFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("planting a journal: %v", err)
	}
}

// FuzzInitStateJSON: the journal decoder refuses, never panics, on any
// bytes.
func FuzzInitStateJSON(f *testing.F) {
	for _, seed := range []string{
		`{"schema_version":1,"completed_step":0}`,
		`{"schema_version":1,"completed_step":9,"profile":"local","providers":["p"]}`,
		`{"schema_version":999}`,
		`{"completed_step":"three"}`,
		`{}`, ``, `null`, `[]`, `"a string"`, `{{{`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, StateFileName), []byte(body), 0o600); err != nil {
			t.Skip()
		}
		state, _, err := LoadState(home)
		if err != nil {
			return
		}
		if state.CompletedStep != 0 && state.CompletedStep.position() < 0 {
			t.Fatalf("LoadState accepted step %d", int(state.CompletedStep))
		}
		if state.SchemaVersion > StateSchemaVersion {
			t.Fatalf("LoadState accepted schema version %d", state.SchemaVersion)
		}
	})
}

// typicalMachine is an interactive `cascade init --profile local` on a
// machine with one harness and the intake registry wired, the operator
// taking every default and adding no account.
func typicalMachine(t *testing.T) (*Wizard, *scriptedPrompter, *bytes.Buffer) {
	t.Helper()
	isolateHome(t)
	prompt := &scriptedPrompter{confirms: []bool{false, true, false, true}, lines: []string{""}}
	w, rec, out, _ := fixture(t, Options{Profile: ProfileLocal}, prompt)
	rec.detected, w.deps.Registry = rec.detected[:1], intake.NewMemoryRegistry()
	return w, prompt, out
}

// TestWizardV2OrderedSteps: step 5 is the capacity checklist, 8b nodes and
// 8c policy run after step 8's controller designation, and a journal
// written before 8b existed still replays.
func TestWizardV2OrderedSteps(t *testing.T) {
	w, prompt, out := typicalMachine(t)
	want := "preflight profile storage plugins providers harnesses telemetry daemon nodes policy doctor"
	if got := strings.Join(mustRun(t, w).Steps, " "); got != want {
		t.Errorf("steps ran as %q, want %q", got, want)
	}
	if !contains(prompt.asked, capacityMoreQuestion) {
		t.Errorf("the provider step never asked the capacity question: %v", prompt.asked)
	}
	text, at := out.String(), 0
	for _, mark := range []string{"== providers", "== capacity", "== harnesses",
		"controller designated: epoch 1", "== nodes", "== policy", "== doctor"} {
		i := strings.Index(text[at:], mark)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", mark, text)
		}
		at += i
	}
	w, _, _, home := fixture(t, Options{Mode: ModeYes}, nil)
	writeJournal(t, home, `{"schema_version":1,"completed_step":8,"profile":"local","daemon_installed":true}`)
	report := mustRun(t, w)
	if got := strings.Join(report.Steps, " "); !report.Resumed || got != "nodes policy doctor" {
		t.Errorf("a pre-8b journal at step 8 replayed %q (resumed %t), want nodes policy doctor", got, report.Resumed)
	}
	writeJournal(t, home, `{"schema_version":1,"completed_step":9,"profile":"local"}`)
	if state, _, err := LoadState(home); err != nil || !state.Complete() || state.NextStep() != 0 {
		t.Errorf("a completed pre-8b journal reads as %+v, %v; want complete", state, err)
	}
}

// TestInitNoSecretValuesInConfig: the setup file, the journal, the
// reconverge delta and the doctor notes carry references, never the value
// a reference resolves to.
func TestInitNoSecretValuesInConfig(t *testing.T) {
	w, rec, body := v2Run(t, initconfig.Flags{}, map[string]string{}, "strict")
	w.deps.Policy = policy.NewController(nil)
	st := mustRun(t, w).State
	if !contains(st.Providers, "work") || !contains(st.Nodes, "n1.invalid") || st.PolicyPreset != "strict" {
		t.Errorf("the run recorded no provider, node or preset name: %+v", st)
	}
	journalHome := t.TempDir()
	saveErr := SaveState(journalHome, st)
	journal, _ := os.ReadFile(StatePath(journalHome))
	delta, err := ReconvergeDiscovery(context.Background(), &fakeDiscoveryStore{},
		DiscoveryState{Nodes: st.Nodes, Providers: st.Providers, Policy: "strict"})
	deltaJSON, _ := json.Marshal(delta)
	args, _ := json.Marshal(rec.subArgs)
	notes := w.deps.Out.(interface{ String() string }).String()
	for name, text := range map[string]string{"config": body, "journal": string(journal),
		"delta": string(deltaJSON), "provider add argv": string(args), "doctor notes": notes} {
		if strings.Contains(text, v2Canary) {
			t.Errorf("the %s holds the secret value:\n%s", name, text)
		}
	}
	if err != nil || saveErr != nil || !strings.Contains(string(args), "INIT_CANARY_KEY") ||
		!strings.Contains(notes, "every first-run check passed") {
		t.Errorf("delta %v, journal %v, or no key_env name in the argv / no doctor run:\n%s\n%s", err, saveErr, args, notes)
	}
}
