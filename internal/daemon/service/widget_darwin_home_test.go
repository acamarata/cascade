//go:build darwin

// Purpose: install and status stay consistent for every CASCADE_HOME the
//
//	installer records, clean or not: a successful install always validates,
//	and a key-as-value edit (which a reader and launchd disagree on) never does.
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"strings"
	"testing"
)

func TestWidgetInstallThenStatusValidForAnyRecordedHome(t *testing.T) {
	for name, home := range map[string]string{
		"unset": "", "clean": "/Users/example/ch", "relative": "rel/home", "trailing slash": "/Users/example/ch/",
	} {
		t.Run(name, func(t *testing.T) {
			f := newWidgetFixture(t)
			f.cfg.Getenv = func(k string) string {
				if k == "CASCADE_HOME" {
					return home
				}
				return ""
			}
			mustInstall(t, f)
			rep, err := NewWidgetInstaller().Status(f.cfg)
			if err != nil || !rep.Managed || !rep.ProgramValid || rep.BundlePath != f.bundle {
				t.Fatalf("status = %+v, %v; want valid", rep, err)
			}
		})
	}
}

func TestWidgetStatusKeyAsValueProgramIsNotValid(t *testing.T) {
	f := newWidgetFixture(t)
	mustInstall(t, f)
	data, _ := readWidgetPlist(t, f)
	evil := strings.Replace(string(data), "<dict>\n\t<key>Env", "<dict>\n\t<key>Program</key><key>/tmp/evil</key>\n\t<key>Env", 1)
	if evil == string(data) {
		t.Fatal("edit did not change the plist")
	}
	if err := os.WriteFile(widgetPlistPath(f.home), []byte(evil), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil || !rep.Managed || rep.ProgramValid || rep.BundlePath != "" {
		t.Fatalf("status = %+v, %v; want managed but not valid", rep, err)
	}
}
