// Package output is the CLI output-contract package every cascade command
// must go through (D/S-06.T5, R-14.7). Owns TTY/color detection, the
// stdout=data / stderr=diagnostics stream split, non-TTY progress
// suppression, and the Writer seam that keeps command code from ever
// touching os.Stdout/os.Stderr directly (enforced separately by the
// .golangci.yml forbidigo rule this ticket adds).
//
// Purpose: this file (output.go) holds Mode, Writer, and construction —
//
//	see envelope.go for the --json envelope/NDJSON wire types and
//	exitcodes.go for the process exit-code seam (split across files per
//	Art.10.3's 300-line cap, R-14.117).
//
// Inputs: the resolved --json/-q/-v/--no-color flag values (read by the
//
//	composition root, cmd/cascade/main.go, and passed in — this package
//	never parses flags itself) plus the NO_COLOR/TERM environment and the
//	real stdout/stderr file descriptors.
//
// Outputs: a *Writer bound to a Mode; production code gets one from
//
//	NewDefault, tests build one from New over in-memory buffers.
//
// Constraints: no bare time.Now/rand (Art.7.3 — this package needs neither);
//
//	internal/output must not import cmd/** (cmd is the composition root and
//	depends inward, never the reverse); <=300 lines/file, <=50 lines/func
//	(Art.10.3) — see envelope.go and exitcodes.go for the split.
//
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).
package output

import (
	"fmt"
	"io"
	"os"
)

// Mode is the resolved, immutable output configuration for a Writer. It is
// computed once at construction (New/NewDefault) from the caller's flag
// values plus the environment, never re-read per call — so a single Writer
// behaves consistently for the lifetime of one command invocation.
type Mode struct {
	// JSON requests the versioned --json envelope contract (envelope.go)
	// instead of human-readable text.
	JSON bool
	// Quiet suppresses Progress output only (07-CLI-COMMAND-TREE global
	// flags; root.go's GlobalFlags.Quiet: "suppresses progress output").
	// It does not suppress Warn, Debug, or Fail.
	Quiet bool
	// Verbose gates Debug output. Mutually exclusive with Quiet at the
	// cobra layer (root.go's PersistentPreRunE); Mode does not re-enforce
	// that here, it only records the resolved value.
	Verbose bool
	// NoColor is the fully-resolved color decision: true when colored
	// output must be suppressed, considering (in precedence order) the
	// --no-color flag, the NO_COLOR env var's presence, TERM=dumb, and
	// whether stdout is attached to a terminal at all. See noColorResolved.
	NoColor bool
}

// Writer is the single seam through which every cascade command writes
// process output. It owns the real stdout/stderr streams (or, in tests,
// substitute buffers) and enforces the stdout=data / stderr=diagnostics
// split so command authors never need to reason about which stream a given
// call belongs on.
type Writer struct {
	stdout io.Writer
	stderr io.Writer
	mode   Mode
	tty    bool // stdout TTY-ness, resolved once at construction
}

// New constructs a Writer bound to the given stdout/stderr streams, with its
// Mode resolved from the caller-supplied flag values plus the process
// environment (NO_COLOR, TERM). Tests pass *bytes.Buffer (or any io.Writer)
// for stdout/stderr, in which case tty is always false — matching the
// documented default that a non-*os.File destination is treated as
// non-interactive (IsTerminal).
func New(stdout, stderr io.Writer, jsonMode, quiet, verbose, noColorFlag bool) *Writer {
	tty := IsTerminal(stdout)
	_, noColorEnvSet := os.LookupEnv("NO_COLOR")
	mode := Mode{
		JSON:    jsonMode,
		Quiet:   quiet,
		Verbose: verbose,
		NoColor: noColorResolved(noColorFlag, noColorEnvSet, os.Getenv("TERM"), tty),
	}
	return &Writer{stdout: stdout, stderr: stderr, mode: mode, tty: tty}
}

// NewDefault constructs a Writer bound to the real process os.Stdout and
// os.Stderr. Within cmd/**, it is the ONLY place allowed to reference those
// two identifiers outside a _test.go file: the .golangci.yml forbidigo rule
// added by this ticket is scoped to cmd/** only (a repo-wide ban also
// caught plugins/examples' standalone example plugin, which legitimately
// prints on its own account — see the rule's own comment in .golangci.yml),
// so every cmd/** command reaches the real streams by holding a Writer
// built here rather than naming os.Stdout/os.Stderr itself. Nothing today
// stops an internal/** package outside this one from writing to stdout
// directly; R-14.137 tracks closing that gap with an AST gate that makes
// "only NewDefault touches the real streams" a whole-program property
// instead of a cmd/**-scoped one.
func NewDefault(jsonMode, quiet, verbose, noColorFlag bool) *Writer {
	return New(os.Stdout, os.Stderr, jsonMode, quiet, verbose, noColorFlag)
}

// noColorResolved computes the final color decision from the four
// precedence-ordered inputs documented on Mode.NoColor. Highest-precedence
// match wins; each is independently sufficient to disable color.
func noColorResolved(flag bool, noColorEnvSet bool, term string, isTTY bool) bool {
	switch {
	case flag:
		return true
	case noColorEnvSet:
		return true
	case term == "dumb":
		return true
	case !isTTY:
		return true
	default:
		return false
	}
}

