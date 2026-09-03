package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func sampleReport() RunReport {
	return RunReport{Entries: []ReportEntry{
		{Name: "b", Result: CheckResult{Status: StatusWarn, Message: "warn msg"}},
		{Name: "a", Result: CheckResult{Status: StatusOK, Message: "ok msg"}},
	}}
}

func TestRenderJSON_VersionedEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, sampleReport()); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var env struct {
		Version string    `json:"version"`
		Data    RunReport `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v, raw=%s", err, buf.String())
	}
	if env.Version != "1" {
		t.Fatalf("got version=%q, want %q", env.Version, "1")
	}
	if len(env.Data.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(env.Data.Entries))
	}
}

func TestRenderTTY_ContainsColorCodes(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderTTY(&buf, sampleReport()); err != nil {
		t.Fatalf("RenderTTY: %v", err)
	}
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("TTY render has no ANSI codes: %q", buf.String())
	}
}

func TestRenderPlain_NoANSICodes(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderPlain(&buf, sampleReport()); err != nil {
		t.Fatalf("RenderPlain: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("plain render must contain no ANSI codes: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "warn msg") || !strings.Contains(buf.String(), "ok msg") {
		t.Fatalf("plain render missing check messages: %q", buf.String())
	}
}

func TestRender_SortedByName(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderPlain(&buf, sampleReport()); err != nil {
		t.Fatalf("RenderPlain: %v", err)
	}
	out := buf.String()
	if strings.Index(out, "ok msg") > strings.Index(out, "warn msg") {
		t.Fatalf("entries not rendered in Name-sorted order: %q", out)
	}
}

func TestRender_Dispatch(t *testing.T) {
	var jsonBuf, ttyBuf, plainBuf bytes.Buffer
	if err := Render(&jsonBuf, sampleReport(), true, false); err != nil {
		t.Fatalf("Render json: %v", err)
	}
	if !json.Valid(jsonBuf.Bytes()) {
		t.Fatalf("Render(json=true) did not produce valid JSON: %s", jsonBuf.String())
	}
	if err := Render(&ttyBuf, sampleReport(), false, true); err != nil {
		t.Fatalf("Render tty: %v", err)
	}
	if !strings.Contains(ttyBuf.String(), "\x1b[") {
		t.Fatalf("Render(tty=true) missing ANSI codes")
	}
	if err := Render(&plainBuf, sampleReport(), false, false); err != nil {
		t.Fatalf("Render plain: %v", err)
	}
	if strings.Contains(plainBuf.String(), "\x1b[") {
		t.Fatalf("Render(plain) must have no ANSI codes")
	}
}

func TestUseColor_NoColorFlagWins(t *testing.T) {
	if UseColor(os.Stdout, "", true) {
		t.Fatalf("--no-color must disable color regardless of TTY-ness")
	}
}

func TestUseColor_NoColorEnvWins(t *testing.T) {
	if UseColor(os.Stdout, "1", false) {
		t.Fatalf("a set NO_COLOR env var must disable color")
	}
}

func TestUseColor_NonTTYFileDisablesColor(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = f.Close() }()
	if UseColor(f, "", false) {
		t.Fatalf("a plain file is not a character device; color must be disabled")
	}
}

func TestDefaultOutcomeExitCode_ThreeDistinctCodes(t *testing.T) {
	ok := DefaultOutcomeExitCode(OutcomeOK)
	warn := DefaultOutcomeExitCode(OutcomeWarn)
	errC := DefaultOutcomeExitCode(OutcomeError)
	if ok == warn || warn == errC || ok == errC {
		t.Fatalf("outcome exit codes must be three distinct values: ok=%d warn=%d error=%d", ok, warn, errC)
	}
}

func TestDefaultOutcomeExitCode_UnknownOutcomeDefaultsToError(t *testing.T) {
	if got := DefaultOutcomeExitCode(Outcome("bogus")); got != DefaultOutcomeExitCode(OutcomeError) {
		t.Fatalf("got %d, want the same code as OutcomeError for an unrecognized Outcome", got)
	}
}

func TestStatusGlyphAndAnsiColor_UnknownStatus(t *testing.T) {
	if statusGlyph(Status("bogus")) != "?" {
		t.Fatalf("got %q, want \"?\" for an unrecognized Status", statusGlyph(Status("bogus")))
	}
	if ansiColor(Status("bogus")) != "" {
		t.Fatalf("got %q, want empty color code for an unrecognized Status", ansiColor(Status("bogus")))
	}
}

func TestUseColor_StatFailure(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "will-close")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_ = f.Close()
	if UseColor(f, "", false) {
		t.Fatalf("a closed file's Stat must fail, disabling color")
	}
}
