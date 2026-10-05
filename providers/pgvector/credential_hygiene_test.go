//go:build postgres

// Purpose: unit proof that pgvector refuses bad credentials without echoing
// them: wrapConnError's classification, redactDSN, the reconnect detach, and
// adversarial DSNs run through Open, real pgx errors (nothing is dialed) and
// synthetic errors echoing each password spelling. Canaries never print.
package pgvector

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

// canary returns the acceptance password, assembled at run time.
func canary() string { return strings.Join([]string{"s3", "cr3t"}, "") }

// isolatePGEnv keeps pgx's config parser away from the real HOME,
// passfile, service file and PG* variables.
func isolatePGEnv(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	for _, k := range []string{"PGPASSWORD", "PGSSLPASSWORD", "PGSERVICE", "PGHOST", "PGUSER", "PGDATABASE"} {
		t.Setenv(k, "")
	}
	for _, k := range []string{"HOME", "USERPROFILE", "PGPASSFILE", "PGSERVICEFILE"} {
		t.Setenv(k, filepath.Join(tmp, "none"))
	}
}

// chainTexts renders err every way a caller could: %v, %+v, %#v, %q and
// the Error() of every error reachable through Unwrap (single and joined).
func chainTexts(err error) []string {
	texts := []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%q", err)}
	for stack := []error{err}; len(stack) > 0; {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if e == nil {
			continue
		}
		texts = append(texts, e.Error(), fmt.Sprintf("%+v", e))
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			stack = append(stack, u.Unwrap()...)
		case interface{ Unwrap() error }:
			stack = append(stack, u.Unwrap())
		}
	}
	return texts
}

// assertNoLeak fails when err is nil, when any rendering of its chain
// holds a forbidden value, or when errors.As can reach a pgx type that
// exports the DSN or Config. It never prints a forbidden value.
func assertNoLeak(t *testing.T, name string, err error, forbidden []string) {
	t.Helper()
	if err == nil || len(forbidden) == 0 {
		t.Fatalf("%s: err=%v forbidden=%d, want an error and a non-empty forbidden list", name, err == nil, len(forbidden))
	}
	for i, f := range forbidden {
		if f == "" {
			t.Fatalf("%s: forbidden value #%d is empty", name, i)
		}
		for _, text := range chainTexts(err) {
			if strings.Contains(text, f) {
				t.Errorf("%s: error text holds forbidden value #%d", name, i)
				break
			}
		}
	}
	var ce *pgconn.ConnectError
	var pe *pgconn.ParseConfigError
	if errors.As(err, &ce) || errors.As(err, &pe) {
		t.Errorf("%s: the raw pgx connection error is reachable through errors.As", name)
	}
}

// advCase is one adversarial DSN and the values no error text may hold.
type advCase struct {
	name, dsn string
	forbid    []string
}

