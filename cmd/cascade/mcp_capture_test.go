package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the capture diagnostic's four properties — it
//   records BOTH directions, it appends rather than truncates, it refuses
//   rather than degrading to a silent pass-through, and it alters no byte
//   that crosses it.
// Constraints: no network, no os.Stdin/os.Stdout — every stream here is a
//   buffer, which is also what the production call site hands it.
// SPORT: cmd/cascade:mcp-capture tests (ADD) — P1-E04-W4-S86-T1.

// TestCaptureRecordsBothDirections is the property the goldens depend on:
// a capture that recorded only what the server said would be a transcript
// of this repo talking to itself, which is exactly the paraphrase failure
// the diagnostic exists to prevent.
func TestCaptureRecordsBothDirections(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	clientSent := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"
	var served bytes.Buffer

	in, out, closer, err := captureStreams(dir, strings.NewReader(clientSent), &served)
	if err != nil {
		t.Fatalf("captureStreams: %v", err)
	}
	read, err := io.ReadAll(in)
	if err != nil {
		t.Fatalf("read the teed input: %v", err)
	}
	serverSent := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"
	if _, err := io.WriteString(out, serverSent); err != nil {
		t.Fatalf("write the teed output: %v", err)
	}
	closer()

	// The diagnostic alters nothing: the reader still yields the client's
	// bytes verbatim and the writer still reaches the real stream.
	if string(read) != clientSent {
		t.Errorf("the teed reader yielded %q, want the client's bytes verbatim", read)
	}
	if served.String() != serverSent {
		t.Errorf("the real output stream got %q, want %q", served.String(), serverSent)
	}
	if got := readCapture(t, dir, "in.jsonl"); got != clientSent {
		t.Errorf("in.jsonl = %q, want the client frame %q", got, clientSent)
	}
	if got := readCapture(t, dir, "out.jsonl"); got != serverSent {
		t.Errorf("out.jsonl = %q, want the server frame %q", got, serverSent)
	}
}

// TestCaptureAppendsAcrossSessions holds the reason openCaptureFile does
// not truncate. A client that reconnects mid-session would otherwise erase
// the handshake that preceded it — the half of the exchange the goldens
// most need, since it is the part this repo got wrong.
func TestCaptureAppendsAcrossSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	for _, frame := range []string{"first\n", "second\n"} {
		in, out, closer, err := captureStreams(dir, strings.NewReader(frame), io.Discard)
		if err != nil {
			t.Fatalf("captureStreams for %q: %v", frame, err)
		}
		if _, err := io.ReadAll(in); err != nil {
			t.Fatalf("drain %q: %v", frame, err)
		}
		if _, err := io.WriteString(out, frame); err != nil {
			t.Fatalf("write %q: %v", frame, err)
		}
		closer()
	}
	for _, name := range []string{"in.jsonl", "out.jsonl"} {
		if got := readCapture(t, dir, name); got != "first\nsecond\n" {
			t.Errorf("%s = %q, want both sessions; the second reconnect truncated the first", name, got)
		}
	}
}

// TestCaptureRefusesRatherThanPassingThrough is the fail-loud rule. An
// operator who asked for frames and got a working server with no files
// would conclude the client sent nothing, which is the wrong conclusion
// about the one question the capture is asked.
func TestCaptureRefusesRatherThanPassingThrough(t *testing.T) {
	// A regular file where the capture directory should go: MkdirAll cannot
	// succeed, on every platform.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, out, closer, err := captureStreams(blocked, strings.NewReader("x"), io.Discard)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	// Every returned value must be nil. A non-nil reader here is the silent
	// pass-through this test exists to forbid: the caller would serve
	// happily and write nothing.
	if in != nil || out != nil || closer != nil {
		t.Error("a refused capture returned usable streams; the server would run with no files")
	}
}

// TestCaptureFilesAreNotWorldReadable covers the mode the frames are
// written under. Captured frames carry whatever the client sent, which can
// include arguments to a tool call.
//
// POSIX-only for the reason internal/elevation's keystore_file_test.go
// states: Windows implements no Unix permission bits, so the check would
// assert the syscall's 0666 fallback rather than the writer's intent.
func TestCaptureFilesAreNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "capture")
	_, _, closer, err := captureStreams(dir, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("captureStreams: %v", err)
	}
	closer()
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("capture dir mode = %v, want 0700", perm)
	}
	for _, name := range []string{"in.jsonl", "out.jsonl"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, perm)
		}
	}
}

// TestOpenCaptureFileNamesTheFileItCouldNotOpen keeps the refusal
// actionable: two files are opened per capture and an error naming neither
// leaves the operator guessing which.
func TestOpenCaptureFileNamesTheFileItCouldNotOpen(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be: O_WRONLY cannot open it.
	if err := os.Mkdir(filepath.Join(dir, "out.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := openCaptureFile(dir, "out.jsonl")
	if f != nil {
		t.Error("a refused open returned a file handle")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "out.jsonl") {
		t.Errorf("err = %q, want it to name out.jsonl", err)
	}
}

// TestCaptureClosesTheFirstFileWhenTheSecondFails holds the leak the
// ordering in captureStreams is written to avoid: in.jsonl is already open
// when out.jsonl is attempted.
func TestCaptureClosesTheFirstFileWhenTheSecondFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	if err := os.MkdirAll(filepath.Join(dir, "out.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, closer, err := captureStreams(dir, strings.NewReader("x"), io.Discard)
	if err == nil {
		t.Fatal("captureStreams succeeded with an unopenable out.jsonl")
	}
	if closer != nil {
		t.Error("a failed capture returned a closer; the caller cannot know to call it")
	}
	// in.jsonl was opened before out.jsonl was attempted. It must have been
	// closed on the way out, which is observable as the descriptor being
	// reusable: reopening and writing must succeed.
	f, err := openCaptureFile(dir, "in.jsonl")
	if err != nil {
		t.Fatalf("reopen in.jsonl: %v", err)
	}
	if _, err := f.WriteString("still writable\n"); err != nil {
		t.Errorf("write to the reopened in.jsonl: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// readCapture reads one capture file's whole contents.
func readCapture(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
