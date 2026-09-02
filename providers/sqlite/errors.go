// Purpose: maps a database/sql error produced against the modernc-sqlite
//   driver to the taxonomy Kind that best describes its real cause,
//   instead of collapsing every write-path failure to KindUnavailable
//   (CR fix 2). The same conflation was ruled on in R-14.125 for the
//   queue driver's enqueue-overflow case ("unavailable" implies retry,
//   overflow means the backend is healthy and full) — here the risk is a
//   corrupt database file and a transient lock timeout becoming
//   indistinguishable to a caller, so a generic retry middleware would
//   cheerfully retry a corrupt file forever.
// Inputs: classifyDBError(err) — an error returned by database/sql against
//   the "sqlite" driver (ExecContext/QueryRowContext/BeginTx/Commit).
// Outputs: the taxonomy Kind to wrap err in; wrapDBError additionally
//   performs the cascade.Wrapf call so call sites stay one line.
// Constraints: classification reads ONLY the SQLite result code via
//   errors.As + modernc.org/sqlite's *Error.Code(), never the error
//   string — string matching is fragile across locales and driver
//   versions. See providers/sqlite/README.md "Error taxonomy mapping" for
//   the full code table and which codes have a real test trigger.
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix).

package sqlite

import (
	"errors"

	mcsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/acamarata/cascade/pkg/cascade"
)

// classifyDBError reports the taxonomy Kind that best fits err's real
// SQLite cause. sql.ErrNoRows is handled separately by getValue (it maps
// to KindNotFound before ever reaching a write path); classifyDBError only
// sees errors from writes (Exec/Begin/Commit) and the corrupt-file case
// that can also surface from a read.
//
// Any code this switch does not explicitly recognize — including the
// always-possible case where err does not wrap a *mcsqlite.Error at all
// (e.g. a context error surfacing through a different path than
// submit's own ctx.Done() branch) — falls through to KindUnavailable,
// which is the pre-fix default this function narrows, not a new
// assumption.
func classifyDBError(err error) cascade.Kind {
	var sqliteErr *mcsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return cascade.KindUnavailable
	}
	switch sqliteErr.Code() & 0xff { // mask off any extended-result-code bits
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return cascade.KindIntegrity
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return cascade.KindUnavailable // genuinely retryable
	case sqlite3.SQLITE_CONSTRAINT:
		return cascade.KindConflict
	case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_PERM, sqlite3.SQLITE_AUTH:
		return cascade.KindPermissionDenied
	case sqlite3.SQLITE_FULL, sqlite3.SQLITE_TOOBIG:
		return cascade.KindQuotaExhausted
	default:
		return cascade.KindUnavailable
	}
}

// wrapDBError wraps err as a *cascade.Error under classifyDBError's Kind,
// with the given format message — the one-line replacement for the
// former cascade.Wrapf(cascade.KindUnavailable, err, ...) call sites in
// driver.go and tx.go.
func wrapDBError(err error, format string, args ...any) error {
	return cascade.Wrapf(classifyDBError(err), err, format, args...)
}
