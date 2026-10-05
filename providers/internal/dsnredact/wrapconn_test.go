// Purpose: unit proof that WrapConn and Detach never keep the raw pgx
// error: real ParseConfigError and ConnectError values (nothing is
// dialed) whose text holds the canary are wrapped, and no Error(), fmt
// verb, Unwrap step or errors.As target reaches the credential. The Kind
// and context sentinels survive.

package dsnredact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

// lookupConnectError returns the real *pgconn.ConnectError (Config inside)
// pgx builds when every lookup for dsn fails with echo. Nothing is dialed.
func lookupConnectError(t *testing.T, dsn, echo string) error {
	t.Helper()
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgconn.ParseConfig: %s", kindOf(err))
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) {
		return nil, errors.New("no address for " + echo)
	}
	_, err = pgconn.ConnectConfig(context.Background(), cfg)
	var ce *pgconn.ConnectError
	if err == nil || !errors.As(err, &ce) {
		t.Fatalf("stub connect = %s, want a *pgconn.ConnectError", kindOf(err))
	}
	return err
}

// parseConfigError returns the real *pgconn.ParseConfigError pgx builds
// for dsn.
func parseConfigError(t *testing.T, dsn string) error {
	t.Helper()
	_, err := pgconn.ParseConfig(dsn)
	var pe *pgconn.ParseConfigError
	if !errors.As(err, &pe) {
		t.Fatalf("ParseConfig = %T, want a *pgconn.ParseConfigError", err)
	}
	return err
}

func TestWrapConnNeverChainsTheDriverError(t *testing.T) {
	isolatePGEnv(t)
	c := canary()
	bad := "host=localhost port=bad user=u password=" + c
	good := "host=localhost user=u password=" + c + " sslmode=disable"
	parseErr, connErr := parseConfigError(t, bad), lookupConnectError(t, good, c)
	t.Logf("raw ParseConfigError echoes the canary: %v; raw ConnectError: %v",
		strings.Contains(parseErr.Error(), c), strings.Contains(connErr.Error(), c))
	for _, tc := range []struct {
		name, dsn string
		err       error
		kind      cascade.Kind
	}{
		{"parse", bad, parseErr, cascade.KindInvalidInput},
		{"parse-wrapped", bad, fmt.Errorf("sql open: %w", parseErr), cascade.KindInvalidInput},
		{"connect-echo", good, connErr, cascade.KindUnavailable},
		{"connect-echo-wrapped", good, fmt.Errorf("ping: %w", connErr), cascade.KindPermissionDenied},
		{"plain-echo", good, errors.New("refused for " + c), cascade.KindUnavailable},
		{"unparseable-dsn", "not a dsn " + c, errors.New("dial failed"), cascade.KindUnavailable},
	} {
		got := WrapConn(tc.err, tc.dsn, tc.kind, "store: connect %s", "ns")
		if !cascade.HasKind(got, tc.kind) {
			t.Errorf("%s: kind = %s, want %s", tc.name, kindOf(got), tc.kind)
		}
		assertNoLeak(t, tc.name, got, []string{c, tc.dsn})
		for e := got; e != nil; e = errors.Unwrap(e) {
			if e == tc.err || errors.Unwrap(tc.err) != nil && e == errors.Unwrap(tc.err) {
				t.Errorf("%s: the raw driver error is in the Unwrap chain", tc.name)
			}
		}
		if !strings.Contains(got.Error(), "store: connect ns") {
			t.Errorf("%s: the formatted context is missing", tc.name)
		}
	}
	echoes := 0
	for _, f := range loadForms(t, "testdata/dsn-forms.json", c) {
		if _, perr := pgconn.ParseConfig(f.DSN); perr != nil {
			echoes += countHits(perr.Error(), f.Forbid)
			assertNoLeak(t, f.Name+"/parse", WrapConn(perr, f.DSN, cascade.KindInvalidInput, "store: open"), f.Forbid)
		}
	}
	if echoes == 0 {
		t.Fatal("no raw pgx parse error in the table echoes a forbidden value: the table no longer proves the echo case")
	}
	t.Logf("raw pgx parse errors echoing a forbidden value: %d", echoes)
}

func TestWrapConnKeepsCleanTextAndContext(t *testing.T) {
	isolatePGEnv(t)
	dsn := "postgres://u:" + canary() + "@localhost/db?sslmode=disable"
	got := WrapConn(lookupConnectError(t, dsn, ""), dsn, cascade.KindUnavailable, "store: connect")
	cause := errors.Unwrap(got)
	if cause == nil || cause.Error() == withheld || !strings.Contains(cause.Error(), "failed to connect") {
		t.Errorf("a clean connect error must pass its text through (cause found=%v)", cause != nil)
	}
	assertNoLeak(t, "clean", got, []string{canary(), dsn})
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		got := WrapConn(fmt.Errorf("dial: %w", sentinel), dsn, cascade.KindUnavailable, "store: connect")
		if !errors.Is(got, sentinel) || !cascade.HasKind(got, cascade.KindUnavailable) {
			t.Errorf("WrapConn(%v) lost the context sentinel or the kind", sentinel)
		}
	}
	t.Setenv("PGSSLPASSWORD", canary()+"1")
	assertNoLeak(t, "sslpassword-env", WrapConn(errors.New("tls key: "+canary()+"1"),
		"host=localhost user=u sslmode=disable", cascade.KindUnavailable, "store: connect"), []string{canary() + "1"})
}

func TestDetachDropsConnectionErrors(t *testing.T) {
	isolatePGEnv(t)
	pw1, pw2 := canary()+"1", canary()+"2"
	dsn := "postgres://u:" + pw1 + "@localhost/db?sslmode=disable&password=" + pw2
	secrets, ok := Secrets(dsn)
	if !ok {
		t.Fatal("Secrets did not parse the fixture DSN")
	}
	echo := lookupConnectError(t, dsn, "u:"+pw1)
	assertNoLeak(t, "detach-userinfo-vs-query", Detach(echo, secrets), []string{pw1, pw2})
	assertNoLeak(t, "detach-unknown-secrets", Detach(echo, nil), []string{pw1, pw2})
	assertNoLeak(t, "detach-parse", Detach(parseConfigError(t, dsn+"&port=bad"), secrets), []string{pw1, pw2})
	clean := lookupConnectError(t, dsn, "")
	if Detach(clean, nil).Error() != withheld || Detach(clean, secrets).Error() == withheld {
		t.Error("Detach: want withheld with no secret set and the clean text with one")
	}
	plain := &pgconn.PgError{Code: "23505"}
	if got := Detach(plain, secrets); got != error(plain) {
		t.Error("Detach must return a query-stage error unchanged")
	}
	kv := "host=localhost user=u password=" + pw1 + " sslmode=disable"
	kvSecrets, _ := Secrets(kv)
	wrapped := cascade.Wrapf(cascade.KindUnavailable, Detach(lookupConnectError(t, kv, pw1), kvSecrets), "store: count %s", "ns")
	assertNoLeak(t, "detach-wrapped", wrapped, []string{pw1, kv})
}

// TestWrapConnNilErrorReturnsNil proves WrapConn does not dereference a
// nil error: no failure means no wrapped error.
func TestWrapConnNilErrorReturnsNil(t *testing.T) {
	if err := WrapConn(nil, "postgres://u@localhost/db", cascade.KindUnavailable, "open"); err != nil {
		t.Fatalf("WrapConn(nil) = %v, want nil", kindOf(err))
	}
}