// advCases builds the adversarial DSNs around the canary password c. The four rows before empty-password hold a value pgx never sends, so only one guard in dsnSecrets covers it.
func advCases(c string) []advCase {
	pct := ""
	for i := range len(c) {
		pct += fmt.Sprintf("%%%02X", c[i])
	}
	hostPw, dbPw := strings.Join([]string{"cvh", "0st"}, ""), strings.Join([]string{"cvd", "bname"}, "")
	part := strings.ReplaceAll(c, "c", "%63")        // partly %-encoded, as written in the DSN
	bsl, bslDec := c[:2]+`\ `+c[2:], c[:2]+" "+c[2:] // key=value backslash-escaped space
	quo, quoDec := c[:2]+`\'`+c[2:], c[:2]+"'"+c[2:] // key=value escaped quote inside quotes
	pw1, pw2 := c+"1", c+"2"                         // userinfo and query passwords that differ
	base := "postgres://u@localhost/db?sslmode=disable"
	cs := []advCase{
		{"url-userinfo", "postgres://u:" + c + "@localhost:5432/db?sslmode=disable", []string{c, "u:" + c}},
		{"url-malformed", "postgres://u:" + c + "@[::1", []string{c, "u:" + c}},
		{"url-query-password", "postgres://u@localhost/db?sslmode=disable&password=" + c, []string{c}},
		{"url-query-password-bad-port", "postgres://u@localhost:99x/db?password=" + c, []string{c}},
		{"url-percent-encoded", "postgres://u:" + pct + "@localhost/db?sslmode=disable", []string{c, pct}},
		{"url-bad-escape", "postgres://u:" + c + "%zz@localhost/db", []string{c}},
		{"url-colon-in-password", "postgres://u:" + c + ":" + c + "@[::1", []string{c}},
		{"kv-plain", "host=localhost user=u password=" + c + " dbname=db sslmode=disable", []string{c}},
		{"kv-spaced-equals", "host=localhost port=bad user=u password = " + c + " dbname=db", []string{c}},
		{"kv-spaced-equals-valid", "host=localhost user=u password = " + c + " dbname=db sslmode=disable", []string{c}},
		{"kv-quoted-spaces", "host=localhost user=u password='" + c + " x y' dbname=db sslmode=disable", []string{c, c + " x y"}},
		{"kv-quoted-bad-port", "host=localhost port=bad password='" + c + " x y'", []string{c}},
		{"password-is-host", "postgres://u:" + hostPw + "@" + hostPw + ".invalid/db?sslmode=disable", []string{hostPw}},
		{"password-in-dbname", "host=localhost user=u password=" + dbPw + " dbname=" + dbPw + "x sslmode=disable", []string{dbPw}},
		{"url-userinfo-raw-escapes", "postgres://u:" + part + "@localhost/db?sslmode=disable", []string{c, part, "u:" + part}},
		{"url-userinfo-and-query-differ", "postgres://u:" + pw1 + "@localhost/db?sslmode=disable&password=" + pw2, []string{pw1, pw2, "u:" + pw1}},
		{"url-query-raw-escapes", "postgres://u@localhost/db?sslmode=disable&password=" + part, []string{c, part}},
		{"url-query-two-passwords", "postgres://u@localhost/db?password=" + pw1 + "&password=" + pw2 + "&sslmode=disable", []string{pw1, pw2}},
		{"url-sslpassword", "postgres://u@localhost/db?sslmode=disable&sslpassword=" + part, []string{c, part}},
		{"kv-backslash-space", "host=localhost user=u password=" + bsl + " dbname=db sslmode=disable", []string{bsl, bslDec}},
		{"kv-quoted-escaped-quote", "host=localhost user=u password='" + quo + "' dbname=db sslmode=disable", []string{quo, "'" + quo + "'", quoDec}},
		{"kv-sslpassword", "host=localhost user=u sslpassword='" + bsl + "' password=" + pw1 + " sslmode=disable", []string{bsl, bslDec, pw1}},
		{"kv-password-query-mark", "host=localhost user=u password=" + c + "?x sslmode=disable", []string{c, c + "?x"}},
		{"url-query-escaped-key", base + "&password=" + pw1 + "&%70assword=" + pw2, []string{pw1, pw2}},
		{"url-query-plus-and-percent", base + "&password=" + pw1 + "&password=" + c + "+x&password=%73" + c[1:] + "+*", []string{pw1, c + " x", c + "+*"}},
		{"url-slash-password-is-port", "postgres://u:4242/" + c + "@localhost/db?sslmode=disable", []string{"4242/" + c}},
		{"kv-surrounding-space", "  host=localhost user=u sslmode=disable  ", []string{"host=localhost user=u sslmode=disable"}},
		{"empty-password", "postgres://u:@localhost/db?sslmode=disable", nil},
		{"garbage", "not a dsn " + c, []string{c}},
	}
	for i := range cs {
		cs[i].forbid = append(cs[i].forbid, cs[i].dsn)
	}
	return cs
}

