// Purpose: print the tail of each failed test's output when a lane fails, so a
// CI log shows why a row failed and not only the one-line summary.
// Constraints: reads only the captured go test -json streams; bounded output.

package main

import (
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/output"
)

// failTailLines bounds the output printed for one failed test.
const failTailLines = 40

// failedOutput returns, per failed test (sorted), the last failTailLines
// output lines go test captured for it across every stream in raws.
func failedOutput(raws map[string][]byte) map[string][]string {
	lines, failed := map[string][]string{}, map[string]bool{}
	for _, raw := range raws {
		for _, ev := range decode(raw) {
			if ev.Test == "" {
				continue
			}
			switch ev.Action {
			case "output":
				lines[ev.Test] = append(lines[ev.Test], strings.TrimRight(ev.Output, "\n"))
			case "fail":
				failed[ev.Test] = true
			}
		}
	}
	out := map[string][]string{}
	for t := range failed {
		l := lines[t]
		if len(l) > failTailLines {
			l = l[len(l)-failTailLines:]
		}
		out[t] = l
	}
	return out
}

// printFailedOutput writes the tail of every failed test's output to w.
func printFailedOutput(w *output.Writer, raws map[string][]byte) {
	byTest := failedOutput(raws)
	names := make([]string, 0, len(byTest))
	for t := range byTest {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		w.Println("--- output of failed " + t + " (last lines):")
		for _, l := range byTest[t] {
			w.Println("    " + l)
		}
	}
}
