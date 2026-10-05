// Purpose: unit proof that a URL whose password holds an unescaped '/',
// '?' or '#' after a numeric head (pgx reads the head as the port and the
// tail as the path) or that lacks "scheme://" never reaches a caller
// through WrapConn, Detach or URLCause: the real pgx ConnectError for
// such a DSN prints database=<tail> and host:<head>, so the bare canary
// and the head are forbidden alongside the DSN. Nothing is dialed: the
// lookup is stubbed and the context is canceled before the dial, so the
// error is pgx's own per-dial ConnectError without a socket (the
// no-network unit gate keeps "net" out of this file).

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

// ambiguousForm is one DSN whose authority ends before its last '@', and
// the leading password segment pgx would print as a port.
type ambiguousForm struct{ name, dsn, seg string }

// ambiguousForms lists the slash, query-mark and hash forms, each with a
// host pgx resolves (u) and a loopback host, plus the single-slash forms
// pgx refuses to parse. Every seg is a five-digit port, so a forbid on it
// cannot match by chance.
func ambiguousForms(c string) []ambiguousForm {
	return []ambiguousForm{
		{"slash", "postgres://u:42431/" + c + "@localhost/db?sslmode=disable", "42431"},
		{"slash-loopback", "postgres://127.0.0.1:42432/" + c + "@localhost/db", "42432"},
		{"slash-empty-host", "postgres://:42433/" + c + "@h/db", "42433"},
		{"query-mark", "postgres://u:42434?" + c + "@localhost/db", "42434"},
		{"query-mark-loopback", "postgres://127.0.0.1:42435?" + c + "@localhost/db", "42435"},
		{"hash", "postgres://u:42436#" + c + "@h/db", "42436"},
		{"hash-loopback", "postgres://127.0.0.1:42437#" + c + "@h/db", "42437"},
		{"single-slash", "postgres:/u:" + c + "@localhost/db", "u:" + c},
		{"single-slash-query-mark", "postgres:/u:42438?" + c + "@localhost/db", "42438"},
	}
}

// dialConnectError returns the real *pgconn.ConnectError pgx builds when
// every dial for dsn fails: its text names host:port and the database.
// The lookup is stubbed and the context is already canceled, so the dialer
// returns before it opens a socket.
func dialConnectError(t *testing.T, dsn string) error {
	t.Helper()
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgconn.ParseConfig: %s", kindOf(err))
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = pgconn.ConnectConfig(canceled, cfg)
	var ce *pgconn.ConnectError
	if err == nil || !errors.As(err, &ce) {
		t.Fatalf("stub connect = %s, want a *pgconn.ConnectError", kindOf(err))
	}
	return err
}

// driverErrors lists the driver errors a connect for f.dsn can surface:
// a synthetic echo of the DSN and, when pgx parses it, the real lookup and
// dial ConnectErrors; otherwise the real ParseConfigError.
func driverErrors(t *testing.T, f ambiguousForm) []error {
	t.Helper()
	errs := []error{errors.New("dial " + f.dsn), fmt.Errorf("ping: %w", errors.New("echo "+f.seg))}
	if _, perr := pgconn.ParseConfig(f.dsn); perr != nil {
		return append(errs, perr)
	}
	return append(errs, lookupConnectError(t, f.dsn, ""), dialConnectError(t, f.dsn))
}

func TestAmbiguousDSNWithholdsDriverText(t *testing.T) {
	isolatePGEnv(t)
	c := canary()
	rawHits, checked := 0, 0
	for _, f := range ambiguousForms(c) {
		forbid := []string{c, f.seg, f.dsn}
		if s, ok := Secrets(f.dsn); ok || s != nil {
			t.Errorf("%s: Secrets must report unknown (nil, false)", f.name)
		}
		if got := Redact(f.dsn); got != placeholder {
			t.Errorf("%s: Redact must give the placeholder", f.name)
		}
		errs := driverErrors(t, f)
		if _, perr := pgconn.ParseConfig(f.dsn); perr == nil && countHits(errs[len(errs)-1].Error(), []string{f.seg}) == 0 {
			t.Errorf("%s: the raw dial ConnectError must print the leading segment as the port", f.name)
		}
		for i, e := range errs {
			rawHits += countHits(e.Error(), forbid[:2])
			name := fmt.Sprintf("%s/wrap#%d", f.name, i)
			got := WrapConn(e, f.dsn, cascade.KindUnavailable, "store: connect %s", "ns")
			assertNoLeak(t, name, got, forbid)
			assertNoLeak(t, name+"/chained", fmt.Errorf("open store: %w", got), forbid)
			if cause := errors.Unwrap(got); cause == nil || cause.Error() != withheld {
				t.Errorf("%s: the cause must be withheld", name)
			}
			var ce *pgconn.ConnectError // Detach returns a non-pgx error unchanged by contract
			if errors.As(e, &ce) {
				secrets, _ := Secrets(f.dsn)
				assertNoLeak(t, name+"/detach", Detach(e, secrets), forbid)
			}
			checked++
		}
	}
	if rawHits == 0 {
		t.Fatal("no raw driver error holds the canary or the leading segment: the forms no longer prove the leak shape")
	}
	t.Logf("checked %d wrapped errors; raw driver texts holding the canary or segment: %d", checked, rawHits)
}

func TestURLCauseWithholdsWhenTheURLDoesNotRender(t *testing.T) {
	c := canary()
	schemes := []string{"redis", "rediss", "unix"}
	dial := fmt.Errorf("dial tcp 127.0.0.1:42441: %w", context.DeadlineExceeded)
	if got := URLCause(dial, "redis://u:"+c+"@127.0.0.1:42441/0", schemes...); got != dial {
		t.Error("URLCause must keep a clean error for a URL that renders")
	}
	if URLCause(nil, "redis://h:6379/0", schemes...) != nil {
		t.Error("URLCause(nil) must be nil")
	}
	for _, tc := range []struct{ name, raw, seg string }{
		{"hash", "redis://127.0.0.1:42441#" + c + "@h/0", "42441"},
		{"hash-empty-host", "redis://:42441#" + c + "@h/0", "42441"},
		{"query-mark", "redis://127.0.0.1:42441?" + c + "@h/0", "42441"},
		{"slash", "redis://127.0.0.1:42441/" + c + "@h/0", "42441"},
		{"single-slash", "redis:/u:" + c + "@127.0.0.1:42441", "42441"},
		{"text-holds-password", "redis://u:" + c + "@h:6379/0", "42441"},
		{"scheme-not-listed", "http://127.0.0.1:42441/", "42441"},
	} {
		err := dial
		if tc.name == "text-holds-password" {
			err = fmt.Errorf("auth %s: %w", c, context.DeadlineExceeded)
		}
		got := URLCause(err, tc.raw, schemes...)
		wrapped := cascade.Wrap(cascade.KindUnavailable, got, "cache: connect "+RedactURL(tc.raw, schemes...))
		assertNoLeak(t, tc.name, wrapped, []string{c, tc.seg, tc.raw})
		if got.Error() != withheld || !errors.Is(got, context.DeadlineExceeded) {
			t.Errorf("%s: want the withheld text and the context sentinel kept", tc.name)
		}
	}
	if !strings.Contains(dial.Error(), "42441") {
		t.Fatal("the fixture dial error must name the port the forms forbid")
	}
}
