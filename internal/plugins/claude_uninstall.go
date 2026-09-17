// Purpose: the host half of cascade-claude's uninstall hook — the
//
//	ProcessTeardown internal/plugins.RemovePlugin calls, which runs the
//	plugin's own removal and writes the audit row for it.
//
// WHY THE SPLIT. Art.10.2 forbids plugins/** from importing internal/**,
//
//	and the audit log lives in internal/audit. So the plugin decides WHAT
//	to remove — it is the only thing that knows which files are its own —
//	and this file records that it happened. Neither half can be done by
//	the other.
//
// THE AUDIT KIND, AND WHY IT IS NOT A NEW ONE (R-14.248). The taxonomy is
//
//	frozen at fourteen (R-21.235). A cascade-claude uninstall removes the
//	instruction file, the hook-pack config and the MCP server entry from
//	the harness's configuration directory, so the audited fact is "the
//	harness configuration changed" — which is what KindConfigReload names.
//	The row's Actor names the plugin and its Outcome names the uninstall,
//	so a reader tells an uninstall from a reload without a fifteenth kind
//	existing to tell them apart. This is the same trade R-14.243's work
//	made when it reused KindPolicyRoute for the auto-advance trail.
//
// Inputs: the harness paths, the working directory whose instructions were
//
//	installed, and an audit.Writer.
//
// Outputs: an error only when the REMOVAL failed. A failed audit write is
//
//	returned too — an uninstall nobody can prove happened is the thing this
//	hook exists to prevent — but it is reported after the removal has
//	already been attempted, never instead of it.
//
// SPORT: internal/plugins cascade-claude uninstall hook (ADD) — P1-E16-W4-S34-T1.

package plugins

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
	claude "github.com/acamarata/cascade/plugins/claude"
)

// claudeUninstallActor names this hook in the audit log.
const claudeUninstallActor = "plugin.cascade-claude.uninstall"

// claudeUninstallOutcome is the row's outcome, which is what separates an
// uninstall from a config reload under the shared kind.
const claudeUninstallOutcome = "plugin_uninstalled"

// ClaudeTeardown is the ProcessTeardown for cascade-claude.
type ClaudeTeardown struct {
	paths  claude.Paths
	cwd    string
	writer audit.Writer
	// detector answers which harnesses this machine has, so the teardown
	// can tell which files another one still reads.
	detector harnessDetector
}

// NewClaudeTeardown builds the teardown hook.
//
// The audit writer may be nil, which is how a CLI invocation with no
// daemon-side log degrades: the files are still removed, and the caller is
// told nothing was recorded. The alternative — refusing to uninstall
// because the row could not be written — would leave an operator unable to
// remove a plugin from a machine whose audit log is unavailable.
func NewClaudeTeardown(paths claude.Paths, cwd string, writer audit.Writer) *ClaudeTeardown {
	return &ClaudeTeardown{paths: paths, cwd: cwd, writer: writer, detector: hostHarnessDetector()}
}

// WithDetector returns t using d to decide which harnesses are installed.
// The unit lane states an installed set instead of installing one.
func (t *ClaudeTeardown) WithDetector(d harnessDetector) *ClaudeTeardown {
	t.detector = d
	return t
}

// Teardown runs cascade-claude's removal and records it.
//
// A name that is not this plugin's is a no-op rather than an error: the
// teardown seam takes a name precisely so one implementation can be handed
// to RemovePlugin for every plugin, and answering "not mine" is how it
// declines.
func (t *ClaudeTeardown) Teardown(ctx context.Context, name string) error {
	if name != claudePackName {
		return nil
	}
	// The shared set is computed HERE, where both the detector and every
	// harness generator are reachable; the adapter is told, never left to
	// guess (R-14.265). A detection failure aborts rather than falling
	// back to "nothing is shared", which is the answer that deletes a file
	// the other harness was still reading.
	shared, sharedErr := sharedPathsFor(ctx, t.detector, casctx.HarnessClaude, t.cwd)
	if sharedErr != nil {
		return sharedErr
	}
	results, removeErr := claude.Uninstall(ctx, t.paths, t.cwd, shared)
	auditErr := t.record(ctx, results, removeErr)
	if removeErr != nil {
		return removeErr
	}
	return auditErr
}

// uninstallRow is the audit row's rationale payload: which files went, and
// which were deliberately kept.
type uninstallRow struct {
	Plugin  string   `json:"plugin"`
	Removed []string `json:"removed,omitempty"`
	Kept    []string `json:"kept,omitempty"`
	Absent  []string `json:"absent,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// record writes the audit row. It is written whether or not anything was
// removed: "an uninstall ran and found nothing" is a fact an operator may
// need as much as the other one.
func (t *ClaudeTeardown) record(ctx context.Context, results []claude.UninstallResult, removeErr error) error {
	if t.writer == nil {
		return nil
	}
	row := uninstallRow{Plugin: claudePackName}
	for _, r := range results {
		switch {
		case r.Removed:
			row.Removed = append(row.Removed, r.Path)
		case r.Kept:
			row.Kept = append(row.Kept, r.Path)
		default:
			row.Absent = append(row.Absent, r.Path)
		}
	}
	if removeErr != nil {
		row.Error = removeErr.Error()
	}
	payload, err := json.Marshal(row)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "plugins: encoding the cascade-claude uninstall row")
	}
	if _, err := t.writer.Append(ctx, audit.Event{
		Kind:    audit.KindConfigReload,
		Actor:   claudeUninstallActor,
		Action:  claudePackName,
		Outcome: claudeUninstallOutcome,
		Explain: payload,
	}); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "plugins: recording the cascade-claude uninstall")
	}
	return nil
}
