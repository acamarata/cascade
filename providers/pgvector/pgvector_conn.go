//go:build postgres

// Purpose: the pgvector error seam. It classifies a pgx database/sql
// driver error and hands it to providers/internal/dsnredact, the one DSN
// redactor, so no error carries a DSN credential in its text or chain.
//
// Inputs: a driver error and, at the connection stage, the DSN it was
// dialed with (later, that DSN's secret set). The DSN only names the
// secrets that must stay out; it is never interpolated.
//
// Outputs: a *cascade.Error. wrapConnError maps PgError 28P01/42501 to
// KindPermissionDenied, *pgconn.ParseConfigError to KindInvalidInput and
// anything else to KindUnavailable; its message carries dsnredact.Redact.
//
// Constraints: fail closed. The raw pgx error never enters the returned
// chain (dsnredact.WrapConn and dsnredact.Detach keep only a credential-free
// cause that unwraps to a context sentinel at most).
//
// SPORT: providers.pgvector.wrapConnError/CHANGED.

package pgvector

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/internal/dsnredact"
)

// wrapConnError wraps a connection-stage error (Open, Ping) through
// dsnredact.WrapConn. PgError 28P01/42501 is KindPermissionDenied,
// *pgconn.ParseConfigError KindInvalidInput, anything else
// KindUnavailable. The message is format and args followed by the
// redacted DSN; the cause is checked driver text or withheld.
func wrapConnError(err error, dsn, format string, args ...any) error {
	kind := classifyPgError(err)
	if kind != cascade.KindPermissionDenied {
		kind = cascade.KindUnavailable
	}
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		kind = cascade.KindInvalidInput
	}
	return dsnredact.WrapConn(err, dsn, kind, format, args...)
}

// wrapDBError wraps a query-stage error under classifyPgError's Kind. A
// connection-stage error from a pooled reconnect is detached first against
// secrets (the store's dsnredact.Secrets), so no call site can hand a
// caller the driver's Config, raw DSN or any DSN password.
func wrapDBError(err error, secrets []string, format string, args ...any) error {
	return cascade.Wrapf(classifyPgError(err), dsnredact.Detach(err, secrets), format, args...)
}
