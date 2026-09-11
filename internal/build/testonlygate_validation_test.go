package build

// Purpose: the refusal and falsifiability branches of the test-only gate
// that the real tree can never reach: the checked-in allow list is
// well-formed and its caller_site promises are kept, so each rejection
// path (a malformed entry, a broken promise, an unparseable fixture file)
// needs a deliberately wrong fixture. A positive control sits next to
// every table so a rejection is attributable to the mutated field alone.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// validationModulePath is the fixture module path every caller-site test
// imports under, so cross-package qualified selectors resolve.
const validationModulePath = "example.com/fixture"

// validEntry is the well-formed entry every mutation below starts from.
func validEntry(symbol string) TestOnlyAllowEntry {
	return TestOnlyAllowEntry{
		Symbol:       symbol,
		Reason:       "the wiring ticket has not landed yet",
		Caller:       "the composition root",
		RetireTicket: "P1-E17-W4-S36-T2",
		CallerSite:   "internal/alpha/caller.go",
	}
}

// TestValidateTicketAndCallerSiteTable drives every rejection of the
// falsifiable half of an entry, plus the three accepted ticket shapes.
func TestValidateTicketAndCallerSiteTable(t *testing.T) {
	cases := []struct {
		name    string
		ticket  string
		site    string
		wantErr bool
	}{
		{"full-form ticket is accepted", "P1-E17-W4-S36-T2", "internal/alpha/caller.go", false},
		{"short-form ticket is accepted", "E-08.T4", "internal/alpha/caller.go", false},
		{"unowned literal is accepted", UnownedTicket, "internal/alpha/caller.go", false},
		{"missing retire_ticket", "", "internal/alpha/caller.go", true},
		{"free-text retire_ticket", "someday", "internal/alpha/caller.go", true},
		{"missing caller_site", "P1-E17-W4-S36-T2", "", true},
		{"test-file caller_site", "P1-E17-W4-S36-T2", "internal/alpha/caller_test.go", true},
		{"non-go caller_site", "P1-E17-W4-S36-T2", "internal/alpha/notes.txt", true},
		{"absolute caller_site", "P1-E17-W4-S36-T2", "/elsewhere/caller.go", true},
		{"dot-dot caller_site", "P1-E17-W4-S36-T2", "internal/../caller.go", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validEntry("internal/alpha.Hook")
			e.RetireTicket, e.CallerSite = c.ticket, c.site
			err := validateTicketAndCallerSite(e)
			if c.wantErr && err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected acceptance, got %v", err)
			}
		})
	}
}

// TestLoadTestOnlyAllowListRefusalPaths covers the loader's own failures:
// an unreadable path, unparseable JSON, a symbol-less entry, and a
// validation refusal surfaced through the loader, with the well-formed
// file as the positive control.
func TestLoadTestOnlyAllowListRefusalPaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadTestOnlyAllowList(dir); err == nil {
		t.Error("reading a directory must be an error, not an empty list")
	}

	path := filepath.Join(dir, "allow.json")
	writeFileT(t, path, `{"symbol":"not-an-array"}`)
	if _, err := LoadTestOnlyAllowList(path); err == nil {
		t.Error("malformed JSON must be rejected")
	}

	writeFileT(t, path, `[{"symbol":"","reason":"r","expected_caller":"c","retire_ticket":"P1-E17-W4-S36-T2","caller_site":"internal/alpha/caller.go"}]`)
	if _, err := LoadTestOnlyAllowList(path); err == nil || !strings.Contains(err.Error(), "no symbol") {
		t.Errorf("an entry without a symbol must be rejected, got %v", err)
	}

	writeFileT(t, path, `[{"symbol":"internal/alpha.Hook","reason":"r","expected_caller":"c","retire_ticket":"","caller_site":"internal/alpha/caller.go"}]`)
	if _, err := LoadTestOnlyAllowList(path); err == nil || !strings.Contains(err.Error(), "retire_ticket") {
		t.Errorf("the loader must surface the ticket validation refusal, got %v", err)
	}

	writeFileT(t, path, `[{"symbol":"internal/alpha.Hook","reason":"r","expected_caller":"c","retire_ticket":"P1-E17-W4-S36-T2","caller_site":"internal/alpha/caller.go"}]`)
	got, err := LoadTestOnlyAllowList(path)
	if err != nil || len(got) != 1 {
		t.Fatalf("the well-formed control must load, got %v, %d entries", err, len(got))
	}
}

