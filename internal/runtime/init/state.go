// Package init implements `cascade init`: the nine-step setup wizard and
// the journal that lets a killed run resume where it stopped.
//
// The package is named init because the command is. That shadows no
// builtin — Go's `init` is a function name, not a package name — and a
// caller that wants both imports this one under an alias, which
// cmd/cascade does.
package init

// Purpose: the init journal (P1-E16-W4-S35-T6) — the on-disk record of
//   which wizard steps have completed, written after each one so a
//   killed run resumes at the first incomplete step instead of asking
//   nine questions again.
// Inputs: a path under the cascade home; the wizard's own step results.
// Outputs: init-state.json, written atomically.
// Constraints: the write is tmpfile + fsync + rename, because the one
//   thing a resume journal must never do is exist half-written — a
//   truncated journal is worse than no journal, since the wizard would
//   resume from a step that did not finish. A journal from a future
//   schema version is REFUSED, never guessed at.
// SPORT: internal/runtime/init state journal (ADD) — P1-E16-W4-S35-T6.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// StateFileName is the journal's name within the cascade home.
const StateFileName = "init-state.json"

// StateSchemaVersion is the journal format this build writes and is the
// highest it reads. A file claiming a higher version is refused rather
// than parsed on the assumption that the unknown fields do not matter:
// the whole purpose of the file is to say what has already happened, and
// being wrong about that means repeating a step or skipping one.
const StateSchemaVersion = 1

// Step identifies one wizard step. The values are the spec's own step
// numbers, so a journal is readable without this file open beside it.
type Step int

// The nine steps, in the order they run.
const (
	StepPreflight Step = 1
	StepProfile   Step = 2
	StepStorage   Step = 3
	StepPlugins   Step = 4
	StepProviders Step = 5
	StepHarnesses Step = 6
	StepTelemetry Step = 7
	StepDaemon    Step = 8
	StepDoctor    Step = 9
)

// LastStep is the final step; a journal recording it is a completed run.
const LastStep = StepDoctor

// String names the step for a journal, a prompt and a log line, so those
// three can never disagree about what step 6 is called.
func (s Step) String() string {
	switch s {
	case StepPreflight:
		return "preflight"
	case StepProfile:
		return "profile"
	case StepStorage:
		return "storage"
	case StepPlugins:
		return "plugins"
	case StepProviders:
		return "providers"
	case StepHarnesses:
		return "harnesses"
	case StepTelemetry:
		return "telemetry"
	case StepDaemon:
		return "daemon"
	case StepDoctor:
		return "doctor"
	default:
		return "unknown"
	}
}

// State is the journal's content.
//
// It records what each completed step DECIDED, not merely that it ran. A
// resume that knew only "step 5 finished" would have to ask again which
// providers were added in order to report them in step 9's summary, and
// asking again is the thing this file exists to prevent.
type State struct {
	// SchemaVersion is the format version. Written always; a read
	// refuses anything higher than StateSchemaVersion.
	SchemaVersion int `json:"schema_version"`
	// CompletedStep is the highest step that finished. Zero means the
	// wizard has not completed any step, which is a legitimate state: a
	// run killed inside step 1 leaves exactly that.
	CompletedStep Step `json:"completed_step"`
	// Profile is the profile chosen in step 2.
	Profile string `json:"profile,omitempty"`
	// StoragePath is the storage location confirmed in step 3.
	StoragePath string `json:"storage_path,omitempty"`
	// Plugins are the catalog entries selected in step 4.
	Plugins []string `json:"plugins,omitempty"`
	// Providers are the provider names added in step 5. Names only: a
	// journal is a plaintext file in the cascade home and no credential
	// of any kind belongs in it.
	Providers []string `json:"providers,omitempty"`
	// Harnesses are the harness kinds wired in step 6.
	Harnesses []string `json:"harnesses,omitempty"`
	// HarnessSkipReason names why step 6 wired nothing, so step 9's
	// summary can say so rather than printing an empty list that reads
	// as "no harnesses installed".
	HarnessSkipReason string `json:"harness_skip_reason,omitempty"`
	// Telemetry records step 7's answer.
	Telemetry bool `json:"telemetry"`
	// DaemonInstalled records whether step 8 installed the service. It
	// is false both when the user declined and when the platform does
	// not support it; DaemonSkipReason says which.
	DaemonInstalled bool `json:"daemon_installed"`
	// DaemonSkipReason names why step 8 installed nothing, so step 9's
	// summary can say so rather than leaving a blank line.
	DaemonSkipReason string `json:"daemon_skip_reason,omitempty"`
	// HelperFingerprint is the elevation helper's enrolled key
	// fingerprint, surfaced in step 9's summary card.
	HelperFingerprint string `json:"helper_fingerprint,omitempty"`
}

