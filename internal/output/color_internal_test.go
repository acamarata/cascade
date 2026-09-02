// Purpose: exhaustive, branch-level tests for noColorResolved — the
//
//	unexported precedence function behind Mode.NoColor (output.go).
//
// Inputs: none (pure function under test — flag/env/TERM/isTTY are all
//
//	function parameters, never read from globals mid-function).
//
// Outputs: none (test-only file).
//
// Constraints: package output (not output_test) — noColorResolved is
//
//	unexported, so only an internal test file can call it directly. The
//	rest of this package's tests (output_test.go et al.) exercise it only
//	indirectly through New(), which always supplies a non-TTY stdout
//	(regularFile/*bytes.Buffer never satisfy IsTerminal) and so only ever
//	reaches the "!isTTY" branch. This file is what actually proves the
//	full flag > NO_COLOR > TERM=dumb > non-TTY precedence, including the
//	"colour genuinely enabled" default branch that was previously
//	unreached by any test (CR fix, P1-E04-W1-S06-T5: noColorResolved
//	measured 33.3% branch coverage before this file).
//
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).
package output

import "testing"

// noColorResolvedCase is one row of TestNoColorResolved's table. Split out
// as a named type (rather than an anonymous struct inline in the test func)
// so the case table itself can live at package scope — keeping the test
// function body under Art.10.3's 50-line cap.
type noColorResolvedCase struct {
	name          string
	flag          bool
	noColorEnvSet bool
	term          string
	isTTY         bool
	want          bool
}

// noColorResolvedCases covers every branch of noColorResolved, including
// precedence ORDER (a higher-precedence input forcing true/false even when
// a lower-precedence input, taken alone, would decide the opposite way) and
// the one case previous coverage never reached: colour genuinely enabled
// (flag=false, NO_COLOR unset, TERM not "dumb", isTTY=true).
//
// NO_COLOR convention: per https://no-color.org, the variable's mere
// PRESENCE disables colour — its value is irrelevant, including the empty
// string. noColorResolved's noColorEnvSet parameter already encodes this
// correctly (callers pass the boolean from os.LookupEnv's second return,
// not os.Getenv's value), so `NO_COLOR=""` and `NO_COLOR=1` are
// indistinguishable to this function by design. The "NO_COLOR set to empty
// string" case below asserts exactly that rule: noColorEnvSet=true (as
// LookupEnv reports for a set-but-empty var) still forces NoColor=true.
var noColorResolvedCases = []noColorResolvedCase{
	// --- single-cause branches ---
	{
		name: "flag true, everything else would allow colour",
		flag: true, noColorEnvSet: false, term: "xterm-256color", isTTY: true,
		want: true,
	},
	{
		name: "NO_COLOR set, everything else would allow colour",
		flag: false, noColorEnvSet: true, term: "xterm-256color", isTTY: true,
		want: true,
	},
	{
		name: "TERM=dumb, everything else would allow colour",
		flag: false, noColorEnvSet: false, term: "dumb", isTTY: true,
		want: true,
	},
	{
		name: "non-TTY, everything else would allow colour",
		flag: false, noColorEnvSet: false, term: "xterm-256color", isTTY: false,
		want: true,
	},
	{
		name: "colour genuinely enabled: no flag, no NO_COLOR, TERM not dumb, real TTY",
		flag: false, noColorEnvSet: false, term: "xterm-256color", isTTY: true,
		want: false,
	},
	// --- precedence ORDER: a higher-precedence cause must win even when a
	// lower-precedence input, taken alone, would decide the opposite way.
	{
		name: "flag explicitly false does not override NO_COLOR (env still wins)",
		flag: false, noColorEnvSet: true, term: "xterm-256color", isTTY: true,
		want: true,
	},
	{
		name: "TERM=dumb wins on a real TTY even with NO_COLOR unset",
		flag: false, noColorEnvSet: false, term: "dumb", isTTY: true,
		want: true,
	},
	{
		name: "flag true wins even over a real TTY with TERM not dumb and NO_COLOR unset",
		flag: true, noColorEnvSet: false, term: "xterm-256color", isTTY: true,
		want: true,
	},
	{
		name: "NO_COLOR set to empty string still disables colour (presence, not value)",
		// LookupEnv("NO_COLOR") for `NO_COLOR=` reports ok=true with value
		// "" — noColorEnvSet is that ok, so this case is indistinguishable
		// from any other NO_COLOR value by design.
		flag: false, noColorEnvSet: true, term: "xterm-256color", isTTY: true,
		want: true,
	},
	{
		name: "non-TTY wins even with TERM not dumb and NO_COLOR unset",
		flag: false, noColorEnvSet: false, term: "", isTTY: false,
		want: true,
	},
}

// TestNoColorResolved runs noColorResolvedCases against noColorResolved
// directly — see that var's doc comment for what precedence rules this
// pins.
func TestNoColorResolved(t *testing.T) {
	for _, tt := range noColorResolvedCases {
		t.Run(tt.name, func(t *testing.T) {
			got := noColorResolved(tt.flag, tt.noColorEnvSet, tt.term, tt.isTTY)
			if got != tt.want {
				t.Errorf("noColorResolved(flag=%v, noColorEnvSet=%v, term=%q, isTTY=%v) = %v, want %v",
					tt.flag, tt.noColorEnvSet, tt.term, tt.isTTY, got, tt.want)
			}
		})
	}
}
