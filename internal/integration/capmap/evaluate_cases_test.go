//go:build capmap

package capmap

import (
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// passedAll is the passed set under which the fixture table is clean.
var passedAll = map[string]bool{"TestCapmap_AlphaRun": true, "TestCapmap_BetaGet": true, "TestCapmap_BetaRefusal": true}

// fixture loads the clean fixture table and id list and proves the pair is
// clean first, so a case that expects findings cannot pass on a table that
// was already broken.
func fixture(t *testing.T) (string, []string) {
	t.Helper()
	table, ids, err := loadInputs("testdata/valid-table.md", "testdata/ids.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fs := evaluate(table, ids, passedAll); len(fs) != 0 {
		t.Fatalf("the fixture is not clean: %v", findingsError(fs))
	}
	return table, ids
}

// mutate replaces the one occurrence of old in table with repl. It fails if
// old is absent or repeated, so a case never runs against an unchanged table.
func mutate(t *testing.T, table, old, repl string) string {
	t.Helper()
	if n := strings.Count(table, old); n != 1 {
		t.Fatalf("mutation target %q occurs %d times in the fixture, want 1", old, n)
	}
	return strings.Replace(table, old, repl, 1)
}

// dropLines removes every line of table that contains marker. It fails if
// none does.
func dropLines(t *testing.T, table, marker string) string {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(table, "\n") {
		if !strings.Contains(line, marker) {
			kept = append(kept, line)
		}
	}
	if len(kept) == strings.Count(table, "\n")+1 {
		t.Fatalf("no fixture line contains %q", marker)
	}
	return strings.Join(kept, "\n")
}

// codesOf returns the distinct finding codes in fs, sorted.
func codesOf(fs []finding) []findingCode {
	var out []findingCode
	for _, f := range fs {
		if !slices.Contains(out, f.Code) {
			out = append(out, f.Code)
		}
	}
	slices.Sort(out)
	return out
}

// wantCodes fails unless fs holds exactly the wanted distinct codes.
func wantCodes(t *testing.T, fs []finding, want ...findingCode) {
	t.Helper()
	slices.Sort(want)
	if got := codesOf(fs); !slices.Equal(got, want) {
		t.Fatalf("finding codes = %v, want %v; findings:\n%v", got, want, findingsError(fs))
	}
}

// TestCapabilityMapDetectsMissingRow drops each capability's rows from a
// copy of the fixture and expects exactly that capability reported, then
// covers an unknown id, an empty id list and a table with no rows.
func TestCapabilityMapDetectsMissingRow(t *testing.T) {
	table, ids := fixture(t)
	for _, id := range ids {
		t.Run("drop "+id, func(t *testing.T) {
			fs := evaluate(dropLines(t, table, "| "+id+" |"), ids, passedAll)
			wantCodes(t, fs, findMissingCapability)
			if len(fs) != 1 || fs[0].Capability != id {
				t.Fatalf("findings = %v, want one missing-capability for %s", fs, id)
			}
		})
	}
	t.Run("one of two rows dropped is still covered", func(t *testing.T) {
		wantCodes(t, evaluate(dropLines(t, table, "cascade delta export"), ids, passedAll))
	})
	t.Run("unknown capability id", func(t *testing.T) {
		fs := evaluate(mutate(t, table, "| cap:gamma |", "| cap:gammma |"), ids, passedAll)
		wantCodes(t, fs, findUnknownCapability, findMissingCapability)
	})
	t.Run("empty id list", func(t *testing.T) {
		wantCodes(t, evaluate(table, nil, passedAll), findNoCapabilityIDs)
	})
	t.Run("header only", func(t *testing.T) {
		fs := evaluate(dropLines(t, table, "| cap:"), ids, passedAll)
		wantCodes(t, fs, findMissingCapability)
		if len(fs) != len(ids) {
			t.Fatalf("got %d findings for a header-only table, want %d", len(fs), len(ids))
		}
	})
}

// badTableCases each break the fixture one way and name the one code the
// break must raise. A header or separator defect leaves no rows to read, so
// every capability is also reported missing.
var badTableCases = []struct {
	label, old, repl string
	want             []findingCode
}{
	{"renamed column", "| owner |", "| owners |", []findingCode{findBadHeader, findMissingCapability}},
	{"swapped columns", "| classification | owner |", "| owner | classification |", []findingCode{findBadHeader, findMissingCapability}},
	{"five columns", "| capability | entrypoint |", "| entrypoint |", []findingCode{findBadHeader, findMissingCapability}},
	{"no separator row", "| --- | --- | --- | --- | --- | --- |\n", "", []findingCode{findBadSeparator, findMissingCapability}},
	{"separator with five cells", "| --- | --- | --- | --- | --- | --- |\n", "| --- | --- | --- | --- | --- |\n", []findingCode{findBadSeparator, findMissingCapability}},
	{"separator with seven cells", "| --- | --- | --- | --- | --- | --- |\n", "| --- | --- | --- | --- | --- | --- | --- |\n", []findingCode{findBadSeparator, findMissingCapability}},
	{"last row without leading pipe", "| refusal documented |", "| refusal documented |\ncap:b | e | p | verified | - | TestCapmap_Nope |", []findingCode{findBadRow}},
	{"middle row without leading pipe", "| cap:gamma | cascade gamma |", "cap:gamma | cascade gamma |", []findingCode{findBadRow, findMissingCapability}},
	{"unknown classification", "| verified | - | `TestCapmap_AlphaRun` |", "| verifed | - | `TestCapmap_AlphaRun` |", []findingCode{findUnknownClassification}},
	{"row with too few columns", "| cap:gamma | cascade gamma | present in tree, no probe yet | present-unverified | P2-ABC-01 | reviewed, no probe |", "| cap:gamma | cascade gamma |", []findingCode{findBadRow, findMissingCapability}},
}

// TestCapabilityMapRejectsBadHeader covers a wrong header, a bad separator,
// an empty required cell in every column, an unknown classification and
// input with no table at all.
func TestCapabilityMapRejectsBadHeader(t *testing.T) {
	table, ids := fixture(t)
	for _, c := range badTableCases {
		t.Run(c.label, func(t *testing.T) {
			wantCodes(t, evaluate(mutate(t, table, c.old, c.repl), ids, passedAll), c.want...)
		})
	}
	alpha := "| cap:alpha | cascade alpha run | cmd/cascade to the alpha subsystem | verified | - | `TestCapmap_AlphaRun` |"
	for col, name := range tableHeader {
		t.Run("empty "+name, func(t *testing.T) {
			cs := cells(alpha)
			cs[col] = ""
			fs := evaluate(mutate(t, table, alpha, "| "+strings.Join(cs, " | ")+" |"), ids, passedAll)
			if !slices.Contains(codesOf(fs), findEmptyCell) {
				t.Fatalf("an empty %s cell was not reported: %v", name, fs)
			}
		})
	}
	t.Run("no table", func(t *testing.T) {
		wantCodes(t, evaluate("just prose, no pipes\n", ids, passedAll), findNoTable, findMissingCapability)
	})
	t.Run("pipe prose after a blank line is not a row", func(t *testing.T) {
		wantCodes(t, evaluate(mutate(t, table, "| refusal documented |", "| refusal documented |\n\nsee a | b"), ids, passedAll))
	})
	t.Run("second table", func(t *testing.T) {
		wantCodes(t, evaluate(table+"\n| a | b |\n| - | - |\n", ids, passedAll), findSecondTable)
	})
	t.Run("fenced example is not the table", func(t *testing.T) {
		fenced := "```\n" + table + "\n```\n"
		wantCodes(t, evaluate(fenced, ids, passedAll), findNoTable, findMissingCapability)
	})
	t.Run("findings convert to an invalid-input error", func(t *testing.T) {
		err := findingsError(evaluate("no table", ids, passedAll))
		if !cascade.HasKind(err, cascade.KindInvalidInput) || findingsError(nil) != nil {
			t.Fatalf("findingsError kind wrong: %v", err)
		}
	})
}

// TestCapabilityMapRequiresProbeForVerified breaks the proof a verified row
// needs: no probe named, an unregistered, skipped or failed probe, or one of
// two named probes missing.
func TestCapabilityMapRequiresProbeForVerified(t *testing.T) {
	table, ids := fixture(t)
	t.Run("no probe named in evidence", func(t *testing.T) {
		fs := evaluate(mutate(t, table, "`TestCapmap_AlphaRun`", "see the alpha tests"), ids, passedAll)
		wantCodes(t, fs, findNoProbeNamed)
	})
	t.Run("probe not registered", func(t *testing.T) {
		passed := runProbes(t, []string{"TestCapmap_AlphaRun"}, map[string]func(*testing.T){})
		fs := evaluate(table, ids, merge(passed, "TestCapmap_BetaGet", "TestCapmap_BetaRefusal"))
		wantCodes(t, fs, findProbeNotPassed)
	})
	t.Run("probe skipped", func(t *testing.T) {
		passed := runProbes(t, []string{"TestCapmap_AlphaRun"}, map[string]func(*testing.T){
			"TestCapmap_AlphaRun": func(t *testing.T) { t.Skip("needs a service") },
		})
		fs := evaluate(table, ids, merge(passed, "TestCapmap_BetaGet", "TestCapmap_BetaRefusal"))
		wantCodes(t, fs, findProbeNotPassed)
		if fs[0].Capability != "cap:alpha" {
			t.Fatalf("finding names %q, want cap:alpha", fs[0].Capability)
		}
	})
	t.Run("probe failed", func(t *testing.T) {
		fs := evaluate(table, ids, map[string]bool{"TestCapmap_BetaGet": true, "TestCapmap_BetaRefusal": true})
		wantCodes(t, fs, findProbeNotPassed)
	})
	t.Run("one of two named probes did not pass", func(t *testing.T) {
		fs := evaluate(table, ids, map[string]bool{"TestCapmap_AlphaRun": true, "TestCapmap_BetaGet": true})
		wantCodes(t, fs, findProbeNotPassed)
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "TestCapmap_BetaRefusal") {
			t.Fatalf("findings = %v, want one naming TestCapmap_BetaRefusal", fs)
		}
	})
	t.Run("a longer name does not satisfy a prefix", func(t *testing.T) {
		fs := evaluate(mutate(t, table, "`TestCapmap_AlphaRun`", "`TestCapmap_AlphaRun_extra`"), ids, passedAll)
		wantCodes(t, fs, findNoProbeNamed)
	})
}

