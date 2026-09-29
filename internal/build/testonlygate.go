// Package build implements the repository's own build-time gates. This file
// holds the test-only-usage gate.
//
// The dead-code gate next door counts a symbol as used when anything in the
// tree references it, INCLUDING that symbol's own tests. That is the right
// rule for dead code and the wrong rule for the failure this repository kept
// hitting: a subsystem built, thoroughly tested, and called by nothing that
// ships. Such a symbol is not dead. Its tests reference it, so the dead-code
// gate sees a live symbol and says nothing, while the running program never
// reaches it.
//
// This gate asks the narrower question the other one cannot: is this symbol
// referenced ONLY from _test.go files. A yes is not automatically a defect.
// A guard written before the dangerous operation it guards is legitimate and
// often the right order to build in. What is not acceptable is for that state
// to be invisible, so every instance is listed here and each one carries a
// reason.
package build

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
)

// TestOnlySymbol is one exported symbol referenced only from test files.
type TestOnlySymbol struct {
	Dir  string
	Name string
}

// TestOnlyAllowEntry records a symbol that is deliberately not yet called
// from shipping code, and why. An entry without a reason is not an
// exemption, it is a silence, so the loader rejects one.
//
// RetireTicket and CallerSite exist because a reason and a free-text
// "expected caller" sentence are not falsifiable: a caller can be named in
// prose forever without anyone ever checking whether it showed up. Symbol,
// Reason and Caller stay in place unchanged (existing entries keep their
// original narrative); RetireTicket and CallerSite add a machine-checkable
// promise on top of it. See CheckCallerSitesWired.
type TestOnlyAllowEntry struct {
	// Symbol is "<dir>.<Name>", e.g. "internal/policy.IsSomethingAllowed".
	Symbol string `json:"symbol"`
	// Reason says why shipping code does not call this yet. Required.
	Reason string `json:"reason"`
	// Caller names what is expected to call it, so the entry can be
	// retired deliberately rather than forgotten. Required.
	Caller string `json:"expected_caller"`
	// RetireTicket is the exemption key (retireKeyPattern): a plan ticket id
	// "P<phase>-<EPIC>-<nn>" the closed manifest lists as known; a capability
	// id "cap:<name>" for a row whose ticket already closed, with Expires; or
	// UnownedTicket. Legacy ids are refused; CheckRetireKeys checks membership.
	RetireTicket string `json:"retire_ticket"`
	// CallerSite is a repo-relative path to the non-test .go file
	// expected to reference Symbol. Required. CheckCallerSitesWired
	// fails the gate if this file exists and does not reference Symbol:
	// the day the owning ticket creates the file, the exemption must
	// already be wired, or the promise was false.
	CallerSite string `json:"caller_site"`
	// AddedByTicket names the ticket whose lane wrote this entry, so a
	// self-referential exemption (its own author named as the ticket that
	// will retire it) can be refused mechanically. Optional and
	// forward-only; empty means "provenance not recorded" and
	// FindSelfReferentialAllowEntries skips it. When set, a known plan id.
	AddedByTicket string `json:"added_by_ticket,omitempty"`
	// Expires names the plan ticket whose closure expires a cap row: once
	// the manifest lists it closed, the row fails (CheckRetireKeys).
	// Required on a cap row, refused on any other.
	Expires string `json:"expires,omitempty"`
}

// UnownedTicket is the literal RetireTicket value for a legacy exemption
// whose reason names no ticket. Every use is counted (UnownedTicketCount)
// against the shrink-only testdata/testonly-unowned-baseline.txt.
const UnownedTicket = "UNOWNED"

// retireKeyPattern accepts a plan ticket id or a capability id, matched as
// written so a padded key cannot slip past IsCapKey.
var retireKeyPattern = regexp.MustCompile(`^(P\d+-[A-Z]{2,5}-\d{2,3}|cap:[a-z0-9-]+)$`)