// canaryFragment names the cases whose password only contains the canary.
var canaryFragment = map[string]bool{"url-colon-in-password": true, "kv-quoted-spaces": true, "kv-quoted-bad-port": true, "kv-password-query-mark": true}

// echoes lists the spellings a synthetic driver error echoes one at a time:
// all but the DSN (it would mask a missed form) and a fragment canary.
func (c advCase) echoes() (out []string) {
	for _, f := range c.forbid {
		if f != c.dsn && (f != canary() || !canaryFragment[c.name]) {
			out = append(out, f)
		}
	}
	return out
}

// TestPgvectorWrongPasswordIsPermissionDenied also feeds refusals whose
// text echoes the password as the DSN spells it: that cause is withheld.
func TestPgvectorWrongPasswordIsPermissionDenied(t *testing.T) {
	isolatePGEnv(t)
	c := canary()
	part, bsl := strings.ReplaceAll(c, "c", "%63"), c[:2]+`\ `+c[2:]
	for _, tc := range []struct{ dsn, echo string }{
		{"postgres://u:" + c + "@localhost/db?sslmode=disable", "u:" + c},
		{"postgres://u:" + part + "@localhost/db?sslmode=disable", "u:" + part},
		{"host=localhost user=u password=" + bsl + " sslmode=disable", "password=" + bsl},
	} {
		for _, code := range []string{"28P01", "42501"} {
			echo := &pgconn.PgError{Severity: "FATAL", Code: code, Message: "password authentication failed: " + tc.echo}
			for _, err := range []error{&pgconn.PgError{Code: code}, fmt.Errorf("dial: %w", &pgconn.PgError{Code: code}), echo, fmt.Errorf("dial: %w", echo)} {
				got := wrapConnError(err, tc.dsn, "pgvector: connect")
				if !cascade.HasKind(got, cascade.KindPermissionDenied) {
					t.Errorf("wrapConnError(PgError %s) kind = %s, want KindPermissionDenied", code, kindOf(got))
				}
				assertNoLeak(t, "pgerror-"+code, got, []string{tc.echo, tc.dsn})
			}
		}
	}
}

func TestPgvectorMalformedDSNIsInvalidInput(t *testing.T) {
	isolatePGEnv(t)
	dsn := "postgres://u:" + canary() + "@[::1"
	_, err := Open(context.Background(), dsn)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(malformed DSN) = %s, want KindInvalidInput", kindOf(err))
	}
	assertNoLeak(t, "open-malformed", err, []string{canary(), dsn, "u:" + canary()})
	if got := redactDSN(dsn); got != maskedDSN {
		t.Errorf("redactDSN(unparseable) is not the fixed mask (%d bytes)", len(got))
	}
}

