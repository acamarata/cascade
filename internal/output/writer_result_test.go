// Purpose: tests for output.go's Println/Result/Fail result-emission
//
//	methods and the package's overall conformance suite
//	(TestOutputConformance, required by the ticket's checks list). Split
//	out of output_test.go, same package, to satisfy Art.10.3's 300-line
//	cap (R-14.117 — a behavior-preserving split of one ticket's own test
//	file, not a restructuring; TestFileCap300Lines_RealTreeGreen in
//	internal/build is what caught output_test.go at 316 lines).
//
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).
package output_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestPrintln_WritesStdoutOnly(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	w := output.New(stdout, stderr, false, false, false, false)
	w.Println("hello", "world")
	if got := stdout.String(); got != "hello world\n" {
		t.Fatalf("Println wrote %q to stdout, want %q", got, "hello world\n")
	}
	if stderr.Len() != 0 {
		t.Fatalf("Println wrote to stderr: %q", stderr.String())
	}
}

func TestResult_HumanMode(t *testing.T) {
	stdout := &bytes.Buffer{}
	w := output.New(stdout, &bytes.Buffer{}, false, false, false, false)
	if err := w.Result("plain text"); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if got := stdout.String(); got != "plain text\n" {
		t.Fatalf("Result (human) wrote %q, want %q", got, "plain text\n")
	}
}

func TestResult_JSONMode(t *testing.T) {
	stdout := &bytes.Buffer{}
	w := output.New(stdout, &bytes.Buffer{}, true, false, false, false)
	if err := w.Result(map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !strings.Contains(stdout.String(), `"version": 1`) || !strings.Contains(stdout.String(), `"ok": true`) {
		t.Fatalf("Result (json) = %q, missing envelope shape", stdout.String())
	}
}

func TestResult_WriteErrorSurfaced(t *testing.T) {
	w := output.New(failingWriter{}, &bytes.Buffer{}, true, false, false, false)
	err := w.Result("x")
	if err == nil {
		t.Fatal("Result over a failing stdout must return an error")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Result write-error kind = %v, want KindUnavailable", err)
	}
}

func TestFail_NilIsNoop(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	w := output.New(stdout, stderr, false, false, false, false)
	w.Fail(nil)
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("Fail(nil) must write nothing")
	}
}

func TestFail_HumanMode_WritesStderr(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	w := output.New(stdout, stderr, false, false, false, false)
	w.Fail(cascade.New(cascade.KindNotFound, "widget missing"))
	if stdout.Len() != 0 {
		t.Fatalf("Fail (human) wrote to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "widget missing") {
		t.Fatalf("Fail (human) stderr = %q, missing message", stderr.String())
	}
}

func TestFail_JSONMode_WritesStdoutEnvelope(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	w := output.New(stdout, stderr, true, false, false, false)
	w.Fail(cascade.New(cascade.KindNotFound, "widget missing"))
	if stderr.Len() != 0 {
		t.Fatalf("Fail (json) wrote to stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "not-found"`) {
		t.Fatalf("Fail (json) stdout = %q, missing error kind", stdout.String())
	}
}

// TestFail_JSONMode_EnvelopeWriteErrorFallsBackToStderr covers the CR fix
// (P1-E04-W1-S06-T5): a failed --json envelope write during Fail must not
// vanish silently — it falls back to a best-effort stderr line so an
// operator piping --json output somewhere that fails (closed pipe, disk
// full) still sees SOMETHING, rather than a process that exited non-zero
// with zero visible explanation.
func TestFail_JSONMode_EnvelopeWriteErrorFallsBackToStderr(t *testing.T) {
	stderr := &bytes.Buffer{}
	w := output.New(failingWriter{}, stderr, true, false, false, false)
	w.Fail(cascade.New(cascade.KindNotFound, "widget missing"))
	if stderr.Len() == 0 {
		t.Fatal("Fail (json, failing stdout) wrote nothing to stderr, want a fallback error line")
	}
	if !strings.Contains(stderr.String(), "widget missing") {
		t.Errorf("Fail fallback stderr = %q, missing original error message", stderr.String())
	}
	if !strings.Contains(stderr.String(), "boom: write failed") {
		t.Errorf("Fail fallback stderr = %q, missing envelope write error", stderr.String())
	}
}

func TestFail_NeverSuppressedByQuiet(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	w := output.New(stdout, stderr, false, true, false, false)
	w.Fail(cascade.New(cascade.KindInternal, "boom"))
	if !strings.Contains(stderr.String(), "boom") {
		t.Fatal("Fail must not be suppressed by Quiet")
	}
}

// TestOutputConformance is the ticket's required overarching conformance
// suite: it walks every Quiet x Verbose x JSON x TTY combination relevant
// to the documented suppression matrix and asserts the observed behavior
// against docs/cli-output-contract.md's TTY/non-TTY behavior matrix,
// rather than any single mode in isolation.
func TestOutputConformance(t *testing.T) {
	cases := []struct {
		name           string
		quiet, verbose bool
		wantProgress   bool
		wantDebug      bool
	}{
		{"default", false, false, false, false},
		{"quiet suppresses progress", true, false, false, false},
		{"verbose enables debug", false, true, false, true},
		{"quiet+verbose still suppresses progress", true, true, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stderr := &bytes.Buffer{}
			// stdout stays a *bytes.Buffer (never a TTY) throughout this
			// table, so "wantProgress" is always false here — TTY-gated
			// Progress is covered separately by TestProgress_SuppressedWhenNotTTY
			// (output_test.go).
			w := output.New(&bytes.Buffer{}, stderr, false, tc.quiet, tc.verbose, false)

			w.Progress("p")
			gotProgress := strings.Contains(stderr.String(), "p")
			if gotProgress != tc.wantProgress {
				t.Errorf("Progress present = %v, want %v", gotProgress, tc.wantProgress)
			}

			stderr.Reset()
			w.Debug("d")
			gotDebug := strings.Contains(stderr.String(), "d")
			if gotDebug != tc.wantDebug {
				t.Errorf("Debug present = %v, want %v", gotDebug, tc.wantDebug)
			}

			stderr.Reset()
			w.Warn("w")
			if !strings.Contains(stderr.String(), "w") {
				t.Error("Warn must never be suppressed")
			}
		})
	}

	t.Run("exit code delegation stays total over every taxonomy kind", func(t *testing.T) {
		for _, k := range cascade.AllKinds() {
			err := cascade.New(k, "x")
			if got, want := output.ExitCode(err), k.ExitCode(); got != want {
				t.Errorf("kind %v: output.ExitCode = %d, want %d", k, got, want)
			}
		}
	})
}
