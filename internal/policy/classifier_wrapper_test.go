package policy

import (
	"strings"
	"testing"
)

// specWrapperSentenceVerbatim is copied verbatim from 06-FORGE-SPEC.md's
// numbered item 15. As with the ladder, this is a SECOND transcription:
// the wrapper set is re-derived from the spec sentence below and compared
// against wrapperTable, so a wrapper form dropped from the table fails
// here instead of passing a table checked against itself.
const specWrapperSentenceVerbatim = `Wrapper/indirection forms ` +
	`(sh -c, xargs, make, npm scripts, ssh remote) classify at ` +
	`max(wrapper, resolved-inner); an unresolvable inner ⇒ L4.`

// specWrapperToCommands bridges each spec phrase to the command names
// wrapperTable is expected to register for it. wrapperTable never appears
// on the right-hand side here: it is what the test compares against.
var specWrapperToCommands = map[string][]string{
	"sh -c":       {"sh", "bash", "zsh", "dash", "ksh"},
	"xargs":       {"xargs"},
	"make":        {"make"},
	"npm scripts": {"npm", "pnpm", "yarn"},
	"ssh remote":  {"ssh"},
}

func TestWrapperTableMatchesSpec(t *testing.T) {
	open := strings.Index(specWrapperSentenceVerbatim, "(")
	closeAt := strings.Index(specWrapperSentenceVerbatim, ")")
	if open < 0 || closeAt < open {
		t.Fatal("specWrapperSentenceVerbatim no longer lists the wrapper forms in parentheses")
	}
	phrases := strings.Split(specWrapperSentenceVerbatim[open+1:closeAt], ", ")
	if len(phrases) != 5 {
		t.Fatalf("spec names %d wrapper forms, expected 5 (drift in the transcription itself)", len(phrases))
	}
	want := map[string]bool{}
	for _, phrase := range phrases {
		commands, ok := specWrapperToCommands[phrase]
		if !ok {
			t.Fatalf("no translation registered for spec wrapper phrase %q", phrase)
		}
		for _, c := range commands {
			want[c] = true
		}
	}
	for c := range want {
		if _, ok := wrapperTable[c]; !ok {
			t.Errorf("the spec names wrapper %q but wrapperTable does not register it", c)
		}
	}
	for c := range wrapperTable {
		if !want[c] {
			t.Errorf("wrapperTable registers %q but it is not derivable from the spec wrapper list", c)
		}
	}
	if !strings.Contains(specWrapperSentenceVerbatim, "an unresolvable inner ⇒ L4") {
		t.Error("specWrapperSentenceVerbatim no longer states the unresolvable-inner rule")
	}
}

// TestWrapperTakesTheMaxOfWrapperAndInner walks each wrapper form with an
// inner the classifier can resolve.
func TestWrapperTakesTheMaxOfWrapperAndInner(t *testing.T) {
	cases := []struct {
		cmd  string
		want RiskLevel
		why  string
	}{
		{"sh -c ls", L1, "the shell wrapper itself is local dev, so an L0 inner rises to L1"},
		{"sh -c 'go test ./...'", L1, "max(L1 wrapper, L1 inner)"},
		{"sh -c 'git add .'", L2, "max(L1 wrapper, L2 inner)"},
		{"sh -c 'git push origin main'", L3, "max(L1 wrapper, L3 inner)"},
		{"bash -c 'git push'", L3, "every shell in the family behaves the same"},
		{"sh -c 'rm -rf /tmp/data'", L4, "max(L1 wrapper, L4 inner)"},
		{"xargs ls", L1, "max(L1 wrapper, L0 inner)"},
		{"xargs -0 -n 1 git add", L2, "xargs options are skipped to find the inner command"},
		{"xargs git push", L3, "max(L1 wrapper, L3 inner)"},
		{"ssh build-host ls", L3, "ssh is external, so even a read-only inner is L3"},
		{"ssh -p 2222 build-host git add .", L3, "max(L3 wrapper, L2 inner)"},
		{"ssh build-host rm -rf /tmp/data", L4, "max(L3 wrapper, L4 inner)"},
		{"npm install", L3, "npm's network verbs are external side effects"},
		{"npm ls", L1, "max(L1 wrapper, L0 inner)"},
		{"pnpm publish", L3, "the whole npm family shares one verb table"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, err := classify(t, tc.cmd)
			if err != nil {
				t.Fatalf("Classify(%q) refused with %v; want %s because %s", tc.cmd, err, tc.want, tc.why)
			}
			if got != tc.want {
				t.Fatalf("Classify(%q) = %s, want %s because %s", tc.cmd, got, tc.want, tc.why)
			}
		})
	}
}

