//go:build postgres

// Purpose: unit proof that providers/postgres never echoes a DSN
// credential: every row of the shared adversarial table
// (providers/internal/dsnredact/testdata/dsn-forms.json) runs through Open
// (nothing is dialed), wrapConnError with real and synthetic pgx errors,
// wrapDBError's reconnect detach and Driver.String. Canaries never print.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

// canary returns the acceptance password, assembled at run time.
func canary() string { return strings.Join([]string{"s3", "cr3t"}, "") }

// dsnForm is one row of the shared adversarial table, tokens expanded.
type dsnForm struct {
	Name   string   `json:"name"`
	DSN    string   `json:"dsn"`
	Forbid []string `json:"forbid"`
}

// loadForms reads the shared table and expands its tokens around c (the
// token set is documented in the table's _comment). Every row's
// forbidden list ends with its own DSN.
func loadForms(t *testing.T, c string) []dsnForm {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "internal", "dsnredact", "testdata", "dsn-forms.json"))
	var doc struct {
		Forms []dsnForm `json:"forms"`
	}
	if err != nil || json.Unmarshal(raw, &doc) != nil || len(doc.Forms) < 38 {
		t.Fatalf("shared DSN table: read err=%v rows=%d, want at least 38", err, len(doc.Forms))
	}
	pct := ""
	for i := range len(c) {
		pct += fmt.Sprintf("%%%02X", c[i])
	}
	r := strings.NewReplacer("{c}", c, "{c1}", c[1:], "{pct}", pct, "{part}", strings.ReplaceAll(c, "c", "%63"),
		"{bsl}", c[:2]+`\ `+c[2:], "{bslDec}", c[:2]+" "+c[2:], "{quo}", c[:2]+`\'`+c[2:], "{quoDec}", c[:2]+"'"+c[2:],
		"{pw1}", c+"1", "{pw2}", c+"2", "{hostPw}", "cvh"+"0st", "{dbPw}", "cvd"+"bname")
	for i := range doc.Forms {
		f := &doc.Forms[i]
		f.DSN = r.Replace(f.DSN)
		for j := range f.Forbid {
			f.Forbid[j] = r.Replace(f.Forbid[j])
		}
		f.Forbid = append(f.Forbid, f.DSN)
	}
	return doc.Forms
}

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

// chainTexts renders err with %v, %+v, %#v and %q, and the Error() and
// %+v/%#v of every error reachable through Unwrap (single and joined).
func chainTexts(err error) string {
	texts := []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%q", err)}
	for stack := []error{err}; len(stack) > 0; {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if e == nil {
			continue
		}
		texts = append(texts, e.Error(), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e))
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			stack = append(stack, u.Unwrap()...)
		case interface{ Unwrap() error }:
			stack = append(stack, u.Unwrap())
		}
	}
	return strings.Join(texts, "\n")
}

// assertNoLeak fails when err is nil, when any rendering of its chain
// holds a forbidden value, or when errors.As reaches a pgx type that
// exports the DSN or Config. It never prints a forbidden value.
func assertNoLeak(t *testing.T, name string, err error, forbidden []string) {
	t.Helper()
	if err == nil || len(forbidden) == 0 {
		t.Fatalf("%s: err=%v forbidden=%d, want an error and a non-empty forbidden list", name, err == nil, len(forbidden))
	}
	texts := chainTexts(err)
	for i, f := range forbidden {
		if f == "" || strings.Contains(texts, f) {
			t.Errorf("%s: forbidden value #%d is empty or held by the error text", name, i)
		}
	}
	var ce *pgconn.ConnectError
	var pe *pgconn.ParseConfigError
	if errors.As(err, &ce) || errors.As(err, &pe) {
		t.Errorf("%s: the raw pgx connection error is reachable through errors.As", name)
	}
}

// lookupConnectError returns the real *pgconn.ConnectError (Config inside)
// pgx builds when every lookup for dsn fails with echo. Nothing is dialed.
func lookupConnectError(t *testing.T, dsn, echo string) error {
	t.Helper()
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal("pgconn.ParseConfig failed on a parseable fixture")
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) {
		return nil, errors.New("no address for " + echo)
	}
	_, err = pgconn.ConnectConfig(context.Background(), cfg)
	var ce *pgconn.ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("stub connect = %T, want a *pgconn.ConnectError", err)
	}
	return err
}

func TestPostgresOpenNeverEchoesCredential(t *testing.T) {
	isolatePGEnv(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	checked := 0
	for _, f := range loadForms(t, canary()) {
		ctx := canceled // a parseable DSN must not dial: Ping returns at once
		_, perr := pgconn.ParseConfig(f.DSN)
		if perr != nil {
			ctx = context.Background() // parsing fails first, so no dial happens
		}
		_, err := Open(ctx, f.DSN)
		assertNoLeak(t, f.Name+"/open", err, f.Forbid)
		errs := []error{errors.New("dial " + f.DSN), fmt.Errorf("connect: %w", errors.New("echo "+strings.Join(f.Forbid, " ")))}
		if perr != nil {
			errs = append(errs, perr)
		} else {
			errs = append(errs, lookupConnectError(t, f.DSN, ""), lookupConnectError(t, f.DSN, strings.Join(f.Forbid, " ")))
			assertNoLeak(t, f.Name+"/reconnect", wrapDBError(errs[len(errs)-1], "postgres: get %s", "ns"), f.Forbid)
		}
		for i, e := range errs {
			assertNoLeak(t, fmt.Sprintf("%s/wrap#%d", f.Name, i), wrapConnError(e, f.DSN, "postgres: connect"), f.Forbid)
			checked++
		}
		if countHits(newDriver(nil, f.DSN).String(), f.Forbid) != 0 {
			t.Errorf("%s: Driver.String holds a forbidden value", f.Name)
		}
	}
	t.Logf("checked %d wrapped errors", checked)
}

func TestPostgresConnErrorKeepsKindAndContextOnly(t *testing.T) {
	isolatePGEnv(t)
	c := canary()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(canceled, "postgres://u:"+c+"@localhost/db?sslmode=disable")
	if !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatal("Open(canceled ctx) must be KindUnavailable wrapping context.Canceled")
	}
	denied := wrapConnError(&pgconn.PgError{Code: sqlstateInvalidPassword, Message: "auth failed for " + c}, "host=localhost password="+c, "postgres: connect")
	if !cascade.HasKind(denied, cascade.KindPermissionDenied) {
		t.Error("a 28P01 refusal must stay KindPermissionDenied")
	}
	assertNoLeak(t, "pgerror-28P01", denied, []string{c})
	plain := &pgconn.PgError{Code: sqlstateUniqueViolation}
	if got := wrapDBError(plain, "postgres: put"); !errors.Is(got, plain) || !cascade.HasKind(got, cascade.KindConflict) {
		t.Error("wrapDBError must keep a query-stage PgError and its kind")
	}
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