// pewttIDPattern is a plan ticket id alone: Expires and AddedByTicket.
var pewttIDPattern = regexp.MustCompile(`^P\d+-[A-Z]{2,5}-\d{2,3}$`)

// IsCapKey reports whether a retire key is a capability id.
func IsCapKey(key string) bool { return strings.HasPrefix(key, "cap:") }

// LoadTestOnlyAllowList reads the allow list from path. A missing file is
// an empty list, not an error: having no exemptions is the stricter state
// and must be the easiest one to be in.
func LoadTestOnlyAllowList(path string) (map[string]TestOnlyAllowEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]TestOnlyAllowEntry{}, nil
		}
		return nil, fmt.Errorf("test-only gate: reading %s: %w", path, err)
	}
	var entries []TestOnlyAllowEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("test-only gate: parsing %s: %w", path, err)
	}
	out := make(map[string]TestOnlyAllowEntry, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.Symbol) == "" {
			return nil, fmt.Errorf("test-only gate: an entry has no symbol")
		}
		if strings.TrimSpace(e.Reason) == "" {
			return nil, fmt.Errorf("test-only gate: %s has no reason", e.Symbol)
		}
		if strings.TrimSpace(e.Caller) == "" {
			return nil, fmt.Errorf("test-only gate: %s names no expected caller", e.Symbol)
		}
		if err := validateTicketAndCallerSite(e); err != nil {
			return nil, err
		}
		out[e.Symbol] = e
	}
	return out, nil
}

// validateTicketAndCallerSite enforces the falsifiable half of an entry: a
// key of a known shape (or the counted UnownedTicket escape hatch), an
// Expires exactly where the key is a capability, and a caller_site path that
// could plausibly exist one day. It never accesses the filesystem;
// CheckCallerSitesWired does that, and CheckRetireKeys checks membership.
func validateTicketAndCallerSite(e TestOnlyAllowEntry) error {
	ticket := e.RetireTicket
	if strings.TrimSpace(ticket) == "" {
		return fmt.Errorf("test-only gate: %s names no retire_ticket", e.Symbol)
	}
	if ticket != UnownedTicket && !retireKeyPattern.MatchString(ticket) {
		return fmt.Errorf("test-only gate: %s has retire_ticket %q, which is neither a plan ticket id, "+
			"a capability id, nor the literal %q", e.Symbol, ticket, UnownedTicket)
	}
	if IsCapKey(ticket) && !pewttIDPattern.MatchString(e.Expires) {
		return fmt.Errorf("test-only gate: %s is keyed %q and needs expires set to a plan ticket id, got %q",
			e.Symbol, ticket, e.Expires)
	}
	if !IsCapKey(ticket) && e.Expires != "" {
		return fmt.Errorf("test-only gate: %s sets expires %q, which only a capability-keyed row may carry",
			e.Symbol, e.Expires)
	}
	site := strings.TrimSpace(e.CallerSite)
	if site == "" {
		return fmt.Errorf("test-only gate: %s names no caller_site", e.Symbol)
	}
	if !strings.HasSuffix(site, ".go") || strings.HasSuffix(site, "_test.go") {
		return fmt.Errorf("test-only gate: %s has caller_site %q, which must be a non-test .go file", e.Symbol, site)
	}
	if strings.HasPrefix(site, "/") || strings.Contains(site, "..") {
		return fmt.Errorf("test-only gate: %s has caller_site %q, which must be a repo-relative path", e.Symbol, site)
	}
	if e.AddedByTicket != "" && !pewttIDPattern.MatchString(e.AddedByTicket) {
		return fmt.Errorf("test-only gate: %s has added_by_ticket %q, which is not a plan ticket id",
			e.Symbol, e.AddedByTicket)
	}
	return nil
}