// merge returns passed plus the given names.
func merge(passed map[string]bool, names ...string) map[string]bool {
	out := map[string]bool{}
	for k, v := range passed {
		out[k] = v
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// gapRows locate the class and owner cells of each non-verified fixture row.
// badOwners are the owner cells such a row must reject.
var (
	gapRows   = map[string]string{"present-unverified": "| present-unverified | P2-ABC-01 |", "missing": "| missing | NEW:P1-DELTA |", "policy-refused-as-specified": "| policy-refused-as-specified | P3-XYZ-12 |"}
	badOwners = map[string]string{"dash": "-", "empty": "", "free text": "someone", "lowercase id": "p2-abc-01", "bare NEW": "NEW:", "text before id": "see P2-ABC-01", "text after id": "P1-BLD-07 later", "text after NEW id": "NEW:P1-FOO and more"}
)

// TestCapabilityMapRequiresOwnerForGap blanks or garbles the owner of each
// non-verified class and expects a missing-owner finding, and checks that
// a verified row may carry "-".
func TestCapabilityMapRequiresOwnerForGap(t *testing.T) {
	table, ids := fixture(t)
	for class, cells := range gapRows {
		for label, owner := range badOwners {
			t.Run(class+" owner "+label, func(t *testing.T) {
				repl := "| " + class + " | " + owner + " |"
				fs := evaluate(mutate(t, table, cells, repl), ids, passedAll)
				if !slices.Contains(codesOf(fs), findMissingOwner) {
					t.Fatalf("%s row with owner %q was accepted: %v", class, owner, fs)
				}
			})
		}
	}
	t.Run("a verified row needs no owner id", func(t *testing.T) {
		wantCodes(t, evaluate(table, ids, passedAll))
	})
	t.Run("both id shapes are accepted", func(t *testing.T) {
		fs := evaluate(mutate(t, table, "| missing | NEW:P1-DELTA |", "| missing | P12-AB3-007 |"), ids, passedAll)
		wantCodes(t, fs)
	})
}
