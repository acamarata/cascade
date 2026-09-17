// Purpose: the human rendering of the domain reports the CLI hands
// straight to output.Writer.Result — daemon service deltas, the fleet
// bench, the retrieval lifecycle verbs, the backup reports, and the MCP
// tool registry.
//
// WRAPPED HERE, NOT GIVEN String() IN THEIR OWN PACKAGES. These types
// belong to domains that have no opinion about terminals: a String method
// on internal/backup.RestoreReport would make one CLI's line wrapping a
// property of the restore engine. The wrapper is this package's, the way
// syncStatusView wraps sync.StatusResult, and the --json document is
// unchanged because each wrapper embeds the report it renders.
//
// SPORT: cmd/cascade report views (ADD) — P1-E19-W4-S42-T7.
package main

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
)

// serviceDeltaView renders a service install/uninstall outcome.
type serviceDeltaView struct{ service.DeltaReport }

// String says what the platform did. The ACTION leads because both verbs
// are idempotent: "reloaded" and "installed" are both success, and which
// one happened is the fact an operator is checking.
func (v serviceDeltaView) String() string {
	if v.Detail == "" {
		return string(v.Action)
	}
	return fmt.Sprintf("%s: %s", v.Action, v.Detail)
}

// benchResultView renders a lane benchmark.
type benchResultView struct{ fleet.BenchResult }

// String reports the percentiles, the error rate as a percentage, and the
// cost estimate with the unit it is actually in — output tokens, not
// money, which the field's own doc is careful about and a bare number
// would lose.
func (v benchResultView) String() string {
	return fmt.Sprintf("p50 %.0fms · p95 %.0fms · errors %.1f%% · ~%.0f output tokens per run",
		v.P50MS, v.P95MS, v.ErrorRate*100, v.CostEstimate)
}

// rebuildResultView renders a full index rebuild.
type rebuildResultView struct{ lifecycle.RebuildResult }

// String leads with convergence, because a rebuild that changed nothing
// is the expected outcome of a second run and reads as a failure if the
// counts are all it prints.
func (v rebuildResultView) String() string {
	head := fmt.Sprintf("%d corpora indexed: %d chunk(s) written, %d retracted",
		v.CorporaIndexed, v.ChunksWritten, v.ChunksDeleted)
	if v.Converged {
		head = "index already current (" + head + ")"
	}
	return head + "\nmarker: " + orNone(v.Marker)
}

// updateResultView renders an incremental re-ingest.
type updateResultView struct{ lifecycle.UpdateResult }

// String mirrors rebuildResultView's shape, for the same reason.
func (v updateResultView) String() string {
	head := fmt.Sprintf("%d file(s) changed: %d chunk(s) written, %d retracted",
		v.FilesChanged, v.ChunksWritten, v.ChunksDeleted)
	if v.Converged {
		head = "index already current (" + head + ")"
	}
	return head + "\nmarker: " + orNone(v.Marker)
}

// migrateResultView renders a schema migration.
type migrateResultView struct{ lifecycle.MigrateResult }

// String distinguishes "nothing to do" from "migrated", which is the
// whole answer this verb gives.
func (v migrateResultView) String() string {
	if !v.Applied {
		return fmt.Sprintf("retrieval schema already at version %d; nothing to do", v.ToVersion)
	}
	return fmt.Sprintf("retrieval schema migrated from version %d to %d", v.FromVersion, v.ToVersion)
}

// verifyReportView renders an index verification.
type verifyReportView struct{ lifecycle.VerifyReport }

// String leads with the verdict and then lists only what is wrong. A
// clean report that printed empty slices would make an operator read four
// lines to learn nothing happened.
func (v verifyReportView) String() string {
	var buf bytes.Buffer
	if v.Clean() {
		buf.WriteString("index verified: no missing or orphaned chunks, marker current")
		return buf.String()
	}
	buf.WriteString("index NEEDS attention\n")
	for label, ids := range map[string][]string{
		"missing (in the catalog, absent from the search index)":  v.Missing,
		"orphaned (in the search index, absent from the catalog)": v.Orphaned,
		"corpora with an incomplete vector namespace":             v.VectorIncomplete,
	} {
		if len(ids) > 0 {
			_, _ = fmt.Fprintf(&buf, "  %s: %s\n", label, strings.Join(ids, ", "))
		}
	}
	if v.MarkerStatus != lifecycle.MarkerCurrent {
		_, _ = fmt.Fprintf(&buf, "  marker %s: stored %s, current %s\n",
			v.MarkerStatus, orNone(v.StoredMarker), orNone(v.CurrentMarker))
	}
	return buf.String()
}

