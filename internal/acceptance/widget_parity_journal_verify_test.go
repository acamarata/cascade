package acceptance

// Purpose (this file): the widget parity journal VERIFIER — the code that
//   P1-EXIT-15's condition-11 check runs through the live test. It parses a
//   journal (markdown table rows `| WIDGET/<locator> | <status> | <evidence
//   path> | <rc tag> | <artifact sha256> |`) and is red on every missing,
//   failed, stale or foreign row.
//
// FAIL-CLOSED BY CONSTRUCTION. Nothing a person types is trusted when the
//   verifier can recompute it: the artifact hash is recomputed from the
//   artifact itself, the CDHash from the artifact's own signature, and the
//   fresh-install time from machine-written files. A typed value only has to
//   AGREE with the recomputed one; an empty expectation never matches.
//
// The verifier lives in test files on purpose (no exported symbols, no
//   allow-list row). Evidence-file checks are in
//   widget_parity_journal_evidence_test.go.
// Contract: contract:widget-parity-journal (P1-WID-10).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	parityPassed    = "passed"
	parityFailed    = "failed"
	parityDisclosed = "disclosed-not-passed"
	parityDeferred  = "deferred-P2"

	// parityDeferralCite is what C6P's evidence must cite.
	parityDeferralCite = "DEF-P1-widget-project-rows"
	// parityRowPrefix starts the first cell of every journal row.
	parityRowPrefix = "WIDGET/"
)

// parityLocators is the locator set of the contract, exactly.
var parityLocators = []string{
	"C1", "C2", "C3", "C4", "C4B", "C5", "C6", "C6P", "C7", "C8", "C10", "C11",
	"SB1", "SB2", "SB3", "SB4", "FRESH-INSTALL", "SIGNING",
	"PAR-A", "PAR-CO", "PAR-GP", "PAR-OC", "PAR-ZA",
}

// parityDeferrable are the only locators that may be journaled deferred-P2.
var parityDeferrable = map[string]bool{"PAR-CO": true, "PAR-GP": true, "PAR-OC": true, "PAR-ZA": true}

// parityRow is one parsed journal row.
type parityRow struct{ locator, status, evidence, rc, sha string }

// parityEnv is what the journal is checked against. codesignOf and spctlOf
// recompute the artifact's own signature and Gatekeeper assessment: the live
// test runs codesign and spctl, the seeded tests read recorded probes of the
// seeded artifact. The typed signing records must agree with them.
type parityEnv struct {
	rcTag      string
	artifact   string
	codesignOf func(artifact string) (string, error)
	spctlOf    func(artifact string) (string, error)
}

// parityResult holds every problem found and the three disjoint row lists.
type parityResult struct {
	problems                    []string
	passed, disclosed, deferred []string
}

// parityCells splits one markdown table line into trimmed cells.
func parityCells(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// parseParityRows returns the WIDGET/ rows of a journal and a problem for each
// row that does not have exactly five cells.
func parseParityRows(text string) ([]parityRow, []string) {
	var rows []parityRow
	var problems []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		c := parityCells(line)
		if !strings.HasPrefix(c[0], parityRowPrefix) {
			continue
		}
		if len(c) != 5 {
			problems = append(problems, fmt.Sprintf("%s: malformed row, want 5 cells, got %d", c[0], len(c)))
			continue
		}
		rows = append(rows, parityRow{strings.TrimPrefix(c[0], parityRowPrefix), c[1], c[2], c[3], c[4]})
	}
	return rows, problems
}

// statusProblem reports why status is not allowed for locator ("" when it is).
func statusProblem(loc, status string) string {
	switch status {
	case parityPassed:
		return ""
	case parityDisclosed:
		if loc != "C6P" {
			return "disclosed-not-passed is valid only for C6P"
		}
		return ""
	case parityDeferred:
		if !parityDeferrable[loc] {
			return "deferred-P2 is valid only for PAR-CO, PAR-GP, PAR-OC and PAR-ZA"
		}
		return ""
	case parityFailed:
		return "status failed"
	default:
		return fmt.Sprintf("unknown status %q", status)
	}
}

// resolveParityEvidence maps a journal evidence cell to a real path inside the
// journal's directory: relative, no escape, symlinks resolved, existing.
func resolveParityEvidence(dir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("evidence path is empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("evidence path %q is absolute; it must be relative to the journal directory", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence path %q escapes the journal directory", rel)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(dir, clean))
	if err != nil {
		return "", fmt.Errorf("evidence path %q does not exist", rel)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("journal directory unreadable: %v", err)
	}
	back, err := filepath.Rel(root, resolved)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence path %q resolves outside the journal directory", rel)
	}
	return resolved, nil
}

