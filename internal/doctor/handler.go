package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Purpose: the `cascade doctor` / `cascade doctor bundle` render layer —
//
//	TTY coloured table, non-TTY plain text, and the --json versioned
//	envelope (ticket contract task 4).
//
// CASCADE-ALLOW: P1-E03-W1-S05-T2 — these handlers are the real, fully
// implemented rendering capability behind `cascade doctor`/`cascade
// doctor bundle`; only the cobra command mounting is deferred, because
// the cobra root does not exist in this tree yet (D/S-06.T1 owns it;
// 06-FORGE-SPEC §5.19 allowed-fail forward-stub pattern — same shape as
// internal/runtime/config_handlers.go's CASCADE-ALLOW). CONTRACT NOTE:
// this ticket's task list additionally asks for "a forward-stub
// cobra.Command"; the module has no cobra dependency (go.mod is outside
// this ticket's files_scope and the contract elsewhere forbids adding
// one), and the S-04.T1 precedent this same sentence cites did not
// construct a cobra.Command value either — it shipped plain handler
// functions behind a CASCADE-ALLOW comment, exactly this file's shape.
// Followed the precedent over the literal "cobra.Command" wording; see
// this ticket's final report CONTRACT-DEVIATIONS line.
//
// Inputs: a RunReport (runner.go) plus render mode selection.
// Outputs: rendered bytes on the given io.Writer, or an EnvelopeVersion=1
//
//	{version, data} JSON object for --json (internal/output's own
//	Envelope type is D/S-06.T5's; this ticket predates that call site
//	per the contract's "D/S-06.T5 will migrate this call site" note, so
//	handler.go defines its own minimal, forward-compatible {version,
//	data} shape rather than importing internal/output and creating a
//	premature coupling this ticket does not own).
//
// SPORT: placeholder: doctor/framework (ADD).

// jsonEnvelopeVersion is this ticket's --json wire version, matching
// D/S-06.T5's {version, data} shape (string "1", not an int, since this
// ticket's own JSON contract is intentionally minimal pending the real
// migration).
const jsonEnvelopeVersion = "1"

// jsonEnvelope is the --json output shape.
type jsonEnvelope struct {
	Version string    `json:"version"`
	Data    RunReport `json:"data"`
}

// RenderJSON writes report as the versioned {version, data} JSON
// envelope.
func RenderJSON(w io.Writer, report RunReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonEnvelope{Version: jsonEnvelopeVersion, Data: report})
}

// statusGlyph is the single-character status marker shared by both text
// renderers.
func statusGlyph(s Status) string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	case StatusError:
		return "ERROR"
	default:
		return "?"
	}
}

// ansiColor maps a Status to its TTY color code (green/yellow/red);
// StatusOK/StatusWarn/StatusError are exhaustively listed (exhaustive
// linter, default-signifies-exhaustive: false).
func ansiColor(s Status) string {
	switch s {
	case StatusOK:
		return "\x1b[32m"
	case StatusWarn:
		return "\x1b[33m"
	case StatusError:
		return "\x1b[31m"
	default:
		return ""
	}
}

const ansiReset = "\x1b[0m"

// sortedEntries returns report.Entries sorted by Name (RunReport already
// sorts, but rendering must not assume it — a hand-built RunReport in a
// test should still render deterministically).
func sortedEntries(report RunReport) []ReportEntry {
	out := append([]ReportEntry(nil), report.Entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RenderTTY writes report as a coloured summary table.
func RenderTTY(w io.Writer, report RunReport) error {
	for _, e := range sortedEntries(report) {
		color := ansiColor(e.Result.Status)
		if _, err := fmt.Fprintf(w, "%s%-6s%s %-30s %s\n", color, statusGlyph(e.Result.Status), ansiReset, e.Name, e.Result.Message); err != nil {
			return err
		}
	}
	return nil
}

// RenderPlain writes report as ANSI-free plain text (non-TTY mode).
func RenderPlain(w io.Writer, report RunReport) error {
	for _, e := range sortedEntries(report) {
		if _, err := fmt.Fprintf(w, "%-6s %-30s %s\n", statusGlyph(e.Result.Status), e.Name, e.Result.Message); err != nil {
			return err
		}
	}
	return nil
}

// UseColor resolves the NO_COLOR / --no-color convention: noColorFlag
// (the CLI flag) and a non-empty NO_COLOR env value both disable color
// regardless of TTY-ness; otherwise color is enabled only when out is a
// real character-device terminal.
func UseColor(out *os.File, noColorEnv string, noColorFlag bool) bool {
	if noColorFlag || noColorEnv != "" {
		return false
	}
	info, err := out.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// Render dispatches to RenderJSON/RenderTTY/RenderPlain per the flags a
// future cobra command (D/S-06.T1) will parse.
func Render(w io.Writer, report RunReport, jsonOut, tty bool) error {
	switch {
	case jsonOut:
		return RenderJSON(w, report)
	case tty:
		return RenderTTY(w, report)
	default:
		return RenderPlain(w, report)
	}
}

// DefaultOutcomeExitCode is a placeholder mapping used only by this
// package's own tests to prove the three outcomes resolve to three
// distinct codes; it fixes no literal value the contract binds (AC:
// "no literal code values are fixed by this ticket"). D/S-06.T5 owns
// the real A-T7-taxonomy-derived exit-code table and supplies its own
// Outcome->code function at the CLI boundary.
func DefaultOutcomeExitCode(o Outcome) int {
	switch o {
	case OutcomeOK:
		return 0
	case OutcomeWarn:
		return 1
	case OutcomeError:
		return 2
	default:
		return 2
	}
}