// importReportView renders a portable import.
type importReportView struct{ backup.ImportReport }

// String says what landed, and whether the vault came with it.
func (v importReportView) String() string {
	vault := "no vault material"
	if v.VaultImported {
		vault = "vault material imported"
	}
	return fmt.Sprintf("snapshot %s imported: %d manifest(s), %d object(s), %s",
		v.Snapshot, v.ManifestsLanded, v.ObjectsLanded, vault)
}

// restoreReportView renders a restore.
type restoreReportView struct{ backup.RestoreReport }

// String reports the three row counts separately, because "skipped" and
// "overwritten" are the two an operator checks after a restore.
func (v restoreReportView) String() string {
	return fmt.Sprintf("snapshot %s restored across %s: %d row(s) imported, %d skipped, %d overwritten",
		v.Snapshot, joinOrNone(v.Domains), v.RowsImported, v.RowsSkipped, v.RowsOverwritten)
}

// verificationReportView renders a backup verification.
type verificationReportView struct{ backup.VerificationReport }

// String leads with the verdict, and prints the failure reason when there
// is one — the only fact that matters on a failed verify.
func (v verificationReportView) String() string {
	if !v.Verified {
		return fmt.Sprintf("snapshot %s on target %s FAILED verification: %s",
			v.Snapshot, v.Target, orNone(v.FailureReason))
	}
	return fmt.Sprintf("snapshot %s on target %s verified: %d chunk(s) over a chain %d deep, in %dms",
		v.Snapshot, v.Target, v.CheckedChunks, v.ChainDepth, v.DurationMS)
}

// mcpToolsView renders the policy-filtered MCP tool registry.
type mcpToolsView struct {
	Tools      []mcp.Tool          `json:"tools"`
	Withheld   []string            `json:"withheld"`
	Unservable []string            `json:"unservable"`
	Deferred   []map[string]string `json:"deferred"`
}

// String lists what a client would see, then the three reasons a tool is
// not on that list — each of which an operator debugging a missing tool
// needs told apart.
func (v mcpToolsView) String() string {
	var buf bytes.Buffer
	if len(v.Tools) == 0 {
		buf.WriteString("no tools are exposed\n")
	} else {
		tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TOOL\tPLUGIN\tDESCRIPTION")
		for _, t := range v.Tools {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, orNone(t.PluginID), t.Description)
		}
		_ = tw.Flush()
	}
	appendReasonList(&buf, "withheld by policy", v.Withheld)
	appendReasonList(&buf, "unservable here (this process serves no such method)", v.Unservable)
	if len(v.Deferred) > 0 {
		_, _ = fmt.Fprintf(&buf, "\ndeferred (%d):\n", len(v.Deferred))
		for _, d := range v.Deferred {
			_, _ = fmt.Fprintf(&buf, "  %s → %s: %s\n", d["v1_name"], d["ticket"], d["reason"])
		}
	}
	return buf.String()
}

// appendReasonList renders one non-exposure reason, or nothing.
func appendReasonList(buf *bytes.Buffer, label string, names []string) {
	if len(names) == 0 {
		return
	}
	_, _ = fmt.Fprintf(buf, "\n%s (%d): %s\n", label, len(names), strings.Join(names, ", "))
}

// String renders `cascade daemon start|stop|restart|status`.
//
// The four verbs share one result type and one rendering, because an
// operator running `restart` wants the same three facts `status` gives —
// is it running, as which process, and for how long.
func (v statusView) String() string {
	if !v.Running {
		if v.Detail == "" {
			return "the daemon is not running"
		}
		return "the daemon is not running: " + v.Detail
	}
	line := fmt.Sprintf("the daemon is running as pid %d, up %.0fs, %d connection(s)",
		v.PID, v.UptimeS, v.Connections)
	if v.Detail != "" {
		line += "\n" + v.Detail
	}
	return line
}
