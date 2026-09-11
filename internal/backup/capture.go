// Purpose: the §D-15 capture adapters — SQLite domains captured via a
//
//	consistent snapshot (VACUUM INTO), never a raw copy of a live WAL;
//	the Exporter seam a future non-SQLite domain export implements.
//
// Inputs: an open *sql.DB and a storage.DomainID (SQLiteCapture) or
//
//	whatever a future Exporter implementation needs.
//
// Outputs: an io.ReadCloser over the domain's exported byte stream, taken
//
//	at one consistent instant.
//
// Constraints: never opens a raw copy of the live database file — SQLite's
//
//	VACUUM INTO runs inside SQLite's own deferred read transaction, so an
//	uncommitted writer on DB is invisible to the capture regardless of
//	timing (TestCaptureExcludesUncommittedWrite proves this without any
//	sleep-based synchronization, per Art.7.3). A capture that fails at
//	any step returns a KindIntegrity or KindUnavailable *cascade.Error and
//	leaves no partial temp file behind — it never ships a torn snapshot.
//
// SPORT: internal.backup.capture/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Exporter produces the byte stream for one domain at a single consistent
// point in time. SQLiteCapture is the only concrete implementation this
// ticket builds; a future non-SQLite domain (§D-15's "export path") plugs
// into Pipeline/Snapshot by implementing this interface directly — see
// this ticket's journal for the contract-vs-tree note on why that second
// implementation does not exist yet.
type Exporter interface {
	Export(ctx context.Context) (io.ReadCloser, error)
}

// SQLiteCapture captures one SQLite-backed domain: `VACUUM INTO` a fresh
// temp file under Dir (SQLite's consistent-snapshot primitive — see the
// package doc comment for why this, and not the C-level online-backup API,
// is what this pure-Go build uses), then storage.Export (B/S-03.T3) reads
// the isolated copy. The temp file is always removed before Export
// returns, success or failure.
type SQLiteCapture struct {
	DB     *sql.DB
	Domain storage.DomainID
	// Dir is the directory the temp snapshot copy is created under.
	// Required.
	Dir string
}

// Export implements Exporter.
func (c SQLiteCapture) Export(ctx context.Context) (io.ReadCloser, error) {
	if c.DB == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: SQLiteCapture requires a non-nil DB")
	}
	if c.Dir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: SQLiteCapture requires a non-empty Dir")
	}
	path, err := reserveCaptureTempPath(c.Dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(path) }()

	if _, err := c.DB.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: capture snapshot for domain "+string(c.Domain))
	}
	return exportCaptureSnapshot(ctx, path, c.Domain)
}

// reserveCaptureTempPath returns a not-yet-existing path under dir:
// `VACUUM INTO` refuses to write over an existing file, so this creates a
// placeholder via os.CreateTemp (for a collision-free unique name) and then
// removes it, leaving only the name reserved.
func reserveCaptureTempPath(dir string) (string, error) {
	tmp, err := os.CreateTemp(dir, "backup-capture-*.db")
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "backup: reserve capture temp file")
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "backup: close capture temp file")
	}
	if err := os.Remove(path); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "backup: clear capture temp file")
	}
	return path, nil
}

// exportCaptureSnapshot opens the VACUUM INTO copy at path and runs
// storage.Export against it into an in-memory buffer, so the returned
// io.ReadCloser holds no reference to the temp file (already removed by
// the caller's defer by the time the caller reads from it).
func exportCaptureSnapshot(ctx context.Context, path string, domain storage.DomainID) (io.ReadCloser, error) {
	copyDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: open capture snapshot")
	}
	defer func() { _ = copyDB.Close() }()

	var buf bytes.Buffer
	if err := storage.Export(ctx, copyDB, domain, &buf); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: export captured domain")
	}
	return io.NopCloser(&buf), nil
}
