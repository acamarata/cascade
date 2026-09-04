// Purpose: `cascade memory`'s rendering half — the human tables for
//
//	remember/recall/forget/list, the --json payload shapes (the same
//	structs, so the two views cannot drift apart), and scrubDiagnostic,
//	the one filter every error leaving this command passes through.
//
// Inputs: decoded memory.* RPC results; error values from the store, the
//
//	SDK client or config resolution.
//
// Outputs: fmt.Stringer views for internal/output.Writer.Result, and
//
//	scrubbed taxonomy errors.
//
// Constraints: never writes to os.Stdout/os.Stderr (internal/output's
//
//	contract). A record's own body IS printed verbatim — that is what
//	recall is for — but no DIAGNOSTIC may carry a machine path or a
//	secret-shaped value out of this process.
//
// SPORT: cmd.cascade.cmd.memory (ADD, per T-3 sport_updates).
package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memorySummaryWidth caps the summary column so one long record cannot
// wrap a whole terminal into unreadability.
const memorySummaryWidth = 60

// rememberView renders memory.remember's result. The embedding is
// anonymous so the --json payload is RememberResult's own shape.
type rememberView struct {
	memory.RememberResult
}

// String renders the written record's canonical address, which is the one
// thing a caller needs in order to address it again.
func (v rememberView) String() string { return v.ID }

// forgetView renders memory.forget's result.
type forgetView struct {
	memory.ForgetResult
}

// String says exactly what the call did and what it left behind. A
// destructive verb that printed nothing, or printed the same thing for a
// rehearsal as for the real deletion, would leave a user unable to tell
// which one just happened.
func (v forgetView) String() string {
	if v.DryRun {
		return fmt.Sprintf("would forget %s (dry run: nothing was removed)", v.ID)
	}
	return fmt.Sprintf("forgot %s (tombstoned; no other record was touched)", v.ID)
}

// unitsView renders a set of records for recall and list. Both verbs
// return records, so both render through one view rather than two that
// could drift; NextCursor is simply absent for recall.
type unitsView struct {
	Units      []memory.MemoryEntry       `json:"units"`
	Unreadable []memory.ProjectionFailure `json:"unreadable,omitempty"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

// String renders the records as a table, then names every record that
// could not be read. The unreadable rows are shown rather than dropped: a
// listing that silently omits a damaged record reports a smaller store
// than the one on disk.
func (v unitsView) String() string {
	var buf bytes.Buffer
	if len(v.Units) == 0 {
		buf.WriteString("no records")
	} else {
		tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
		// bytes.Buffer never fails a write, so tabwriter's writes through
		// it never do either; errcheck still wants the result handled.
		_, _ = fmt.Fprintf(tw, "ADDRESS\tKIND\tSUMMARY\n")
		for _, u := range v.Units {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n",
				memory.Address(u.Kind, u.Name), u.Kind, summarize(u))
		}
		_ = tw.Flush()
	}
	for _, f := range v.Unreadable {
		_, _ = fmt.Fprintf(&buf, "\nunreadable: %s: %s", f.ID, scrubText(f.Reason))
	}
	if v.NextCursor != "" {
		_, _ = fmt.Fprintf(&buf, "\nnext cursor: %s", v.NextCursor)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// summarize reduces a record to one printable line: its description when
// it has one, otherwise the first line of its body. Control characters are
// dropped, because a record's body is arbitrary text a user pasted and a
// terminal escape sequence in it must not become a terminal escape
// sequence on someone's screen.
func summarize(e memory.MemoryEntry) string {
	text := e.Description
	if strings.TrimSpace(text) == "" {
		text = e.Body
	}
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(stripControl(line))
	if len([]rune(line)) > memorySummaryWidth {
		return string([]rune(line)[:memorySummaryWidth]) + "..."
	}
	return line
}

// stripControl removes every control character, the tab included: a tab
// inside a tabwriter cell would break the column alignment the table
// depends on, and an escape sequence pasted into a record must never
// become an escape sequence on someone's terminal.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// absolutePathPattern matches a token that begins an absolute filesystem
// path, on either a POSIX or a Windows machine. The leading group anchors
// the match to a token boundary (Go's regexp has no lookbehind), so a
// canonical record address like "project/note" — whose slash is preceded
// by a letter — is never mistaken for a path and mangled.
var absolutePathPattern = regexp.MustCompile(`(^|[\s"'=:(\[])((?:/|[A-Za-z]:[\\/])[^\s"'\]]*)`)

// scrubText removes machine paths and secret-shaped values from a
// diagnostic string.
//
// Paths go first, and for their own reason: an error from the store names
// the file it failed on, and that file lives under the operator's home
// directory. A support paste of that message would carry the operator's
// username and directory layout with it. The record's own content is a
// different matter and is NOT scrubbed anywhere in this file — printing
// what a user asked to recall is the whole point of the command — but a
// diagnostic is not content, and it is the one thing here that gets
// copied into a bug report.
func scrubText(text string) string {
	withoutPaths := absolutePathPattern.ReplaceAllString(text, "${1}[PATH-REDACTED]")
	return doctor.RedactText(withoutPaths)
}

// scrubDiagnostic rebuilds err with a scrubbed message, preserving its
// taxonomy Kind so the process exit code is unchanged.
//
// It builds a NEW error rather than wrapping: cascade.Wrapf's Error()
// includes the wrapped error's own text, which would put the unscrubbed
// message straight back into the output. That deliberately terminates the
// errors.Is chain, which is acceptable exactly here and nowhere earlier —
// this is the CLI boundary, the last place the error is inspected before
// it becomes a line on a terminal.
func scrubDiagnostic(err error) error {
	if err == nil {
		return nil
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindInternal
	}
	return cascade.New(kind, scrubText(err.Error()))
}
