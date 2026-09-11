// Package build (this file) holds the counts drift gate: the owner's
// direct request ("for things like number of something ... we need a good
// SPORT model or where to see as we develop because this changes all the
// time") made falsifiable. internal/inventory (its doc comment is the
// companion read) computes the tree's real counts; this gate scans every
// git-tracked prose file for a NUMBER stated next to a NOUN it knows about
// ("14-kind", "fourteen kinds", "eleven domain", "eleven-domain") and fails
// when the stated number disagrees with the derived one.
//
// Sources for the two nouns this gate currently knows (extend
// CountsDriftNounCounts when a third prose-stated count needs a floor):
//   - "kind"   -> len(pkg/cascade.AllKinds()), the frozen R-14.3 taxonomy.
//   - "domain" -> len(internal/storage.AllDomains), the closed R-14.5/R-16.51
//     domain enumeration.
//
// Both digit ("14-kind") and English word ("fourteen kinds", "eleven
// domain") forms are recognized; countsDriftWordToNumber covers zero
// through twenty, which is every number either taxonomy has ever needed
// and is expected to need for the foreseeable future.
//
// What this gate CANNOT catch, stated here per this package's own
// convention (every gate owes its blind spot in its header):
//   - A number word above twenty, or a non-English number word.
//   - A stated count for a noun not in CountsDriftNounCounts (adding a
//     third frozen/closed enumeration this repo states in prose requires a
//     one-line addition here; until that line lands, prose about it can
//     drift silently, same as before this gate existed for kind/domain).
//   - A file extension outside countsDriftScannedExt (binary files, and any
//     text format not in that set, are not scanned at all).
//   - A number correctly stated but attributed to the WRONG noun ("14
//     domains" when the taxonomy in scope is actually kinds) — the gate
//     only knows the noun word, not which enumeration the surrounding
//     sentence actually means, so a swapped attribution that happens to
//     equal a DIFFERENT noun's derived count passes silently.
//   - Untracked or gitignored files (matches every other gate's git
//     ls-files scope, including the planning corpus under .claude/, which
//     is gitignored and cannot be scanned in CI at all — AGENT-BRIEF's own
//     constraint).
//
// SPORT: internal.build.CheckCountsDrift/ADDED.
package build

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CountDriftViolation is one tracked-file line whose stated number
// disagrees with the noun's derived count.
type CountDriftViolation struct {
	File    string
	Line    int
	Noun    string
	Stated  int
	Derived int
	Text    string
}

// String renders one violation for gate failure output.
func (v CountDriftViolation) String() string {
	return fmt.Sprintf("%s:%d: states %d %s(s), derived count is %d: %q",
		v.File, v.Line, v.Stated, v.Noun, v.Derived, v.Text)
}

// CountsDriftNounCounts returns the live noun -> derived-count map this
// gate checks prose against. Computed fresh on every call from the same
// artifacts internal/inventory's live fields use (cascade.AllKinds,
// storage.AllDomains), never from a second hand-maintained number.
func CountsDriftNounCounts() map[string]int {
	return map[string]int{
		"kind":   len(cascade.AllKinds()),
		"domain": len(storage.AllDomains),
	}
}

// countsDriftWordToNumber covers the English number words zero through
// twenty (case-insensitive lookup at call sites).
var countsDriftWordToNumber = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14,
	"fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20,
}

// countsDriftPattern matches a number token (digits or an English word)
// immediately followed by a hyphen or a single space and then one of this
// gate's known nouns, optionally pluralized: "14-kind", "fourteen kinds",
// "eleven domain", "eleven-domain". A match alone is not enough to flag a
// violation — countsDriftAnchored additionally requires the surrounding
// text to actually be STATING a total (see its doc comment), because
// "kind" and "domain" are both common English words this tree also uses
// in senses that have nothing to do with a frozen count ("one domain's
// anchor table", "R-21.223 domain 4", "the two kinds of Store record" for
// an unrelated local enum).
var countsDriftPattern = regexp.MustCompile(`(?i)\b([0-9]+|[a-z]+)[- ](kind|domain)(s)?\b`)

// countsDriftAnchorWords are the words this repo's own prose always pairs
// with a genuine total-count statement for "kind" or "domain": the noun
// turns into a stated total when one of these words comes right after it
// (a digit count immediately followed by "kind" and then the word
// "taxonomy" is one shape this catches; a spelled-out number immediately
// followed by "domain" and then either "set" or "enumeration" is another).
// Requiring one immediately after the matched noun is what keeps this gate
// from flagging every incidental "one domain" or "two kinds of X" in the
// tree — a documented precision/recall tradeoff (see the package doc's
// "wrong noun attribution" blind spot, which this narrowing does not
// close, only shrinks).
var countsDriftAnchorWords = []string{"taxonomy", "enumeration", "set"}

// countsDriftAnchored reports whether the text immediately following one
// noun match (rest is the line's content starting right after the
// matched noun, e.g. " taxonomy in pkg/cascade" or ", per-domain
// export/import") states a genuine total. Two shapes are recognized: the
// noun is immediately followed (after optional whitespace) by one of
// countsDriftAnchorWords, or noun is "domain", plural, and immediately
// followed by a comma (the shape CHANGELOG.md uses for its storage
// summary line: a spelled-out plural count followed straight by a comma
// and a description, never by an anchor WORD). That comma shape applies
// to "domain" only: a plain English list ("three kinds, two actors, two
// verdicts") reads identically for "kind" and is common prose that
// states nothing about the frozen taxonomy.
func countsDriftAnchored(noun string, rest string, plural bool) bool {
	trimmed := strings.TrimLeft(rest, " \t")
	for _, w := range countsDriftAnchorWords {
		if strings.HasPrefix(strings.ToLower(trimmed), w) {
			return true
		}
	}
	return noun == "domain" && plural && strings.HasPrefix(rest, ",")
}