// NextStep returns the first step that has not completed.
func (s State) NextStep() Step {
	if s.CompletedStep < StepPreflight {
		return StepPreflight
	}
	return s.CompletedStep + 1
}

// Complete reports whether every step has finished.
func (s State) Complete() bool { return s.CompletedStep >= LastStep }

// StatePath returns the journal's path within home.
func StatePath(home string) string { return filepath.Join(home, StateFileName) }

// ErrStateFromTheFuture is the refusal for a journal written by a newer
// build. Resuming from it would mean acting on a record this build cannot
// fully read, in a file whose entire job is to say what already happened.
var ErrStateFromTheFuture = cascade.New(cascade.KindUnsupported,
	"cascade init: the init journal was written by a newer version of cascade; "+
		"upgrade, or delete it to start over")

// LoadState reads the journal at home.
//
// An ABSENT journal is not an error: it is the normal state of a machine
// that has never run init, and it returns the zero State with found=false
// so the caller can tell "nothing has happened yet" from "step zero
// completed". A journal that exists but cannot be parsed IS an error —
// silently treating corruption as a fresh start would re-run steps that
// already changed the machine.
func LoadState(home string) (state State, found bool, err error) {
	raw, err := os.ReadFile(StatePath(home)) //nolint:gosec // path is composed from the cascade home.
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, false, nil
		}
		return State{}, false, cascade.Wrap(cascade.KindUnavailable, err, "cascade init: read the init journal")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, false, cascade.Wrap(cascade.KindInvalidInput, err,
			"cascade init: the init journal is not readable JSON")
	}
	if state.SchemaVersion > StateSchemaVersion {
		return State{}, false, ErrStateFromTheFuture
	}
	if state.CompletedStep < 0 || state.CompletedStep > LastStep {
		return State{}, false, cascade.Newf(cascade.KindInvalidInput,
			"cascade init: the init journal records step %d, which is not one of the nine", int(state.CompletedStep))
	}
	return state, true, nil
}

// SaveState writes the journal atomically: a temp file in the same
// directory, fsynced, then renamed over the target.
//
// Same directory on purpose — rename is only atomic within a filesystem,
// and a temp file in the system temp directory can easily be on another
// one.
func SaveState(home string, state State) error {
	state.SchemaVersion = StateSchemaVersion
	sort.Strings(state.Plugins)
	sort.Strings(state.Providers)
	sort.Strings(state.Harnesses)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade init: encode the init journal")
	}
	if err := os.MkdirAll(home, 0o750); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: create the cascade home")
	}
	return writeAtomic(StatePath(home), append(raw, '\n'))
}

// writeAtomic performs the tmpfile + fsync + rename.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: create the journal's temp file")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: write the journal")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: flush the journal to disk")
	}
	if err := tmp.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: close the journal's temp file")
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: set the journal's mode")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: replace the journal")
	}
	return nil
}

// DeleteState removes the journal. A journal that is already gone is a
// success: the caller's intent is "there should be no journal", and there
// is not.
func DeleteState(home string) error {
	if err := os.Remove(StatePath(home)); err != nil && !os.IsNotExist(err) {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: remove the init journal")
	}
	return nil
}