// readParityText reads an evidence file and refuses a blank one.
func readParityText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("evidence unreadable: %v", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("evidence %s is empty", filepath.Base(path))
	}
	return string(b), nil
}

// checkParityEvidence checks the evidence a row names; SIGNING and
// FRESH-INSTALL name a directory of machine-written files.
func checkParityEvidence(dir string, r parityRow, env parityEnv) []string {
	path, err := resolveParityEvidence(dir, r.evidence)
	if err != nil {
		return []string{err.Error()}
	}
	switch r.locator {
	case "SIGNING":
		return signingProblems(path, env)
	case "FRESH-INSTALL":
		return freshInstallProblems(path)
	}
	text, err := readParityText(path)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	if r.status == parityDeferred {
		out = feedRowProblems(text, strings.TrimPrefix(r.locator, "PAR-"))
	}
	if r.status == parityDisclosed && !strings.Contains(text, parityDeferralCite) {
		out = append(out, "evidence does not cite "+parityDeferralCite)
	}
	return out
}

// checkParityRow returns every problem of one row, each naming the locator.
func checkParityRow(dir string, r parityRow, env parityEnv, artSum string) []string {
	var found []string
	if p := statusProblem(r.locator, r.status); p != "" {
		found = append(found, p)
	}
	if env.rcTag == "" || r.rc != env.rcTag {
		found = append(found, fmt.Sprintf("rc tag %q does not match the rc under test %q", r.rc, env.rcTag))
	}
	if artSum == "" || r.sha != artSum {
		found = append(found, fmt.Sprintf("artifact sha256 %q does not match the artifact's own hash %q", r.sha, artSum))
	}
	found = append(found, checkParityEvidence(dir, r, env)...)
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = parityRowPrefix + r.locator + ": " + f
	}
	return out
}

// add records a checked row: problems, or membership in exactly one list.
func (res *parityResult) add(r parityRow, problems []string) {
	if len(problems) > 0 {
		res.problems = append(res.problems, problems...)
		return
	}
	switch r.status {
	case parityPassed:
		res.passed = append(res.passed, r.locator)
	case parityDisclosed:
		res.disclosed = append(res.disclosed, r.locator)
	case parityDeferred:
		res.deferred = append(res.deferred, r.locator)
	}
}

// claimEvidence refuses evidence that cannot be distinct proof: the journal
// itself, the artifact itself, or a path already cited by another locator.
func claimEvidence(dir string, r parityRow, owner map[string]string, forbid map[string]string) []string {
	p, err := resolveParityEvidence(dir, r.evidence)
	if err != nil {
		return nil
	}
	var out []string
	if what, bad := forbid[p]; bad {
		out = append(out, fmt.Sprintf("%s: evidence %q is the %s itself", parityRowPrefix+r.locator, r.evidence, what))
	}
	if other, dup := owner[p]; dup {
		out = append(out, fmt.Sprintf("%s: evidence %q is already cited by %s%s", parityRowPrefix+r.locator, r.evidence, parityRowPrefix, other))
	}
	owner[p] = r.locator
	return out
}

// verifyParityJournal checks a whole journal against env.
func verifyParityJournal(journalPath string, env parityEnv) parityResult {
	var res parityResult
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		res.problems = append(res.problems, "journal unreadable: "+err.Error())
		return res
	}
	rows, malformed := parseParityRows(string(raw))
	res.problems = append(res.problems, malformed...)
	artSum, err := artifactSHA256(env.artifact)
	if err != nil {
		res.problems = append(res.problems, "artifact: "+err.Error())
	}
	forbid := map[string]string{}
	for what, p := range map[string]string{"journal": journalPath, "artifact": env.artifact} {
		if abs, err := filepath.EvalSymlinks(p); err == nil {
			forbid[abs] = what
		}
	}
	owner := map[string]string{}
	known := map[string]bool{}
	for _, l := range parityLocators {
		known[l] = true
	}
	seen := map[string]bool{}
	for _, r := range rows {
		switch {
		case !known[r.locator]:
			res.problems = append(res.problems, parityRowPrefix+r.locator+": unknown locator")
		case seen[r.locator]:
			res.problems = append(res.problems, parityRowPrefix+r.locator+": duplicate row")
		default:
			seen[r.locator] = true
			dir := filepath.Dir(journalPath)
			res.add(r, append(checkParityRow(dir, r, env, artSum), claimEvidence(dir, r, owner, forbid)...))
		}
	}
	for _, l := range parityLocators {
		if !seen[l] {
			res.problems = append(res.problems, parityRowPrefix+l+": row missing")
		}
	}
	return res
}
