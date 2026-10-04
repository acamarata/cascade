// Purpose: the credential gate's fail-closed branches and its scan bounds.
//
//	An empty pattern table and a pattern with no expression each refuse
//	every non-empty value on real SQLite (P1-BF-R88b). The Decode window
//	scan stays bounded: a run over maxDecodeRun fails closed without a
//	Decode call, a run at the cap is scanned at every offset with at most
//	two calls per offset (one per alphabet, P1-BF-R94), and a value that
//	would exceed maxDecodeCalls fails closed at exactly that many.
//
// SPORT: learn/credential-failclosed/ADD (P1-CAP-02).
package learn

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// swapCredentialPatterns replaces the gate's pattern table for one test.
// No test in this package runs in parallel, so the swap is race-free.
func swapCredentialPatterns(t *testing.T, table []secrets.Pattern) {
	t.Helper()
	orig := credentialPatterns
	credentialPatterns = func() []secrets.Pattern { return table }
	t.Cleanup(func() { credentialPatterns = orig })
}

// TestCredentialFailClosedBranches: with an empty pattern table, and with
// a table holding one pattern whose Expr is nil, an ordinary label is
// flagged, "" is not, and Record refuses a benign outcome leaving zero rows.
func TestCredentialFailClosedBranches(t *testing.T) {
	cases := []struct {
		name  string
		table []secrets.Pattern
	}{
		{"empty-table", nil},
		{"nil-expr", []secrets.Pattern{{Class: secrets.ClassBase64JSON, Name: "no-expr"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapCredentialPatterns(t, tc.table)
			if !credentialShaped("tier-1") {
				t.Errorf("%s: an ordinary label is not flagged; the branch fails open", tc.name)
			}
			if credentialShaped("") {
				t.Errorf("%s: the empty value is flagged", tc.name)
			}
			db := newTestOutcomeDB(t)
			err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), baseOutcome("job-ok-after"))
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("%s: Record did not refuse with KindInvalidInput: %v", tc.name, err)
			}
			if n := telemetryRowCount(t, db); n != 0 {
				t.Errorf("%s: rows = %d, want 0", tc.name, n)
			}
		})
	}
}

// countingDecodePattern returns the registry's Decode pattern with its
// Decode wrapped to count calls.
func countingDecodePattern(t *testing.T, calls *int) secrets.Pattern {
	t.Helper()
	for _, p := range secrets.DefaultRegistry().Patterns() {
		if p.Decode != nil {
			inner := p.Decode
			p.Decode = func(s string) bool { *calls++; return inner(s) }
			return p
		}
	}
	t.Fatal("the default registry has no Decode pattern")
	return secrets.Pattern{}
}

// TestCredentialDecodeScanIsBounded: a 20k-character run fails closed with
// no Decode call; a single-alphabet junk run of exactly maxDecodeRun is not
// flagged after one call per offset, a mixed-alphabet one after at most two;
// a canary at the end of that run is found; seventeen single-alphabet runs
// exhaust maxDecodeCalls and fail closed at exactly that many calls.
func TestCredentialDecodeScanIsBounded(t *testing.T) {
	calls := 0
	p := countingDecodePattern(t, &calls)
	if !patternMatches(p, strings.Repeat("A", 20000)) || calls != 0 {
		t.Errorf("20k run: want fail-closed with 0 Decode calls, got %d calls", calls)
	}
	offsets := maxDecodeRun - minDecodeWindow + 1
	atCap := strings.Repeat("A", maxDecodeRun)
	calls = 0
	if patternMatches(p, atCap) || calls != offsets {
		t.Errorf("junk run at the cap: flagged or %d Decode calls, want not flagged after %d", calls, offsets)
	}
	a30 := strings.Repeat("A", 30)
	mixed := strings.Repeat(a30+"+"+a30+"-", maxDecodeRun)[:maxDecodeRun]
	calls = 0
	if patternMatches(p, mixed) || calls <= offsets || calls > 2*offsets {
		t.Errorf("mixed run at the cap: flagged or %d Decode calls, want not flagged after %d..%d", calls, offsets+1, 2*offsets)
	}
	blob := `{"api_` + `token":"` + strings.Repeat("Q7", 8) + `"}`
	canary := base64.RawURLEncoding.EncodeToString([]byte(blob))
	deep := atCap[:maxDecodeRun-len(canary)-2] + canary + "-x"
	if !patternMatches(p, deep) || !credentialShaped(deep) {
		t.Error("canary at the end of a cap-length run not flagged")
	}
	runs := maxDecodeCalls/offsets + 1
	calls = 0
	many := strings.TrimSuffix(strings.Repeat(atCap+".", runs), ".")
	if !patternMatches(p, many) || calls != maxDecodeCalls {
		t.Errorf("%d cap-length runs: want fail-closed after %d calls, got %d", runs, maxDecodeCalls, calls)
	}
}