// UnownedTicketCount returns how many entries in allow carry the literal
// RetireTicket UnownedTicket. The caller compares this against a checked-in
// baseline so the count of un-owned legacy exemptions is visible and can
// only shrink, never silently grow.
func UnownedTicketCount(allow map[string]TestOnlyAllowEntry) int {
	n := 0
	for _, e := range allow {
		if e.RetireTicket == UnownedTicket {
			n++
		}
	}
	return n
}

// CapKeyedCount returns how many entries in allow are keyed by a capability
// id. Like UnownedTicketCount it is compared against a shrink-only baseline
// (testdata/testonly-capkeyed-baseline.txt): cap rows are closed debt.
func CapKeyedCount(allow map[string]TestOnlyAllowEntry) int {
	n := 0
	for _, e := range allow {
		if IsCapKey(e.RetireTicket) {
			n++
		}
	}
	return n
}

// FindTestOnlySymbols returns exported symbols that shipping code declares
// but only test code references.
//
// declared comes from the shipping-file scan, so a symbol declared only in a
// test file never appears. usedByShipping and usedByTests are reference sets
// gathered from the two file groups separately.
func FindTestOnlySymbols(
	declared map[DeadCodeSymbol]DeadCodeDecl,
	usedByShipping, usedByTests map[DeadCodeSymbol]bool,
) []TestOnlySymbol {
	var out []TestOnlySymbol
	for sym := range declared {
		if usedByShipping[sym] {
			continue
		}
		if !usedByTests[sym] {
			// Referenced by nothing at all. That is the dead-code
			// gate's finding, not this one, and reporting it here
			// too would double-count a single problem.
			continue
		}
		out = append(out, TestOnlySymbol(sym))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// FilterTestOnlyAllowed drops symbols present in the allow list and reports
// which allow-list entries matched nothing, so a stale exemption surfaces
// instead of accumulating.
func FilterTestOnlyAllowed(
	found []TestOnlySymbol, allow map[string]TestOnlyAllowEntry,
) (violations []TestOnlySymbol, staleAllowances []string) {
	matched := make(map[string]bool, len(allow))
	for _, s := range found {
		key := s.Dir + "." + s.Name
		if _, ok := allow[key]; ok {
			matched[key] = true
			continue
		}
		violations = append(violations, s)
	}
	for key := range allow {
		if !matched[key] {
			staleAllowances = append(staleAllowances, key)
		}
	}
	sort.Strings(staleAllowances)
	return violations, staleAllowances
}

// ScanTestOnlySymbols scans the tree and returns exported symbols that
// shipping code declares and only test code references.
//
// It reuses the dead-code scanner's collectors rather than walking the tree
// a second time, so the two gates cannot drift on what counts as a
// declaration or a reference. The only difference is that the parsed files
// are partitioned before references are gathered.
func ScanTestOnlySymbols(moduleRoot, modulePath string, roots []string) ([]TestOnlySymbol, error) {
	files, err := deadcodeCollectFiles(moduleRoot, roots)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	shipping := make(map[string]*ast.File)
	tests := make(map[string]*ast.File)
	all := make(map[string]*ast.File, len(files))
	for _, path := range files {
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil, perr
		}
		all[path] = f
		if strings.HasSuffix(path, "_test.go") {
			tests[path] = f
			continue
		}
		shipping[path] = f
	}

	// Declarations come from shipping files only, which is what
	// deadcodeCollectDeclared already enforces internally.
	declared, declPos, err := deadcodeCollectDeclared(moduleRoot, fset, all)
	if err != nil {
		return nil, err
	}

	usedByShipping, err := deadcodeCollectUsed(moduleRoot, modulePath, shipping, declPos)
	if err != nil {
		return nil, err
	}
	usedByTests, err := deadcodeCollectUsed(moduleRoot, modulePath, tests, declPos)
	if err != nil {
		return nil, err
	}

	// The collector ignores a declaration's own identifier (declPos).
	return FindTestOnlySymbols(declared, usedByShipping, usedByTests), nil
}
