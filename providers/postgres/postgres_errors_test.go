//go:build postgres

// Purpose: unit tests for classifyPgError, wrapDBError/wrapConnError, and
//
//	redactDSN — no live server needed.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).
package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestClassifyPgError_Codes(t *testing.T) {
	cases := []struct {
		code string
		want cascade.Kind
	}{
		{sqlstateUniqueViolation, cascade.KindConflict},
		{sqlstateCheckViolation, cascade.KindConflict},
		{sqlstateSerializationFail, cascade.KindConflict},
		{sqlstateForeignKeyViolation, cascade.KindInvalidInput},
		{sqlstateNotNullViolation, cascade.KindInvalidInput},
		{sqlstateInvalidPassword, cascade.KindPermissionDenied},
		{sqlstateInsufficientPriv, cascade.KindPermissionDenied},
		{sqlstateInvalidCatalogName, cascade.KindUnavailable},
		{sqlstateUndefinedTable, cascade.KindUnavailable},
		{sqlstateDiskFull, cascade.KindQuotaExhausted},
		{sqlstateOutOfMemory, cascade.KindQuotaExhausted},
		{sqlstateTooManyConnections, cascade.KindQuotaExhausted},
		{"99999", cascade.KindUnavailable}, // unrecognized code -> safe default
	}
	for _, c := range cases {
		got := classifyPgError(&pgconn.PgError{Code: c.code})
		if got != c.want {
			t.Errorf("classifyPgError(code=%s) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestClassifyPgError_NonPgError(t *testing.T) {
	if got := classifyPgError(errors.New("dial tcp: connection refused")); got != cascade.KindUnavailable {
		t.Fatalf("classifyPgError(non-PgError) = %v, want KindUnavailable", got)
	}
}

func TestWrapDBError_CarriesKind(t *testing.T) {
	err := wrapDBError(&pgconn.PgError{Code: sqlstateUniqueViolation}, "postgres: put %s/%s", "ns", "k")
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("wrapDBError = %v, want KindConflict", err)
	}
	if !strings.Contains(err.Error(), "ns/k") {
		t.Fatalf("wrapDBError message = %v, want it to contain the formatted context", err)
	}
}

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		dsn      string
		wantHide string
	}{
		{"postgres://user:hunter2@localhost:5432/db?sslmode=disable", "hunter2"},
		{"postgres://user:another-secret@host/db", "another-secret"},
	}
	for _, c := range cases {
		got := redactDSN(c.dsn)
		if strings.Contains(got, c.wantHide) {
			t.Errorf("redactDSN(%q) = %q, still contains the password %q", c.dsn, got, c.wantHide)
		}
	}
	// A DSN net/url cannot parse at all must still never echo any of its
	// own content.
	malformed := "postgres://[::not-a-valid-host/db"
	if got := redactDSN(malformed); got != redactedDSNPlaceholder {
		t.Errorf("redactDSN(malformed) = %q, want the fixed placeholder %q", got, redactedDSNPlaceholder)
	}
}

func TestWrapConnError_NeverIncludesRawDSN(t *testing.T) {
	dsn := "postgres://cascade:top-secret@127.0.0.1:1/db"
	err := wrapConnError(errors.New("dial failed"), dsn, "postgres: connect")
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("wrapConnError leaked the password: %v", err)
	}
}