// callerSiteFixture builds a throwaway module shaped for the
// CheckCallerSitesWired tables: a package declaring three hooks, a
// same-package caller, a cross-package caller, a caller that references
// nothing, an unparseable caller file, and a package whose own
// declaration file is unparseable.
func callerSiteFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "internal/alpha/alpha.go"),
		"package alpha\n\nfunc AlphaHook() int { return 1 }\n\nfunc BetaHook() int { return 2 }\n\n"+
			"func GammaHook() int { return 3 }\n")
	writeFileT(t, filepath.Join(root, "internal/alpha/self.go"),
		"package alpha\n\nfunc samePackageUse() int { return AlphaHook() }\n")
	writeFileT(t, filepath.Join(root, "internal/beta/caller.go"),
		"package beta\n\nimport \"example.com/fixture/internal/alpha\"\n\n"+
			"func crossPackageUse() int { return alpha.BetaHook() }\n")
	writeFileT(t, filepath.Join(root, "internal/delta/caller.go"),
		"package delta\n\nfunc unrelated() int { return 7 }\n")
	writeFileT(t, filepath.Join(root, "internal/eps/broken.go"),
		"this is not Go source {{{\n")
	writeFileT(t, filepath.Join(root, "internal/zeta/decl.go"),
		"package zeta; this is not Go source {{{\n")
	writeFileT(t, filepath.Join(root, "internal/zeta/caller.go"),
		"package zeta\n\nfunc samePackageUse() int { return ZetaHook() }\n")
	return root
}

// TestCheckCallerSitesWiredPromises drives every outcome of the
// falsifiability check over one fixture module: kept promises through a
// same-package bare identifier and a cross-package qualified selector,
// future work (the file does not exist yet), a directory rather than a
// file, a broken promise, and a symbol key that resolves to no package.
func TestCheckCallerSitesWiredPromises(t *testing.T) {
	root := callerSiteFixture(t)
	allow := map[string]TestOnlyAllowEntry{
		"internal/alpha.AlphaHook": {Symbol: "internal/alpha.AlphaHook", CallerSite: "internal/alpha/self.go"},
		"internal/alpha.BetaHook":  {Symbol: "internal/alpha.BetaHook", CallerSite: "internal/beta/caller.go"},
		"internal/alpha.GammaHook": {Symbol: "internal/alpha.GammaHook", CallerSite: "internal/omega/future.go"},
		"internal/omega.OmegaHook": {Symbol: "internal/omega.OmegaHook", CallerSite: "internal/beta"},
		"internal/delta.DeltaHook": {Symbol: "internal/delta.DeltaHook", CallerSite: "internal/delta/caller.go"},
		"DotlessSymbol":            {Symbol: "DotlessSymbol", CallerSite: "internal/delta/caller.go"},
	}
	broken, err := CheckCallerSitesWired(root, validationModulePath, allow)
	if err != nil {
		t.Fatalf("CheckCallerSitesWired: %v", err)
	}
	if len(broken) != 2 {
		t.Fatalf("expected exactly the broken promise and the dotless key, got %+v", broken)
	}
	// Results are sorted by symbol, so this also pins the order.
	if broken[0].Symbol != "DotlessSymbol" || broken[1].Symbol != "internal/delta.DeltaHook" {
		t.Fatalf("broken promises = %+v", broken)
	}
	if broken[1].CallerSite != "internal/delta/caller.go" {
		t.Fatalf("the report must carry the caller_site: %+v", broken[1])
	}
}