func TestPgvectorOpenNeverEchoesCredential(t *testing.T) {
	isolatePGEnv(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	echoes, checked := 0, 0
	for _, c := range advCases(canary()) {
		ctx := canceled // a parseable DSN must not dial: Ping returns at once
		if _, perr := pgx.ParseConfig(c.dsn); perr != nil {
			ctx = context.Background() // parsing fails first, so no dial happens
		}
		_, err := Open(ctx, c.dsn)
		assertNoLeak(t, c.name+"/open", err, c.forbid)
		errs := []error{errors.New("dial " + c.dsn)}
		for _, f := range c.echoes() {
			errs = append(errs, fmt.Errorf("connect: %w", errors.New("echo "+f)))
		}
		if _, perr := pgconn.ParseConfig(c.dsn); perr != nil {
			echoes += countHits(perr.Error(), c.forbid)
			errs = append(errs, perr)
		} else {
			errs = append(errs, lookupConnectError(t, c.dsn, ""), lookupConnectError(t, c.dsn, strings.Join(c.echoes(), " ")))
		}
		for i, e := range errs {
			assertNoLeak(t, fmt.Sprintf("%s/wrap#%d", c.name, i), wrapConnError(e, c.dsn, "pgvector: connect"), c.forbid)
			checked++
		}
		r := redactDSN(c.dsn) // a non-URL DSN never renders: url.Parse would cut it at '?' or '#'
		if countHits(r, c.forbid) != 0 || (!strings.HasPrefix(c.dsn, "postgres://") && r != maskedDSN) {
			t.Errorf("%s: redactDSN output holds a forbidden value or renders a non-URL DSN", c.name)
		}
	}
	t.Logf("checked %d wrapped errors; raw pgx parse errors echoing a forbidden value: %d", checked, echoes)
}

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

// kindOf names err's Kind, so a failure never prints canary-bearing text.
func kindOf(err error) string {
	if k, ok := cascade.KindOf(err); ok {
		return k.String()
	}
	return fmt.Sprintf("%T (no kind)", err)
}

// countHits counts the forbidden values text holds.
func countHits(text string, forbidden []string) (n int) {
	for _, f := range forbidden {
		if f != "" && strings.Contains(text, f) {
			n++
		}
	}
	return n
}

func TestPgvectorConnErrorKeepsContextSentinelOnly(t *testing.T) {
	isolatePGEnv(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(canceled, "postgres://u:"+canary()+"@localhost/db?sslmode=disable")
	if !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Open(canceled ctx) = %s, want KindUnavailable wrapping context.Canceled", kindOf(err))
	}
	dsn := "host=localhost user=u password=" + canary() + " sslmode=disable"
	secrets, _ := dsnSecrets(dsn)
	detached := wrapDBError(lookupConnectError(t, dsn, canary()), secrets, "pgvector: count %s", "ns")
	if !cascade.HasKind(detached, cascade.KindUnavailable) {
		t.Errorf("wrapDBError(reconnect failure) = %s, want KindUnavailable", kindOf(detached))
	}
	assertNoLeak(t, "wrapdb-reconnect", detached, []string{canary(), dsn})
	var pe *pgconn.ParseConfigError
	_, perr := pgconn.ParseConfig(dsn + " port=bad")
	if !errors.As(perr, &pe) {
		t.Fatalf("ParseConfig(bad port) = %T, want a *pgconn.ParseConfigError", perr)
	}
	assertNoLeak(t, "wrapdb-parse", wrapDBError(perr, secrets, "pgvector: count"), []string{canary(), dsn})
	plain := &pgconn.PgError{Code: "23505"}
	if got := wrapDBError(plain, secrets, "pgvector: upsert"); !errors.Is(got, plain) || !cascade.HasKind(got, cascade.KindConflict) {
		t.Errorf("wrapDBError(query PgError) = %v, want the PgError kept and KindConflict", got)
	}
	pw1, pw2 := canary()+"1", canary()+"2"
	dsn = "postgres://u:" + pw1 + "@localhost/db?sslmode=disable&password=" + pw2
	secrets, _ = dsnSecrets(dsn)
	echo := lookupConnectError(t, dsn, "u:"+pw1)
	assertNoLeak(t, "detach-userinfo-vs-query", wrapDBError(echo, secrets, "pgvector: count"), []string{pw1, pw2})
	assertNoLeak(t, "detach-unknown-secrets", wrapDBError(echo, nil, "pgvector: count"), []string{pw1, pw2})
	clean := lookupConnectError(t, dsn, "")
	if detachConnError(clean, nil).Error() != withheld || detachConnError(clean, secrets).Error() == withheld {
		t.Error("detachConnError: want withheld with no secret set and the clean text with one")
	}
	t.Setenv("PGSSLPASSWORD", pw1)
	assertNoLeak(t, "sslpassword-env", wrapConnError(errors.New("tls key: "+pw1), "host=localhost user=u sslmode=disable", "pgvector: connect"), []string{pw1})
}
