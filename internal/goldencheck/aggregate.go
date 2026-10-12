package goldencheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RowResult is the outcome of one lane row, counted from its test2json
// events. A row is OK only when tests ran, at least one passed, none failed,
// no run is left unfinished, the package did not fail, and every test the
// row's regex names ran.
type RowResult struct {
	Row
	Run         int      `json:"run"`
	Passed      int      `json:"passed"`
	Failed      int      `json:"failed"`
	Skipped     int      `json:"skipped"`
	FailedTests []string `json:"failed_tests,omitempty"`
	OK          bool     `json:"ok"`
	Reason      string   `json:"reason,omitempty"`
}

// Report is the aggregate of every row, written as JSON and as a text
// summary. OK is true only when every row is OK and there is at least one.
type Report struct {
	Rows []RowResult `json:"rows"`
	OK   bool        `json:"ok"`
}

// event is the subset of a test2json record the aggregator reads.
type event struct {
	Action string
	Test   string
}

// Aggregate scores every row from its `go test -json` output, keyed by
// Row.Root. A row with no captured output, no run events, or malformed
// output fails; it never aborts the others, so one report lists every
// failure.
func Aggregate(rows []Row, events map[string][]byte) Report {
	report := Report{Rows: make([]RowResult, 0, len(rows)), OK: len(rows) > 0}
	for _, row := range rows {
		res := scoreRow(row, events[row.Root])
		if !res.OK {
			report.OK = false
		}
		report.Rows = append(report.Rows, res)
	}
	return report
}

// scoreRow turns one row's raw event stream into a RowResult.
func scoreRow(row Row, raw []byte) RowResult {
	res := RowResult{Row: row}
	if len(bytes.TrimSpace(raw)) == 0 {
		res.Reason = "no go test -json output captured"
		return res
	}
	ran := map[string]bool{}
	packageFailed := false
	for i, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			res.Reason = fmt.Sprintf("malformed go test -json output at line %d", i+1)
			return res
		}
		packageFailed = tally(&res, ran, ev) || packageFailed
	}
	sort.Strings(res.FailedTests)
	res.Reason = verdict(res, ran, packageFailed)
	res.OK = res.Reason == ""
	return res
}

// tally counts one event and reports whether it is a package-level failure.
func tally(res *RowResult, ran map[string]bool, ev event) bool {
	if ev.Test == "" {
		return ev.Action == "fail"
	}
	switch ev.Action {
	case "run":
		res.Run++
		ran[ev.Test] = true
	case "pass":
		res.Passed++
	case "skip":
		res.Skipped++
	case "fail":
		res.Failed++
		res.FailedTests = append(res.FailedTests, ev.Test)
	}
	return false
}

// verdict returns the first reason a row fails, or "" when it passes.
func verdict(res RowResult, ran map[string]bool, packageFailed bool) string {
	switch {
	case res.Run == 0:
		return "zero tests ran (no run events)"
	case res.Failed > 0:
		return fmt.Sprintf("%d failed: %s", res.Failed, strings.Join(res.FailedTests, ", "))
	case packageFailed:
		return "package failed (build error, panic or timeout)"
	case res.Run != res.Passed+res.Skipped:
		return fmt.Sprintf("%d tests started but did not finish", res.Run-res.Passed-res.Skipped)
	case res.Passed == 0:
		return "no test passed (all skipped)"
	}
	if missing := missingNamed(res.RunRegex, ran); len(missing) > 0 {
		return "named tests did not run: " + strings.Join(missing, ", ")
	}
	return ""
}

var namedRegex = regexp.MustCompile(`^\^\(?([A-Za-z0-9_|]+)\)?\$$`)

// missingNamed lists the tests an exact-name regex (^Name$ or ^(A|B)$)
// selects that produced no run event, so a renamed owner test cannot drop out
// of a row that still passes on its siblings. Other regex shapes are not
// checked by name.
func missingNamed(runRegex string, ran map[string]bool) []string {
	m := namedRegex.FindStringSubmatch(runRegex)
	if m == nil {
		return nil
	}
	var missing []string
	for _, name := range strings.Split(m[1], "|") {
		if !ran[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// Summary renders the report as plain text, one line per row, with the
// reason on each failing row.
func (r Report) Summary() string {
	var b strings.Builder
	for _, row := range r.Rows {
		status := "ok  "
		if !row.OK {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "%s %s [%s] run=%d pass=%d fail=%d skip=%d", status, row.Root, row.Package, row.Run, row.Passed, row.Failed, row.Skipped)
		if row.Reason != "" {
			fmt.Fprintf(&b, " -- %s", row.Reason)
		}
		b.WriteByte('\n')
	}
	state := "OK"
	if !r.OK {
		state = "FAILED"
	}
	fmt.Fprintf(&b, "golden-parity: %s (%d rows)\n", state, len(r.Rows))
	return b.String()
}
