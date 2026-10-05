//go:build postgres

// Purpose: maps a database/sql error produced against the pgx driver to
//
//	the taxonomy Kind that best describes its real cause, following
//	providers/sqlite/errors.go's exact pattern (classify by structured
//	error code, never by string-matching the message). Connection errors
//	go through providers/internal/dsnredact, the one DSN redactor, so no
//	error echoes a DSN's password (A/S-01.T7, 08 §2 secret-custody rule
//	extended to error messages).
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
//	This package holds no redactor of its own: dsnredact renders the DSN
//	(fail closed to a placeholder) and keeps the raw pgx error out of the
//	chain.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).

package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/internal/dsnredact"
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
// Never includes a DSN: callers pass only static context (namespace/key
// are safe: they are not credentials). A pgx connection-stage error from
// a pooled reconnect is detached first (dsnredact.Detach with no secret
// set, so its text is withheld): its Config and ConnString never reach a
// caller through the chain.
func wrapDBError(err error, format string, args ...any) error {
	return cascade.Wrapf(classifyPgError(err), dsnredact.Detach(err, nil), format, args...)
}

// wrapConnError wraps a connection-establishment error (Open/Ping) through
// dsnredact.WrapConn under classifyPgError's Kind. dsn only names what the
// message and cause must not carry; the raw driver error never enters the
// returned chain.
func wrapConnError(err error, dsn, format string, args ...any) error {
	return dsnredact.WrapConn(err, dsn, classifyPgError(err), format, args...)
}
