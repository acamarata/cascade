package build

// Purpose: CheckCallerSitesWired against a real fixture module, so each
// promise shape has its own named assertion instead of only the real
// tree's aggregate: a site that does not exist, a site that is a
// directory, a site that references the symbol (bare and qualified
// flavors), a site that does not, an unparseable caller file, and an
// unparseable symbol package.

import (
	"path/filepath"
	"testing"
)

// callersiteFixtureModulePath is the fixture module's import path; only
// the module-relative prefix matters to the import-alias scan.
const callersiteFixtureModulePath = "fixture.example/mod"

// writeCallersiteFixtureModule builds the module every test in this file
// reads: a widget package exporting four symbols, a same-package caller, a
// cross-package caller, a silent caller, an unparseable caller file, and
// a symbol package whose sibling file does not parse.
func writeCallersiteFixtureModule(t *testing.T, root string) {
	t.Helper()
	writeFileT(t, filepath.Join(root, "go.mod"), "module "+callersiteFixtureModulePath+"\n")
	writeFileT(t, filepath.Join(root, "internal", "widget", "widget.go"), `package widget

func Greet() string { return "hello" }

func Salute() string { return "greetings" }

func Farewell() string { return "goodbye" }

func Later() {}
`)
	writeFileT(t, filepath.Join(root, "internal", "widget", "runner.go"), `package widget

// Announce references Greet as a same-package bare identifier.
func Announce() string { return Greet() }
`)
	writeFileT(t, filepath.Join(root, "internal", "app", "app.go"), `package app

import "`+callersiteFixtureModulePath+`/internal/widget"

// Run references Salute through a cross-package qualified selector.
func Run() string { return widget.Salute() }
`)
	writeFileT(t, filepath.Join(root, "internal", "app", "silent.go"), `package app

// Idle references nothing from the widget package.
func Idle() {}
`)
	writeFileT(t, filepath.Join(root, "internal", "broken", "caller.go"),
		"package broken\n\nfunc ( this file does not parse\n")
	writeFileT(t, filepath.Join(root, "internal", "other", "thing.go"), `package other

// Thing is the symbol whose sibling file does not parse.
func Thing() {}
`)
	writeFileT(t, filepath.Join(root, "internal", "other", "unparseable.go"),
		"package other\n\nfunc ) this file does not parse either\n")
}

// callersiteEntry builds one allow-list entry whose only relevant fields
// are the symbol key and the promised caller_site.
func callersiteEntry(symbol, site string) TestOnlyAllowEntry {
	return TestOnlyAllowEntry{
		Symbol: symbol, Reason: "fixture", Caller: "fixture",
		RetireTicket: "E-08.T4", CallerSite: site,
	}
}

// TestCheckCallerSitesWiredClassifiesEachPromiseShape is the acceptance
// table: only an existing site that does not reference its symbol is a
// broken promise. A missing site and a directory site are future work, and
// both reference flavors count as wired.
func TestCheckCallerSitesWiredClassifiesEachPromiseShape(t *testing.T) {
	root := t.TempDir()
	writeCallersiteFixtureModule(t, root)
	allow := map[string]TestOnlyAllowEntry{
		// wired: cross-package qualified selector in app.go
		"internal/widget.Salute": callersiteEntry("internal/widget.Salute", "internal/app/app.go"),
		// wired: same-package bare identifier in runner.go
		"internal/widget.Greet": callersiteEntry("internal/widget.Greet", "internal/widget/runner.go"),
		// broken: silent.go exists and references no widget symbol
		"internal/widget.Farewell": callersiteEntry("internal/widget.Farewell", "internal/app/silent.go"),
		// future work: the promised file does not exist yet
		"internal/other.Thing": callersiteEntry("internal/other.Thing", "internal/future/worker.go"),
		// future work: the caller_site is a directory, not a file
		"internal/widget.Later": callersiteEntry("internal/widget.Later", "internal/app"),
	}

	broken, err := CheckCallerSitesWired(root, callersiteFixtureModulePath, allow)
	if err != nil {
		t.Fatalf("CheckCallerSitesWired: %v", err)
	}
	want := []BrokenCallerSite{{Symbol: "internal/widget.Farewell", CallerSite: "internal/app/silent.go"}}
	if len(broken) != len(want) || broken[0] != want[0] {
		t.Fatalf("broken = %+v, want exactly %+v", broken, want)
	}
}

// TestCallerSiteReferenceFlavors pins the two reference shapes the
// collector recognizes, and that an existing file without the reference
// reports unwired rather than an error.
func TestCallerSiteReferenceFlavors(t *testing.T) {
	root := t.TempDir()
	writeCallersiteFixtureModule(t, root)
	cases := []struct {
		name   string
		site   string
		symbol string
		want   bool
	}{
		{"cross-package qualified selector", "internal/app/app.go", "internal/widget.Salute", true},
		{"same-package bare identifier", "internal/widget/runner.go", "internal/widget.Greet", true},
		{"existing file, no reference", "internal/app/silent.go", "internal/widget.Salute", false},
		{"symbol without a separator", "internal/app/app.go", "NoSeparatorHere", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := callerSiteReferencesSymbol(root, callersiteFixtureModulePath,
				filepath.Join(root, filepath.FromSlash(c.site)), c.symbol)
			if err != nil {
				t.Fatalf("callerSiteReferencesSymbol: %v", err)
			}
			if got != c.want {
				t.Fatalf("callerSiteReferencesSymbol(%s, %s) = %v, want %v", c.site, c.symbol, got, c.want)
			}
		})
	}
}

// TestCheckCallerSitesWiredUnparseableCallerFailsClosed: a caller_site
// that exists but is not valid Go refuses the whole check rather than
// being read as "no reference found".
func TestCheckCallerSitesWiredUnparseableCallerFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeCallersiteFixtureModule(t, root)
	allow := map[string]TestOnlyAllowEntry{
		"internal/widget.Greet": callersiteEntry("internal/widget.Greet", "internal/broken/caller.go"),
	}
	if _, err := CheckCallerSitesWired(root, callersiteFixtureModulePath, allow); err == nil {
		t.Fatal("an unparseable caller_site must fail the check, not count as unwired")
	}
}

// TestCheckCallerSitesWiredUnparseableSymbolPackageFailsClosed: when the
// symbol's own declaring package cannot be parsed, the same-package
// reference check cannot run and must refuse.
func TestCheckCallerSitesWiredUnparseableSymbolPackageFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeCallersiteFixtureModule(t, root)
	allow := map[string]TestOnlyAllowEntry{
		"internal/other.Thing": callersiteEntry("internal/other.Thing", "internal/app/silent.go"),
	}
	if _, err := CheckCallerSitesWired(root, callersiteFixtureModulePath, allow); err == nil {
		t.Fatal("an unparseable symbol package must fail the check, not count as unwired")
	}
}

// TestSplitSymbolKey covers the separator rule directly: the last dot
// separates the package dir from the name, and a missing or trailing
// separator is not a symbol key.
func TestSplitSymbolKey(t *testing.T) {
	dir, name, ok := splitSymbolKey("internal/widget.Greet")
	if !ok || dir != "internal/widget" || name != "Greet" {
		t.Fatalf("splitSymbolKey(internal/widget.Greet) = %q, %q, %v", dir, name, ok)
	}
	for _, bad := range []string{"NoSeparator", "internal/widget.", ""} {
		if _, _, ok := splitSymbolKey(bad); ok {
			t.Fatalf("splitSymbolKey(%q) accepted a malformed key", bad)
		}
	}
}