// countsDriftScannedExt is the set of file extensions this gate reads as
// prose. Anything else (binaries, and text formats not listed) is skipped
// entirely — a named blind spot, not a silent one (see package doc).
var countsDriftScannedExt = map[string]bool{
	".go": true, ".md": true, ".txt": true, ".yml": true, ".yaml": true,
}

// CountsDriftSkipsPath reports whether rel is fixture data this gate must
// not scan against the live tree — identical rationale to sweep.go's
// SweepSkipsPath: seeded-violation fixtures exist to CONTAIN a wrong
// number, so scanning them in the real-tree check would make the gate
// permanently red on its own test data.
func CountsDriftSkipsPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}

// CountsDriftExemptions names tracked lines this gate would otherwise flag
// but which are correct as written, for one of two reasons. First: the
// line states a total for a DIFFERENT, unrelated enum that happens to
// share the word "kind" and this repo's own "N-kind enumeration" phrasing
// with pkg/cascade's frozen taxonomy — the "wrong noun attribution" blind
// spot the package doc names. Second: the line is a historical release
// record (a CHANGELOG.md or docs/releases entry) stating the count that
// was true AT THE TIME that build was cut, before a later ruling amended
// the live enumeration — rewriting a shipped release's own notes to match
// today's count would make the record claim the release contained
// something it did not, which is worse than a gate false positive. Each
// entry is a file:line key with a reason, checked live by
// TestCountsDriftGate_ExemptionsAreLive so a stale one (the line moved, or
// no longer matches) is caught rather than silently widening.
var CountsDriftExemptions = map[string]string{
	"internal/fleet/journal/journal.go:92": "states fleet/journal.Kind's own closed eight-member enumeration, not pkg/cascade.Kind",
	"CHANGELOG.md:34":                      "alpha-1 release record: the storage domain set genuinely had eleven members when alpha-1 was cut, before R-16.75 added a twelfth (ci_results) — rewriting this to twelve would falsify what alpha-1 actually shipped",
	"docs/releases/alpha1.md:25":           "alpha-1 release record: the storage domain set genuinely had eleven members when alpha-1 was cut, before R-16.75 added a twelfth (ci_results) — rewriting this to twelve would falsify what alpha-1 actually shipped",
}

// CheckCountsDrift scans every path in trackedFiles (repo-relative, under
// root) that CountsDriftSkipsPath and the extension allowlist do not
// exclude, and returns every line whose stated number disagrees with
// CountsDriftNounCounts. A tracked file that no longer exists on disk is
// skipped, not a hard error: several agents edit this tree concurrently
// and an uncommitted working-tree deletion of a still-index-tracked path
// is routine churn, not this gate's failure to report (internal/inventory's
// tree.go makes the identical call, for the identical reason).
func CheckCountsDrift(root string, trackedFiles []string) ([]CountDriftViolation, error) {
	nounCounts := CountsDriftNounCounts()
	var out []CountDriftViolation
	for _, rel := range trackedFiles {
		if CountsDriftSkipsPath(rel) {
			continue
		}
		if !countsDriftScannedExt[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("counts drift: read %s: %w", rel, err)
		}
		for _, v := range countsDriftScanContent(nounCounts, rel, data) {
			key := fmt.Sprintf("%s:%d", v.File, v.Line)
			if _, exempt := CountsDriftExemptions[key]; exempt {
				continue
			}
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// countsDriftScanContent scans one file's already-read content line by
// line and returns every disagreeing match.
func countsDriftScanContent(nounCounts map[string]int, file string, content []byte) []CountDriftViolation {
	var out []CountDriftViolation
	for i, line := range strings.Split(string(content), "\n") {
		for _, m := range countsDriftPattern.FindAllStringSubmatchIndex(line, -1) {
			// A number token immediately preceded by "." is a decimal
			// fragment of something else entirely (a ruling id like
			// "R-14.5", read here as "5 domain") rather than a standalone
			// stated count — reject it before it ever reaches the anchor
			// or noun-count check.
			if m[2] > 0 && line[m[2]-1] == '.' {
				continue
			}
			numTok := line[m[2]:m[3]]
			noun := strings.ToLower(line[m[4]:m[5]])
			plural := m[6] != -1
			if !countsDriftAnchored(noun, line[m[1]:], plural) {
				continue
			}
			derived, known := nounCounts[noun]
			if !known {
				continue
			}
			stated, ok := countsDriftParseNumber(numTok)
			if !ok {
				continue
			}
			if stated != derived {
				out = append(out, CountDriftViolation{
					File: file, Line: i + 1, Noun: noun,
					Stated: stated, Derived: derived, Text: strings.TrimSpace(line),
				})
			}
		}
	}
	return out
}

// countsDriftParseNumber parses tok as a digit string or a known English
// number word, reporting ok=false for anything else (a word this gate does
// not recognize is skipped, never treated as zero).
func countsDriftParseNumber(tok string) (int, bool) {
	if n, err := strconv.Atoi(tok); err == nil {
		return n, true
	}
	if n, known := countsDriftWordToNumber[strings.ToLower(tok)]; known {
		return n, true
	}
	return 0, false
}
