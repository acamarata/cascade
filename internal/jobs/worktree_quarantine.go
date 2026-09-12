package jobs

// Purpose: R-21.140/R-21.177's quarantine path — the sweep's only
//
//	response to a released, dead-pgid orphan whose tree is DIRTY. A dirty
//	orphan is MOVED, never deleted: the working tree relocates to
//	<repo>/.cascade/worktrees/quarantine/job-<id>, git admin metadata is
//	detached with `git worktree remove --force` AFTER the move, the row
//	is re-keyed to the new path, and exactly one R/S-39.T1 attention item
//	is raised — all journaled with the evidence that justified it.
//
// Inputs: the row being quarantined, its lease, and the sweep's own
//
//	evidence (pgid probe result, dirty file count) — never re-derived
//	here, so the journal entry always reflects what the sweep actually
//	observed.
//
// Outputs: the row moved on disk and in the store; one journal
//
//	KindEscalation entry; one supervision.AttentionItem push (idempotent
//	on (kind, source_ref), matching lease_events.go's fenced/expired
//	precedent exactly).
//
// Constraints: no file is ever deleted by this path — os.Rename only.
//
//	--force is used ONLY here, and only on the path AFTER its directory
//	has already been relocated away from it.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
)

// quarantineDirName is the R-21.140 verbatim quarantine path segment,
// nested under worktreesDirName (worktree.go).
const quarantineDirName = "quarantine"

// quarantinePath returns the R-21.140 verbatim quarantine location for
// jobID under repoRoot.
func quarantinePath(repoRoot, jobID string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(worktreesDirName), quarantineDirName, "job-"+jobID)
}

// dirtyFileCount counts the non-empty lines of a `git status --porcelain`
// output — the journal metadata's "dirty file count".
func dirtyFileCount(porcelain string) int {
	n := 0
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// quarantineJournalPayload is the R-21.140 structured metadata: {lease id,
// epoch, holder job id, pgid probe result, dirty file count, moved-from,
// moved-to}.
type quarantineJournalPayload struct {
	LeaseID       string `json:"lease_id"`
	Epoch         int64  `json:"epoch"`
	Holder        string `json:"holder"`
	PGIDProbe     string `json:"pgid_probe_result"`
	DirtyFileCnt  int    `json:"dirty_file_count"`
	MovedFrom     string `json:"moved_from"`
	MovedTo       string `json:"moved_to"`
	AttentionItem string `json:"attention_item_id"`
}

// quarantine moves row's dirty tree to quarantine, detaches git admin
// metadata, re-keys the store row, journals the evidence, and raises
// exactly one attention item. pgidProbeResult and dirtyCount are the
// sweep's own already-collected evidence, recorded verbatim.
func (m *WorktreeManager) quarantine(ctx context.Context, lease ResourceLease, row Worktree, pgidProbeResult string, dirtyCount int) error {
	jobID := lease.Holder
	dest := quarantinePath(row.Repo, jobID)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: create quarantine directory")
	}
	if err := os.Rename(row.Path, dest); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "jobs: move dirty worktree %q to quarantine", row.Path)
	}
	// Best-effort admin detach: the directory is already gone from
	// row.Path, so --force clears the now-stale admin entry rather than
	// operating on a live tree.
	_, _ = m.runGitAdmin(ctx, row.Repo, row.Repo, "worktree", "remove", "--force", row.Path)

	moved := Worktree{Path: dest, LeaseRepoID: row.LeaseRepoID, LeaseScopeGlob: row.LeaseScopeGlob, Repo: row.Repo, Branch: row.Branch}
	if err := moveWorktreeRow(ctx, m.store, row.Path, moved); err != nil {
		return err
	}

	itemID, err := m.pushQuarantineAttention(ctx, lease)
	if err != nil {
		return err
	}
	return m.appendQuarantineJournal(ctx, lease, quarantineJournalPayload{
		LeaseID: leaseEntityID(lease.RepoID, lease.ScopeGlob), Epoch: lease.Epoch, Holder: jobID,
		PGIDProbe: pgidProbeResult, DirtyFileCnt: dirtyCount, MovedFrom: row.Path, MovedTo: dest, AttentionItem: itemID,
	})
}

// pushQuarantineAttention raises exactly one attention item for lease
// (idempotent on (kind, source_ref), the same rule lease_events.go's
// fenced/expired paths use). A nil attention store disables this (unit
// tests that only assert store/git state).
func (m *WorktreeManager) pushQuarantineAttention(ctx context.Context, lease ResourceLease) (string, error) {
	if m.attention == nil {
		return "", nil
	}
	item, err := m.attention.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindStall,
		SourceRef: leaseEntityID(lease.RepoID, lease.ScopeGlob),
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindProject, ID: lease.RepoID},
	})
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

func (m *WorktreeManager) appendQuarantineJournal(ctx context.Context, lease ResourceLease, payload quarantineJournalPayload) error {
	if m.journal == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode quarantine journal payload")
	}
	_, err = m.journal.Append(ctx, leaseEntityID(lease.RepoID, lease.ScopeGlob), journal.KindEscalation, "quarantined", json.RawMessage(raw))
	return err
}