// IsTerminalFile reports whether f is attached to a terminal, using the
// stdlib-only os.ModeCharDevice check (no cgo, no platform-specific ioctl,
// so it builds identically across the GOOS matrix — darwin/linux/windows).
// A nil f, or any Stat error (including a closed file descriptor), is
// treated as "not a terminal" rather than propagating an error: TTY
// detection is advisory (it only ever suppresses progress/color, never
// blocks an operation), so the safe default on any uncertainty is the
// non-interactive behavior.
func IsTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// IsTerminal reports whether w is a terminal. Only *os.File values can ever
// be terminals; any other io.Writer (a *bytes.Buffer in tests, a pipe, an
// os.Pipe write-end used as a plain io.Writer) is treated as non-interactive
// by construction, which is what lets tests exercise the non-TTY code paths
// deterministically without needing a real pty.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return IsTerminalFile(f)
}

// Mode returns the Writer's resolved output mode.
func (w *Writer) Mode() Mode { return w.mode }

// IsTTY reports whether the Writer's stdout was a terminal at construction
// time.
func (w *Writer) IsTTY() bool { return w.tty }

// Progress writes a progress line (spinner tick, counter update) to stderr.
// It is suppressed when Quiet is set OR stdout is not a terminal — a
// progress indicator has no reader once the process is non-interactive
// (piped, redirected, or run under CI), per the ticket's non-TTY
// suppression requirement. Errors are intentionally not surfaced: progress
// output is best-effort diagnostic chatter, never load-bearing.
func (w *Writer) Progress(format string, args ...any) {
	if w.mode.Quiet || !w.tty {
		return
	}
	fmt.Fprintf(w.stderr, format+"\n", args...) //nolint:errcheck // best-effort diagnostic
}

// Warn writes a warning line to stderr. Unlike Progress, Warn is never
// suppressed by Quiet or non-TTY — Quiet's documented scope is progress
// output only (see Mode.Quiet), and a warning that silently vanished under
// --quiet or when piped would defeat the point of a warning.
func (w *Writer) Warn(format string, args ...any) {
	fmt.Fprintf(w.stderr, "warning: "+format+"\n", args...) //nolint:errcheck // best-effort diagnostic
}

// Debug writes a diagnostic line to stderr, gated on Verbose. It is the
// non-interactive equivalent of increased log verbosity (automation parity,
// 08-INIT-CONFIG-SPEC §3): scripts get the same detail via -v regardless of
// TTY-ness.
func (w *Writer) Debug(format string, args ...any) {
	if !w.mode.Verbose {
		return
	}
	fmt.Fprintf(w.stderr, "debug: "+format+"\n", args...) //nolint:errcheck // best-effort diagnostic
}

// Println writes a human-mode data line to stdout. Command authors call
// this only when Mode().JSON is false; JSON-mode output goes through
// Result/Envelope instead, never through Println, so the two output shapes
// never interleave on stdout.
func (w *Writer) Println(a ...any) {
	fmt.Fprintln(w.stdout, a...) //nolint:errcheck // see Result for the error-surfacing path
}

// Result emits data as the command's final data output: in JSON mode, the
// versioned OK envelope (envelope.go); in human mode, data's fmt.Stringer
// form (or fmt's default verb if it doesn't implement one) via Println. The
// returned error is a taxonomy error (pkg/cascade) when the underlying
// stdout write fails — the one place in this method a failure is not
// best-effort, since a data command whose result never reached the caller
// is a real failure, not diagnostic noise.
func (w *Writer) Result(data any) error {
	if w.mode.JSON {
		return w.writeEnvelope(NewOKEnvelope(data))
	}
	w.Println(data)
	return nil
}

// Fail emits err as the command's final (failing) output: in JSON mode, the
// versioned error envelope on stdout (the envelope IS the data output,
// success or failure — scripts parsing --json must see errors on the same
// stream as results); in human mode, a single diagnostic line on stderr.
// Fail is never suppressed by Quiet (errors must always be visible) and
// does not itself compute the process exit status — see ExitCode
// (exitcodes.go) for that, called separately by the composition root.
//
// In JSON mode, a failed envelope write is not silently dropped: unlike
// Result (which surfaces the write error to its caller, since a data
// command whose result never arrived is a real failure), Fail's own
// signature is void — it is called from main's unconditional error path,
// which has no further error-handling step of its own. If the stdout
// envelope write fails, Fail falls back to a best-effort plain-text line on
// stderr so the operator gets SOME signal rather than a process that exited
// non-zero with no visible explanation at all.
func (w *Writer) Fail(err error) {
	if err == nil {
		return
	}
	if w.mode.JSON {
		if writeErr := w.writeEnvelope(NewErrEnvelope(err)); writeErr != nil {
			fmt.Fprintf(w.stderr, "error: %v (also failed to write --json envelope: %v)\n", err, writeErr) //nolint:errcheck // best-effort fallback of last resort
		}
		return
	}
	fmt.Fprintf(w.stderr, "error: %v\n", err) //nolint:errcheck // best-effort diagnostic
}

// writeEnvelope renders env and writes it to stdout, wrapping any encode or
// write failure as a taxonomy error.
func (w *Writer) writeEnvelope(env Envelope) error {
	line, err := env.MarshalLine()
	if err != nil {
		return err
	}
	if _, err := w.stdout.Write(line); err != nil {
		return wrapWriteError(err)
	}
	return nil
}