// TestCheckCallerSitesWiredRefusesUnparseable proves the check fails
// closed: a caller file it cannot parse, and a caller whose symbol's own
// package holds an unparseable declaration file, are errors rather than
// silent passes.
func TestCheckCallerSitesWiredRefusesUnparseable(t *testing.T) {
	root := callerSiteFixture(t)
	cases := []struct {
		name   string
		symbol string
		site   string
	}{
		{"unparseable caller file", "internal/alpha.AlphaHook", "internal/eps/broken.go"},
		{"unparseable symbol package", "internal/zeta.ZetaHook", "internal/zeta/caller.go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allow := map[string]TestOnlyAllowEntry{c.symbol: {Symbol: c.symbol, CallerSite: c.site}}
			broken, err := CheckCallerSitesWired(root, validationModulePath, allow)
			if err == nil {
				t.Fatalf("expected a refusal, got %v", broken)
			}
		})
	}
}

// TestFindTestOnlySymbolsAndFilter drives the pure halves directly: a
// symbol used by shipping code is skipped, a symbol referenced by nothing
// at all is the dead-code gate's finding rather than this one, only
// test-only symbols are reported, and the filter separates a genuine
// violation from a matched exemption while reporting an exemption that
// matches nothing.
func TestFindTestOnlySymbolsAndFilter(t *testing.T) {
	declared := map[DeadCodeSymbol]DeadCodeDecl{
		{Dir: "internal/x", Name: "Wired"}:     {},
		{Dir: "internal/x", Name: "AlsoOnly"}:  {},
		{Dir: "internal/x", Name: "OnlyTests"}: {},
		{Dir: "internal/x", Name: "Dead"}:      {},
	}
	byShipping := map[DeadCodeSymbol]bool{{Dir: "internal/x", Name: "Wired"}: true}
	byTests := map[DeadCodeSymbol]bool{
		{Dir: "internal/x", Name: "AlsoOnly"}:  true,
		{Dir: "internal/x", Name: "OnlyTests"}: true,
	}
	found := FindTestOnlySymbols(declared, byShipping, byTests)
	if len(found) != 2 || found[0].Name != "AlsoOnly" || found[1].Name != "OnlyTests" {
		t.Fatalf("expected only the two test-only symbols, sorted, got %+v", found)
	}

	violations, stale := FilterTestOnlyAllowed(found, map[string]TestOnlyAllowEntry{
		"internal/x.OnlyTests": {CallerSite: "internal/x/caller.go"},
	})
	if len(stale) != 0 {
		t.Fatalf("a matched exemption is not stale: %+v", stale)
	}
	if len(violations) != 1 || violations[0].Name != "AlsoOnly" {
		t.Fatalf("expected the unexempted symbol as a violation, got %+v", violations)
	}

	_, stale = FilterTestOnlyAllowed(nil, map[string]TestOnlyAllowEntry{
		"internal/x.Gone": {CallerSite: "internal/x/gone.go"},
	})
	if len(stale) != 1 || stale[0] != "internal/x.Gone" {
		t.Fatalf("expected the unmatched exemption reported stale, got %+v", stale)
	}
}

// TestScanTestOnlySymbolsRefusesUnparseableFile: a tree containing a .go
// file the scanner cannot parse refuses, rather than counting the file as
// declaring and referencing nothing.
func TestScanTestOnlySymbolsRefusesUnparseableFile(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "inner/fine.go"), "package inner\n\nfunc Fine() int { return 1 }\n")
	writeFileT(t, filepath.Join(root, "inner/broken.go"), "not Go source {{{\n")
	if _, err := ScanTestOnlySymbols(root, validationModulePath, []string{"inner"}); err == nil {
		t.Fatal("an unparseable file must surface the parse error, not pass silently")
	}
}

// TestScanTestOnlySymbolsRefusesUnreadableRoot: a root the walker cannot
// read is an error, not an empty scan. Skipped where directory mode bits
// do not deny reads (root) or deny them differently (windows).
func TestScanTestOnlySymbolsRefusesUnreadableRoot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory mode bits do not deny reads here, so this branch cannot fire")
	}
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "locked/inside.go"), "package locked\n\nfunc Inside() int { return 1 }\n")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := ScanTestOnlySymbols(root, validationModulePath, []string{"locked"}); err == nil {
		t.Fatal("an unreadable root must surface the walk error, not pass silently")
	}
}
