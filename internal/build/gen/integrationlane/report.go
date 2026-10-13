package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/goldencheck"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// event is the subset of a test2json record the identity and skip rules read.
type event struct {
	Action, Test, Package, Output string
}

// reportCmd aggregates <dir>/<n>.json against the plan and writes the report.
func reportCmd(o options, w *output.Writer) error {
	rows := laneRows(o.runner)
	if firstPackage(filepath.Join(o.dir, "1.json")) == build.SeededRedRow.Package {
		rows = []build.LaneRow{build.SeededRedRow}
	}
	gold := make([]goldencheck.Row, len(rows))
	raws := make(map[string][]byte, len(rows))
	for i, r := range rows {
		gold[i] = goldencheck.Row{Root: r.Package, Package: r.Package, RunRegex: r.RunRegex}
		if data, err := os.ReadFile(filepath.Join(o.dir, strconv.Itoa(i+1)+".json")); err == nil {
			raws[r.Package] = data
		}
	}
	rep := goldencheck.Aggregate(gold, raws)
	rep.OK = len(rep.Rows) > 0
	for i := range rep.Rows {
		refine(&rep.Rows[i], raws[rows[i].Package], rows[i], o.runner)
		rep.OK = rep.OK && rep.Rows[i].OK
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = runtime.WriteFileAtomic(o.out, append(data, '\n'), 0o644)
	}
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	w.Println(strings.TrimSuffix(strings.Replace(rep.Summary(), "golden-parity:", "integration-lanes:", 1), "\n"))
	if !rep.OK {
		return fmt.Errorf("%d rows, at least one failed (see the summary above)", len(rep.Rows))
	}
	return nil
}

// firstPackage returns the Package of the first event in a captured stream.
func firstPackage(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, ev := range decode(data) {
		if ev.Package != "" {
			return "./" + strings.TrimPrefix(ev.Package, modulePath+"/")
		}
	}
	return ""
}

// modulePath is the import-path prefix of every package in this module.
const modulePath = "github.com/acamarata/cascade"

// decode parses the well-formed lines of a go test -json stream; Aggregate has
// already failed a row with malformed lines, so a bad line is skipped here.
func decode(raw []byte) []event {
	var out []event
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var ev event
		if json.Unmarshal(bytes.TrimSpace(line), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

// refine applies the two rules Aggregate does not know: the events must belong
// to the planned package, and on a runner with a SkipOn entry (and always on
// windows) every skipped test must carry the declared gate message; a windows
// row with no declared message accepts no skip. An all-skipped row passes only
// when every skip carries its message.
func refine(res *goldencheck.RowResult, raw []byte, row build.LaneRow, runner string) {
	evs := decode(raw)
	want := "/" + strings.TrimPrefix(row.Package, "./")
	for _, ev := range evs {
		if ev.Package != "" && !strings.HasSuffix(ev.Package, want) {
			res.OK, res.Reason = false, fmt.Sprintf("events name package %s, the plan runs %s", ev.Package, row.Package)
			return
		}
	}
	msg := row.SkipOn[runner]
	if msg == "" && runner != build.RunnerWindows {
		return
	}
	skipped, said := map[string]bool{}, map[string]bool{}
	for _, ev := range evs {
		switch {
		case ev.Action == "skip" && ev.Test != "":
			skipped[ev.Test] = true
		case ev.Action == "output" && msg != "" && strings.Contains(ev.Output, msg):
			said[ev.Test] = true
		}
	}
	for t := range skipped {
		if !said[t] {
			res.OK, res.Reason = false, fmt.Sprintf("%s skipped without a declared gate message (declared: %q)", t, msg)
			return
		}
	}
	if !res.OK && strings.Contains(res.Reason, "all skipped") {
		res.OK, res.Reason = true, ""
	}
}

// assertRedCmd checks a red-proof report with build.AssertRedProof.
func assertRedCmd(o options, w *output.Writer) error {
	data, err := os.ReadFile(o.reportFile)
	if err != nil {
		return err
	}
	if err := build.AssertRedProof(data); err != nil {
		return err
	}
	w.Println("integrationlane: red proof confirmed, the seeded row failed through TestSeededRed")
	return nil
}
