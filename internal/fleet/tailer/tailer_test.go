package tailer

// Purpose: unit tests for the rotation-safe follower and versioned
//   parser dispatch: happy-path streaming, rotation mid-stream (rename +
//   reopen), truncation mid-stream, malformed-line and unknown-version
//   ParseError recovery, clean EOF, a removed-file poll, and Close
//   idempotency. The R-40.X16 committed-fixture pointer test lives in
//   redact_test.go alongside the rest of the fixture/canary suite.
// Constraints: every write in this file is rooted at t.TempDir() (Art.7).
// SPORT: fleet/tailer (ADD, per T-2 sport_updates).

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestTailer_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path,
		`{"type":"user","version":"2.1.140"}`,
		`{"type":"assistant","version":"2.1.140"}`,
	)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	for i := 0; i < 2; i++ {
		if _, err := tl.Next(); err != nil {
			t.Fatalf("Next() line %d: %v", i+1, err)
		}
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() at end of content: got %v, want io.EOF", err)
	}
}

func TestTailer_CleanEOFIsNotPermanent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path, `{"type":"user","version":"2.1.140"}`)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() first line: %v", err)
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() at EOF: got %v, want io.EOF", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("append open: %v", err)
	}
	if _, err := f.WriteString(`{"type":"assistant","version":"2.1.140"}` + "\n"); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() after append past EOF: %v", err)
	}
}

func TestTailer_RotationRenameThenReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path, `{"type":"user","version":"2.1.140"}`)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() before rotation: %v", err)
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() before rotation EOF: got %v", err)
	}

	rotated := filepath.Join(dir, "transcript.jsonl.1")
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeLines(t, path, `{"type":"assistant","version":"2.1.140"}`)

	rec, err := tl.Next()
	if err != nil {
		t.Fatalf("Next() after rotation: %v", err)
	}
	if rec.Tool != "" || rec.ExitCode != 0 || rec.ResultHash != "" || rec.Duration != 0 || len(rec.Paths) != 0 {
		t.Fatalf("Next() after rotation: got non-empty record %+v for a line with no allowlisted fields", rec)
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after draining rotated file: got %v, want io.EOF", err)
	}
}

func TestTailer_TruncationMidStream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path,
		`{"type":"user","version":"2.1.140"}`,
		`{"type":"assistant","version":"2.1.140"}`,
	)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() before truncation, line 1: %v", err)
	}
	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() before truncation, line 2: %v", err)
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() before truncation, drained: got %v, want io.EOF", err)
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	writeLines(t, path, `{"type":"system","version":"2.1.261"}`)

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() after truncation: %v", err)
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after draining truncated file: got %v, want io.EOF", err)
	}
}

func TestTailer_MalformedLineThenContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path,
		`{not-json`,
		`{"type":"user","version":"2.1.140"}`,
	)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	_, err = tl.Next()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("Next() on malformed line: got %v, want *ParseError", err)
	}
	if pe.Line != 1 {
		t.Fatalf("ParseError.Line = %d, want 1", pe.Line)
	}

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() after malformed line: %v", err)
	}
}

func TestTailer_UnknownVersionThenContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path,
		`{"type":"user","version":"not-a-version"}`,
		`{"type":"assistant","version":"2.1.140"}`,
	)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	_, err = tl.Next()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("Next() on unknown version: got %v, want *ParseError", err)
	}

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() after unknown-version line: %v", err)
	}
}

func TestTailer_CodexRefusesMidSessionStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	writeLines(t, path, `{"type":"event_msg","payload":{}}`)

	tl, err := NewTailer(path, HarnessCodex, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	_, err = tl.Next()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("Next() on codex mid-session-start: got %v, want *ParseError", err)
	}
}

func TestNewTailer_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := NewTailer(filepath.Join(dir, "missing.jsonl"), HarnessCC, dir)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("NewTailer on missing file: err = %v, want KindNotFound", err)
	}
}

func TestTailer_RemovedFileDuringPoll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path, `{"type":"user","version":"2.1.140"}`)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	if _, err := tl.Next(); err != nil {
		t.Fatalf("Next() line 1: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := tl.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after file removed: got %v, want io.EOF", err)
	}
}

func TestTailer_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeLines(t, path, `{"type":"user","version":"2.1.140"}`)

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	if err := tl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tl.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
