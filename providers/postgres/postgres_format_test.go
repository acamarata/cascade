//go:build postgres

// Purpose: unit proof that formatting a Driver never prints its DSN. Every
// fmt verb on a Driver value, a *Driver, the label and structures holding
// them shows no credential and no raw DSN. Nothing is dialed: sql.Open
// only parses.
package postgres

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// formatHolder embeds a Driver the way a caller's struct might, with one
// unexported field that %v and %#v still print.
type formatHolder struct {
	Exported Driver
	inner    *Driver
}

func TestDriverFormattingNeverPrintsDSN(t *testing.T) {
	isolatePGEnv(t)
	c := canary()
	for _, dsn := range []string{
		"postgres://u:" + c + "@127.0.0.1:1/db?sslmode=disable",
		"host=127.0.0.1 port=1 user=u password=" + c + " sslmode=disable",
	} {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal("sql.Open failed on a parseable fixture")
		}
		t.Cleanup(func() { _ = db.Close() })
		d := newDriver(db, dsn)
		forbid := []string{c, dsn, hex.EncodeToString([]byte(c)), strings.ToUpper(hex.EncodeToString([]byte(c)))}
		subjects := map[string]any{
			"ptr": d, "value": *d, "label-ptr": d.label, "label-value": *d.label,
			"holder": formatHolder{Exported: *d, inner: d}, "holder-ptr": &formatHolder{Exported: *d, inner: d},
			"slice": []Driver{*d}, "map": map[string]*Driver{"k": d}, "any-slice": []any{d, *d},
		}
		for name, v := range subjects {
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%T", "%d"} {
				if got := fmt.Sprintf(verb, v); countHits(got, forbid) != 0 {
					t.Errorf("%s with %s printed the DSN or its password", name, verb)
				}
			}
			if got := fmt.Sprint(v) + fmt.Sprintln(v); countHits(got, forbid) != 0 {
				t.Errorf("%s with Sprint/Sprintln printed the DSN or its password", name)
			}
		}
		if s := d.label.String() + d.label.GoString(); s != redacted+redacted {
			t.Errorf("the label must format as %s for String and GoString", redacted)
		}
	}
}

func TestDriverStringShowsOnlyTheRedactedRendering(t *testing.T) {
	isolatePGEnv(t)
	d := newDriver(nil, "postgres://u:"+canary()+"@127.0.0.1:1/db?sslmode=disable&password="+canary())
	if got, want := d.String(), "postgres.Driver(postgres://u:xxxxx@127.0.0.1:1/db)"; got != want {
		if strings.Contains(got, canary()) {
			t.Fatal("Driver.String printed the password")
		}
		t.Fatalf("Driver.String = %q, want %q", got, want)
	}
	if got := newDriver(nil, "host=h password="+canary()).String(); got != "postgres.Driver(<redacted-dsn>)" {
		t.Fatal("a key=value DSN must render as the placeholder")
	}
}

// TestZeroDriverFormatsWithoutPanic proves a Driver not built by newDriver
// (no label) formats under every verb as the placeholder and never panics.
func TestZeroDriverFormatsWithoutPanic(t *testing.T) {
	const want = "postgres.Driver(<redacted-dsn>)"
	if got := (&Driver{}).String(); got != want {
		t.Fatalf("(&Driver{}).String() = %q, want %q", got, want)
	}
	var nilDriver *Driver
	if got := nilDriver.String(); got != want {
		t.Fatalf("(*Driver)(nil).String() = %q, want %q", got, want)
	}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(verb, &Driver{}); strings.Contains(got, "PANIC") {
			t.Errorf("%s on &Driver{} panicked: %s", verb, got)
		}
		if got := fmt.Sprintf(verb, Driver{}); strings.Contains(got, "PANIC") {
			t.Errorf("%s on Driver{} panicked: %s", verb, got)
		}
	}
}