// TestUnresolvableInnerRefusesTheWholeForm covers the §5.15 rule for each
// wrapper form: if the inner cannot be resolved, the form is L4 no matter
// how harmless the wrapper looks.
func TestUnresolvableInnerRefusesTheWholeForm(t *testing.T) {
	cases := []struct {
		why string
		cmd string
	}{
		{"sh -c with a variable", `sh -c "$VAR"`},
		{"sh -c with a substitution", "sh -c \"$(cat script.sh)\""},
		{"sh -c with an empty string", `sh -c ""`},
		{"sh -c with no command string", `sh -c`},
		{"an interactive shell", `sh`},
		{"a shell running a script file", `sh ./deploy.sh`},
		{"xargs with no inner command", `xargs`},
		{"xargs with only options", `xargs -0`},
		{"xargs with a variable inner", `xargs $CMD`},
		{"xargs with an unrecognised option", `xargs --frobnicate rm`},
		{"an interactive ssh session", `ssh build-host`},
		{"ssh with a variable inner", `ssh build-host $CMD`},
		{"ssh with an unrecognised option", `ssh --frobnicate build-host ls`},
		{"an npm script", `npm run build`},
		{"an npm test script", `npm test`},
		{"a yarn script", `yarn start`},
		{"an unknown npm verb", `npm nonesuch`},
		{"npm with no verb", `npm`},
		{"a make target", `make build`},
		{"make with no target", `make`},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) { mustRefuse(t, tc.cmd, ErrClassifyUnknown) })
	}
}

// TestMakeAndNpmScriptsHaveNoResolvableInner records WHY those two forms
// are always refused: their body is not in the command string at all, so
// there is no inner to resolve at any level of effort.
func TestMakeAndNpmScriptsHaveNoResolvableInner(t *testing.T) {
	_, err := classify(t, "make build")
	if err == nil || !strings.Contains(err.Error(), "not in the command string") {
		t.Errorf("make refusal = %v; it should say the recipe is not in the command string", err)
	}
	_, err = classify(t, "npm run build")
	if err == nil || !strings.Contains(err.Error(), "package.json") {
		t.Errorf("npm run refusal = %v; it should say the script body lives in package.json", err)
	}
}

// TestNestedWrappersAreBounded: a wrapper may contain a wrapper, but not
// without limit. Beyond the bound the form is refused rather than being
// allowed to consume the stack.
func TestNestedWrappersAreBounded(t *testing.T) {
	inner := "ls"
	nests := maxWrapperDepth + 2
	for i := 0; i < nests; i++ {
		inner = "sh -c " + singleQuote(inner)
	}
	got, err := classify(t, inner)
	if got != L4 || err == nil {
		t.Fatalf("a %d-deep wrapper nest classified as %s, err=%v; want a refusal at L4", nests, got, err)
	}
}

// singleQuote wraps a script in single quotes, escaping any it contains.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestWrapperLevelFloorHolds: the wrapper's own rung is a floor, so a
// wrapper never classifies below what running the wrapper itself costs.
func TestWrapperLevelFloorHolds(t *testing.T) {
	for name, class := range wrapperTable {
		if class.Risk() < L1 {
			t.Errorf("wrapper %q sits at %s; running a wrapper is never a read-only act", name, class.Risk())
		}
	}
}
