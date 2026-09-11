//go:build postgres

// Purpose: maps a database/sql error produced against the pgx driver to
//
//	the taxonomy Kind that best describes its real cause, following
//	providers/sqlite/errors.go's exact pattern (classify by structured
//	error code, never by string-matching the message). Also carries
//	redactDSN, the credential-redaction path this package uses on every
//	error that might otherwise echo a DSN's password (A/S-01.T7,
//	08 §2 secret-custody rule extended to error messages).
//
// Inputs: classifyPgError(err) — an error returned by database/sql against
//
//	the "pgx" driver (ExecContext/QueryRowContext/BeginTx/Commit).
//
// Outputs: the taxonomy Kind to wrap err in; wrapDBError/wrapConnError
//
//	additionally perform the cascade.Wrapf call so call sites stay one
//	line.
//
// Constraints: classification reads ONLY the Postgres SQLSTATE code via
//
//	errors.As + jackc/pgconn's *PgError.Code, never the error string.
//	redactDSN never returns a substring of the input that could contain a
//	password — it parses via net/url and calls URL.Redacted (the
//	standard-library redaction path used across the Go ecosystem for
//	exactly this problem), falling back to a fixed placeholder for a DSN
//	net/url cannot parse at all, so a malformed DSN still leaks nothing.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).

package postgres

import (
	"errors"
	"net/url"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SQLSTATE class/code prefixes this package distinguishes. Full list:
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	sqlstateUniqueViolation     = "23505" // integrity_constraint_violation: unique_violation
	sqlstateForeignKeyViolation = "23503"
	sqlstateNotNullViolation    = "23502"
	sqlstateCheckViolation      = "23514"
	sqlstateInvalidPassword     = "28P01" // invalid_password
	sqlstateInsufficientPriv    = "42501" // insufficient_privilege
	sqlstateInvalidCatalogName  = "3D000" // database does not exist
	sqlstateUndefinedTable      = "42P01"
	sqlstateDiskFull            = "53100"
	sqlstateOutOfMemory         = "53200"
	sqlstateTooManyConnections  = "53300"
	sqlstateSerializationFail   = "40001" // could not serialize access
)

// classifyPgError reports the taxonomy Kind that best fits err's real
// Postgres cause via its SQLSTATE code. Any error that does not wrap a
// *pgconn.PgError (a network/dial failure, context deadline, driver-level
// error before the server ever replied) falls through to KindUnavailable —
// correct for "could not reach or complete the exchange with the server".
func classifyPgError(err error) cascade.Kind {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return cascade.KindUnavailable
	}
	switch pgErr.Code {
	case sqlstateUniqueViolation, sqlstateCheckViolation, sqlstateSerializationFail:
		return cascade.KindConflict
	case sqlstateForeignKeyViolation, sqlstateNotNullViolation:
		return cascade.KindInvalidInput
	case sqlstateInvalidPassword, sqlstateInsufficientPriv:
		return cascade.KindPermissionDenied
	case sqlstateInvalidCatalogName, sqlstateUndefinedTable:
		return cascade.KindUnavailable
	case sqlstateDiskFull, sqlstateOutOfMemory, sqlstateTooManyConnections:
		return cascade.KindQuotaExhausted
	default:
		return cascade.KindUnavailable
	}
}

// wrapDBError wraps err as a *cascade.Error under classifyPgError's Kind.
// Never includes a DSN — callers pass only static context (namespace/key
// are safe: they are not credentials).
func wrapDBError(err error, format string, args ...any) error {
	return cascade.Wrapf(classifyPgError(err), err, format, args...)
}

// wrapConnError wraps a connection-establishment error (Open/Ping), taking
// dsn ONLY to compute its redacted form for the message — the raw dsn
// itself is never interpolated. A dial/auth failure at this stage
// virtually never carries a *pgconn.PgError (the exchange did not
// complete), so this always classifies as KindUnavailable except the one
// case classifyPgError already distinguishes (bad password after a
// completed auth handshake, sqlstateInvalidPassword -> KindPermissionDenied).
func wrapConnError(err error, dsn, format string, args ...any) error {
	kind := classifyPgError(err)
	msg := append(append([]any{}, args...), redactDSN(dsn))
	return cascade.Wrapf(kind, err, format+" %s", msg...)
}

// redactedDSNPlaceholder stands in for a DSN net/url cannot parse at all,
// so an unparseable (and therefore unpredictable-shape) DSN still cannot
// leak a credential fragment into an error message.
const redactedDSNPlaceholder = "postgres://<redacted>"

// redactDSN returns dsn with any userinfo password replaced, using the
// standard-library net/url.URL.Redacted() method — the idiomatic Go path
// for exactly this problem, already in this module's dependency graph via
// net/url (stdlib, no new dependency). A dsn that fails to parse as a URL
// returns the fixed placeholder rather than any fragment of the input.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return redactedDSNPlaceholder
	}
	return u.Redacted()
}
