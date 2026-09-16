// Purpose: `cascade mcp serve --stdio --capture <dir>` — tee the RAW MCP
//
//	frames in both directions to `<dir>/in.jsonl` and `<dir>/out.jsonl`.
//
// WHY IT EXISTS. Art.2 requires the MCP goldens to come from a REAL
//
//	client, and there is no way to see what a real client sends without
//	capturing it. The lesson this repo already paid for
//	(lesson_mcp_dialect_built_from_ruling_paraphrase) is that a wire built
//	from a spec PARAPHRASE speaks no real client's protocol: D/S-06.T6
//	shipped exactly that, and a live probe found every frame the installed
//	first-party client sends rejected with -32600. So the capture lands
//	FIRST and the implementation follows the bytes, not the other way
//	round. The client and version are named in
//	internal/mcp/testdata/README.md, where provenance belongs.
//
// Inputs: a directory. Nothing else about serving changes.
// Outputs: two append-only files of raw frames, plus the normal service.
// Constraints: this is a DIAGNOSTIC, not a protocol feature — it alters no
//
//	frame, adds no field, and its absence changes nothing. It never touches
//	os.Stdin/os.Stdout: the streams are the ones cobra already injects
//	(Art.10's output-gate rule).
//
// SPORT: cmd/cascade:mcp-capture (ADD) — P1-E04-W4-S86-T1.
package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// captureStreams wraps in and out so every byte crossing them is also
// written to dir, and returns a closer for the two files.
//
// A capture that could not be opened is an ERROR, never a silent
// pass-through: an operator who asked for frames and got a working server
// with no files would conclude the client sent nothing.
func captureStreams(dir string, in io.Reader, out io.Writer) (io.Reader, io.Writer, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, nil, cascade.Wrap(cascade.KindUnavailable, err,
			"mcp serve --capture: creating the capture directory")
	}
	inFile, err := openCaptureFile(dir, "in.jsonl")
	if err != nil {
		return nil, nil, nil, err
	}
	outFile, err := openCaptureFile(dir, "out.jsonl")
	if err != nil {
		_ = inFile.Close()
		return nil, nil, nil, err
	}
	closer := func() {
		_ = inFile.Close()
		_ = outFile.Close()
	}
	return io.TeeReader(in, inFile), io.MultiWriter(out, outFile), closer, nil
}

// openCaptureFile appends to one capture file.
//
// Append, not truncate: a client that reconnects mid-session would
// otherwise erase the handshake that preceded it, which is the half of the
// exchange the goldens most need.
func openCaptureFile(dir, name string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"mcp serve --capture: opening %s", name)
	}
	return f, nil
}
