// Purpose: rebuild the recall index after a completed v1 migration.
// Inputs: migrated memory under CASCADE_HOME and the existing cascade.db.
// Outputs: a rebuild and verify report, or an integrity error carrying that report.
// Constraints: the migration ledger must be closed before this opens the database.
package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// migrationRecallCorpus classifies the memory imported by migrate v1.
var migrationRecallCorpus = corpus.Corpus{
	ID: "migrated-memory", ScopeRef: "project/migration-v1",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal, Trust: corpus.TrustTrusted,
}

// RecallRebuildError retains the verify findings after the import has committed.
type RecallRebuildError struct {
	Report lifecycle.VerifyReport
	Cause  error
}

func (e *RecallRebuildError) Error() string {
	return fmt.Sprintf("cascade migrate v1: recall index verify failed: missing=%v orphaned=%v vector_incomplete=%v marker=%s: %v",
		e.Report.Missing, e.Report.Orphaned, e.Report.VectorIncomplete, e.Report.MarkerStatus, e.Cause)
}

func (e *RecallRebuildError) Unwrap() error { return e.Cause }

func finishMigrationRecall(cmd *cobra.Command, rebuild func(context.Context) (migrationv1.RebuildIndexResult, error), closeLedger func() error) error {
	if err := closeLedger(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade migrate v1: close ledger before recall rebuild")
	}
	if rebuild == nil {
		return nil
	}
	return runMigrationRecallRebuild(cmd, rebuild)
}

// runMigrationRecallRebuild writes the report even if verification fails.
func runMigrationRecallRebuild(cmd *cobra.Command, rebuild func(context.Context) (migrationv1.RebuildIndexResult, error)) error {
	result, err := rebuild(cmd.Context())
	if writeErr := migrateOutputWriter(cmd).Result(migrationRecallView{result}); writeErr != nil {
		return writeErr
	}
	if err != nil || !result.Verify.Clean() {
		if err == nil {
			err = cascade.New(cascade.KindIntegrity, "recall index is unhealthy")
		}
		return &RecallRebuildError{Report: result.Verify, Cause: err}
	}
	return nil
}

type migrationRecallView struct{ migrationv1.RebuildIndexResult }

func (v migrationRecallView) String() string {
	return fmt.Sprintf("recall index: %d chunk(s) written; verify healthy=%t, marker=%s, missing=%v, orphaned=%v, vector_incomplete=%v",
		v.Rebuild.ChunksWritten, v.Verify.Clean(), v.Verify.MarkerStatus,
		v.Verify.Missing, v.Verify.Orphaned, v.Verify.VectorIncomplete)
}

// productionRebuildRecallIndex composes the existing FTS5 lifecycle over migrated memory.
func productionRebuildRecallIndex(ctx context.Context) (migrationv1.RebuildIndexResult, error) {
	paths := lazyPaths{}
	dataDir, root := paths.DataDir(), paths.Root()
	if dataDir == "" || root == "" {
		return migrationv1.RebuildIndexResult{}, cascade.New(cascade.KindUnavailable, "cascade migrate v1: recall paths unavailable")
	}
	store, err := sqlite.Open(ctx, filepath.Join(dataDir, "cascade.db"))
	if err != nil {
		return migrationv1.RebuildIndexResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade migrate v1: open recall store")
	}
	defer func() { _ = store.Close() }()
	index, err := retrieval.NewIndex(store)
	if err != nil {
		return migrationv1.RebuildIndexResult{}, err
	}
	return migrationv1.RebuildIndex(ctx, migrationv1.RebuildDeps{
		CatalogPath: filepath.Join(dataDir, "retrieval", "catalog.json"),
		Store:       store, Index: index, TreeHash: doctorMarkerFunc(), Clock: runtime.NewSystemClock(),
	}, migrationv1.RebuildOptions{
		Corpus: migrationRecallCorpus, Roots: []string{filepath.Join(root, "memory")},
	})
}
