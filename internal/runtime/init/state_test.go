package init

// Purpose: the journal's contract (P1-E16-W4-S35-T6) — atomic write,
//   versioned read, and the three ways a read can go wrong.
// Constraints: Art.7.1 — every path is a t.TempDir().

import (
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAnAbsentJournalIsNotAnError pins the distinction the resume logic
// rests on: a machine that has never run init has no journal, and that is
// the normal case, not a failure.
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

// TestTheJournalRoundTripsEveryDecision requires each step's recorded
// decision to survive a write and a read.
//
// Not merely "it round-trips": a journal that recorded only the step
// NUMBER would pass a shallower test and still force a resumed run to
// re-ask which providers were added, which is the thing it exists to
// prevent.
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

// TestTheJournalNeverHoldsACredential is a standing assertion about a
// plaintext file in the cascade home: step 5 records provider NAMES, and
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

// TestAJournalFromTheFutureIsRefused: resuming from a record this build
// cannot fully read means acting on an unreliable account of what already
// happened, in the one file whose whole job is to be reliable about that.
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

// TestACorruptJournalIsRefusedRatherThanTreatedAsFresh is the assertion
// that matters most here: treating unreadable bytes as "nothing has
// happened" would re-run steps that already changed the machine.
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

// TestSaveLeavesNoTemporaryFileBehind pins the atomic write's visible
// consequence: the directory holds the journal and nothing else, so a
// crash between the write and the rename cannot leave a file a later read
// might pick up.
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
	// Windows does not carry POSIX permission bits: os.Stat reports 0666
	// for a file created with 0600, so the mode half of this assertion
	// measures the platform rather than the code. The temp-file half above
	// is the part that is portable, and it still runs there.
	if goruntime.GOOS == GOOSWindows {
		t.Logf("journal mode not asserted: %s has no POSIX permission bits", goruntime.GOOS)
		return
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("journal mode = %o, want 0600 (it sits in the cascade home)", mode)
	}
}

// TestDeletingAnAbsentJournalSucceeds: the caller's intent is "there
// should be no journal", and there is not.
func TestDeletingAnAbsentJournalSucceeds(t *testing.T) {
	if err := DeleteState(t.TempDir()); err != nil {
		t.Fatalf("DeleteState on a machine with no journal: %v", err)
	}
}

// TestEveryStepHasAName keeps the journal, the prompts and the log lines
// from disagreeing about what a step is called.
func TestEveryStepHasAName(t *testing.T) {
	seen := map[string]bool{}
	for s := StepPreflight; s <= LastStep; s++ {
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

// FuzzInitStateJSON drives the journal decoder over untrusted bytes: this
// file lives in the cascade home and is read on every run, so it must
// survive anything, refusing rather than panicking.
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
		if state.CompletedStep < 0 || state.CompletedStep > LastStep {
			t.Fatalf("LoadState accepted step %d", int(state.CompletedStep))
		}
		if state.SchemaVersion > StateSchemaVersion {
			t.Fatalf("LoadState accepted schema version %d", state.SchemaVersion)
		}
	})
}
