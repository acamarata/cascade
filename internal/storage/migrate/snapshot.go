package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: §D-18 automatic pre-migration snapshot — before any forward
//
//	migration that advances schema_version, write a WAL-checkpointed copy
//	of the current .db file to <profile>/backups/pre-migrate-v<n>.db,
//	BEFORE the first DDL statement of that migration executes.
//
// Inputs: the write *sql.DB (for the WAL checkpoint PRAGMA), the .db
//
//	file's real filesystem path, the backup directory, and the
//	schema_version being migrated FROM (the "current" version in the
//	filename, i.e. the version this snapshot lets a caller roll back to).
//
// Outputs: the snapshot file's absolute path, or a *cascade.Error.
// Constraints: fail-closed — a failed snapshot write returns an error that
//
//	MUST block the migration (enforced by ledger.go's Apply, which calls
//	this before executing any step and aborts on error). Only meaningful
//	for SQLite (a real on-disk file to copy); Apply skips this entirely
//	when dbPath is "" (e.g. Postgres, or an in-memory SQLite test
//	database, which has no file to snapshot).
//
// SPORT: internal.storage.migrate.Snapshot/ADDED (P1-E02-W1-S02-T3).

// snapshotBeforeMigrate WAL-checkpoints db (PRAGMA wal_checkpoint(TRUNCATE)
// folds the WAL back into the main .db file so the copy below is a
// complete, self-consistent snapshot rather than a stale main file plus an
// un-copied WAL) and then copies dbPath to
// <backupDir>/pre-migrate-v<fromVersion>.db. It returns the snapshot's
// path on success. The snapshot is written synchronously, before the
// caller executes any DDL — see ledger.go's Apply.
func snapshotBeforeMigrate(ctx context.Context, db *sql.DB, dbPath, backupDir string, fromVersion int) (string, error) {
	if dbPath == "" {
		return "", nil
	}

	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "migrate: snapshot: WAL checkpoint before pre-migrate-v%d snapshot", fromVersion)
	}

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "migrate: snapshot: create backup dir %s", backupDir)
	}

	dest := filepath.Join(backupDir, fmt.Sprintf("pre-migrate-v%d.db", fromVersion))
	if err := copyFile(dbPath, dest); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "migrate: snapshot: copy %s to %s", dbPath, dest)
	}
	return dest, nil
}

// copyFile copies src to dst, creating/truncating dst. It is a plain
// byte-for-byte copy (the source file is a fully checkpointed, closed-
// transaction-consistent SQLite file at the point snapshotBeforeMigrate
// calls this, per the WAL checkpoint immediately above).
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // src is the caller's own opened database path, not external input
	if err != nil {
		return err
	}
	defer func() {
		if cerr := in.Close(); err == nil {
			err = cerr
		}
	}()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // dst is derived from the validated backup dir + a version-numbered filename, not external input
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
