//go:build !windows

// Purpose: the composition-root call site for conductor.expand (R-21.68,
// P1-E43-W9-S83-T1's own AQ-owned row of the AP/S-82.T1 conductor.*
// manifest): opens the jobs_claim/jobs_claim_evidence schema on the
// daemon's real cascade.db, builds a real providers/fs.BlobStore over the
// daemon's data directory, and calls internal/rpc.RegisterConductorExpand
// with a real evidence.Fetcher over both -- the same treatment
// daemon_unix_conductor.go gives conductor.execute, split into its own
// file for the identical reason: buildRPCServer's own registration list
// sits permanently at the 300-line cap (daemon_unix_handlers.go's header
// comment), so each registering ticket gets its own sibling file rather
// than growing the shared one.
//
// Inputs: the *rpc.Registry buildRPCServer already built, its
// runtime.PathProvider and runtime.Clock (every sibling registerXHandler
// call already threads both through -- see buildRPCServer's own doc
// comment).
//
// Outputs: "conductor.expand" registered on registry, backed by a real
// evidence.Store (over cascade.db) and a real evidence.Fetcher (over that
// Store and a real providers/fs.BlobStore under
// {CASCADE_HOME}/data/blobs).
//
// Constraints: opens its OWN *sql.DB handle to cascade.db rather than
// reusing platformDaemonRun's rawDB, the same documented tradeoff
// internal/daemon/context_scope.go's RegisterContextScopeHandler already
// accepts for the identical reason (threading rawDB into buildRPCServer's
// signature would ripple into call sites outside this ticket's
// files_scope); evidence.ApplySchema is idempotent by contract, so a
// second connection applying the same schema is a no-op after the first.
// NewCapturer is deliberately NOT constructed here: nothing on the
// conductor.expand read path calls Capture/Pin/Referenced, and its real
// caller is AQ/S-83.T2's distiller, which does not exist in this tree yet
// (internal/build/testonly-allow.json's own entry for
// internal/evidence.NewCapturer, updated by this change to name that
// ticket instead of UNOWNED).
//
// SPORT: cmd/cascade/daemon (ADD, R-21.68).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/evidence"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/fs"
)

// evidenceBlobsDir is where captured evidence content lives:
// {CASCADE_HOME}/data/blobs. It sits beside recallIndexDir's
// {CASCADE_HOME}/data/retrieval for the same reason -- derived,
// content-addressed state under the data directory, never beside config.
func evidenceBlobsDir(paths runtime.PathProvider) string {
	return filepath.Join(paths.DataDir(), "blobs")
}

// wireConductorExpand opens the jobs_claim/jobs_claim_evidence schema on
// the daemon's real cascade.db, builds a real evidence.Fetcher over a
// real providers/fs.BlobStore, and calls rpc.RegisterConductorExpand.
func wireConductorExpand(ctx context.Context, registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) error {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "daemon: conductor.expand: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "daemon: conductor.expand: open cascade.db")
	}
	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := evidence.ApplySchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backupDir); err != nil {
		_ = db.Close()
		return err
	}
	blobs, err := fs.New(evidenceBlobsDir(paths))
	if err != nil {
		_ = db.Close()
		return err
	}
	store := evidence.NewStore(db, clock)
	fetcher := evidence.NewFetcher(store, blobs)
	return rpc.RegisterConductorExpand(registry, fetcher)
}
