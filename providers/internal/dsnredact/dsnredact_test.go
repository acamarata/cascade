// Purpose: unit proof that Redact and RedactURL render every adversarial
// DSN form without a credential, fail closed to the placeholder, and that
// Secrets lists what the redactor checks against. The table is the shared
// testdata/dsn-forms.json; canaries are assembled at run time and never
// printed. No database, no network.

package dsnredact

import (
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
	Name     string   `json:"name"`
	DSN      string   `json:"dsn"`
	Forbid   []string `json:"forbid"`
	Fragment bool     `json:"fragment"`
	Render   string   `json:"render"`
}

// loadForms reads the shared table at path and expands its tokens around
// the canary c. Every row's forbidden list ends with its own DSN.
func loadForms(t *testing.T, path, c string) []dsnForm {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Forms []dsnForm `json:"forms"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Forms) < 38 {
		t.Fatalf("decode %s: err=%v rows=%d, want at least 38", path, err, len(doc.Forms))
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
		f.DSN, f.Render = r.Replace(f.DSN), r.Replace(f.Render)
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
		texts = append(texts, e.Error(), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e))
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
	texts := chainTexts(err)
	for i, f := range forbidden {
		if f == "" {
			t.Fatalf("%s: forbidden value #%d is empty", name, i)
		}
		if countHits(strings.Join(texts, "\n"), []string{f}) != 0 {
			t.Errorf("%s: error text holds forbidden value #%d", name, i)
		}
	}
	var ce *pgconn.ConnectError
	var pe *pgconn.ParseConfigError
	if errors.As(err, &ce) || errors.As(err, &pe) {
		t.Errorf("%s: the raw pgx connection error is reachable through errors.As", name)
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

// kindOf names err's Kind, so a failure never prints canary-bearing text.
func kindOf(err error) string {
	if k, ok := cascade.KindOf(err); ok {
		return k.String()
	}
	return fmt.Sprintf("%T (no kind)", err)
}

func TestRedactCoversEveryDSNForm(t *testing.T) {
	isolatePGEnv(t)
	for _, f := range loadForms(t, "testdata/dsn-forms.json", canary()) {
		got := Redact(f.DSN)
		if countHits(got, f.Forbid) != 0 {
			t.Errorf("%s: Redact output holds a forbidden value", f.Name)
			continue
		}
		if got != f.Render { // safe to print: got holds no forbidden value
			t.Errorf("%s: Redact = %q, want %q", f.Name, got, f.Render)
		}
		if _, err := pgconn.ParseConfig(f.DSN); err != nil && got != placeholder {
			t.Errorf("%s: a DSN pgx cannot parse must give the placeholder", f.Name)
		}
		if u := RedactURL(f.DSN, "postgres", "postgresql"); countHits(u, f.Forbid) != 0 {
			t.Errorf("%s: RedactURL output holds a forbidden value", f.Name)
		}
	}
}

func TestRedactURLMasksUserinfoAndQueryPasswords(t *testing.T) {
	c, hostPw := canary(), "cvh"+"0st"
	pw1, pw2 := c+"1", c+"2"
	schemes := []string{"redis", "rediss", "unix"}
	for _, tc := range []struct{ name, raw, want, seg string }{
		{"userinfo", "redis://u:" + c + "@localhost:6379/0", "redis://u:xxxxx@localhost:6379/0", ""},
		{"query-password", "redis://localhost:6379/0?password=" + c, "redis://localhost:6379/0", ""},
		{"both-any-case", "rediss://u:" + pw1 + "@h:6380/1?db=1&PassWord=" + pw2, "rediss://u:xxxxx@h:6380/1", ""},
		{"unix-socket", "unix://u:" + c + "@/tmp/redis.sock?db=0", "unix://u:xxxxx@/tmp/redis.sock", ""},
		{"upper-scheme", "REDIS://u:" + c + "@h:6379/0", "redis://u:xxxxx@h:6379/0", ""},
		{"scheme-not-listed", "http://u:" + c + "@h/", placeholder, ""},
		{"malformed", "redis://u:" + c + "@[::1", placeholder, ""},
		{"password-is-host", "redis://u:" + hostPw + "@" + hostPw + ":6379/0", placeholder, ""},
		{"garbage", "not a url " + c, placeholder, ""},
		{"query-mark-password-is-port", "redis://:46379?" + c + "@h:6379/0", placeholder, "46379"},
		{"hash-password-is-port", "redis://u:42412#" + c + "@h/0", placeholder, "42412"},
		{"slash-password-is-port", "redis://u:42413/" + c + "@h/0", placeholder, "42413"},
		{"single-slash", "redis:/u:" + c + "@h", placeholder, "u:"},
		{"single-slash-db", "redis:/u:" + c + "@h/0", placeholder, "u:"},
		{"single-slash-query-mark", "redis:/u:42414?" + c + "@h", placeholder, "42414"},
		{"opaque", "redis:u:" + c + "@h", placeholder, "u:"},
	} {
		got := RedactURL(tc.raw, schemes...)
		forbid := []string{c, hostPw, tc.raw}
		if tc.seg != "" {
			forbid = append(forbid, tc.seg)
		}
		if countHits(got, forbid) != 0 {
			t.Errorf("%s: RedactURL output holds a forbidden value", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: RedactURL = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := RedactURL("redis://h:6379/0"); got != placeholder {
		t.Errorf("RedactURL with no schemes = %q, want the placeholder", got)
	}
}

func TestSecretsFailClosedAndCoverEnv(t *testing.T) {
	isolatePGEnv(t)
	if s, ok := Secrets("postgres://u:" + canary() + "@[::1"); ok || s != nil {
		t.Fatal("Secrets(unparseable) must report unknown (nil, false)")
	}
	pw := canary() + "1"
	t.Setenv("PGSSLPASSWORD", pw)
	s, ok := Secrets("host=localhost user=u sslmode=disable")
	if !ok || countHits(strings.Join(s, "\n"), []string{pw}) == 0 {
		t.Fatal("Secrets must list PGSSLPASSWORD")
	}
	t.Setenv("PGPASSWORD", pw+"x")
	if s, _ := Secrets("host=localhost user=u sslmode=disable"); countHits(strings.Join(s, "\n"), []string{pw + "x"}) == 0 {
		t.Fatal("Secrets must list the password pgx would send from PGPASSWORD")
	}
}
