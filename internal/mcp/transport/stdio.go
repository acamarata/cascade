// Package transport implements the MCP transports: this file, the stdio
// transport — line-framed JSON over an injected io.Reader/io.Writer pair,
// one MCP frame per line, stateless per-request dispatch through a
// Dispatcher.
//
// Inputs: an io.Reader carrying newline-delimited MCP frames (in
//
//	production, the process's real stdin — cmd/cascade/mcp.go supplies
//	os.Stdin) and an io.Writer for responses.
//
// Outputs: one Response line per input frame, newline-terminated.
// Constraints: NEVER touches os.Stdin/os.Stdout globals directly (the
//
//	internal/build AST output gate forbids bare os.Stdout/os.Stderr outside
//	internal/output) — both streams are caller-injected fields, which also
//	makes this transport testable with bytes.Buffer, with no subprocess and
//	no `net` import (keeps this file in the no-network default test lane).
//	The scanner bounds the per-line buffer (maxFrameBytes) so a hostile
//	peer cannot exhaust memory by never sending a newline — this, not a
//	length-prefix field, is this transport's "hostile length prefix"
//	defense (see fuzz_test.go's FuzzMCPFrame, which fuzzes the decoder this
//	file calls once a bounded line is in hand).
//
// SPORT: internal/mcp/transport [ADD] (P1-E04-W1-S06-T6 sport_updates).
package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/acamarata/cascade/internal/mcp"
)

// maxFrameBytes bounds one stdio frame line. 4 MiB mirrors
// internal/rpc/handler.go's maxBodyBytes for the same reason: a generous
// ceiling for any realistic MCP request, small enough that a hostile peer
// cannot use it to exhaust process memory.
const maxFrameBytes = 4 << 20

// Dispatcher is the stateless MCP core this transport hands decoded frames
// to. *mcp.Server satisfies it; tests may inject a fake.
type Dispatcher interface {
	Dispatch(ctx context.Context, f *mcp.Frame) *mcp.Response
}

// StdioTransport serves MCP over line-framed JSON on in/out.
type StdioTransport struct {
	dispatcher Dispatcher
	in         io.Reader
	out        io.Writer
}

// NewStdioTransport builds a StdioTransport reading frames from in and
// writing responses to out — both caller-supplied, never the os.Stdin/
// os.Stdout globals (see this file's doc comment).
func NewStdioTransport(dispatcher Dispatcher, in io.Reader, out io.Writer) *StdioTransport {
	return &StdioTransport{dispatcher: dispatcher, in: in, out: out}
}

// Serve reads frames from t.in until EOF (clean shutdown) or ctx is
// canceled, dispatching each stateless request and writing back exactly
// one Response line per request. A malformed frame (non-JSON, truncated,
// unexpected EOF mid-line) never stops the loop — it produces one error
// Response for that line and Serve continues, matching this ticket's
// contract that malformed-frame handling is an error PATH, not a fatal
// condition. Serve returns nil on a clean EOF, ctx.Err() if canceled, and
// any real write failure encountered along the way.
func (t *StdioTransport) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(t.in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		resp := t.dispatchLine(ctx, line)
		if err := t.writeResponse(resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return t.writeResponse(&mcp.Response{
				JSONRPC: "2.0",
				Error:   &mcp.ErrorObject{Code: -32700, Message: "frame exceeds maximum size"},
			})
		}
		return err
	}
	return nil
}

// dispatchLine decodes one line and dispatches it, never panicking and
// never returning a nil *mcp.Response — a decode failure produces an error
// Response with no ID (the frame could not be parsed far enough to recover
// one), matching truncated/malformed-frame handling for any other
// unparseable line.
func (t *StdioTransport) dispatchLine(ctx context.Context, line []byte) *mcp.Response {
	f, perr := mcp.ParseFrame(line)
	if perr != nil {
		return &mcp.Response{JSONRPC: "2.0", Error: perr}
	}
	return t.dispatcher.Dispatch(ctx, f)
}

func (t *StdioTransport) writeResponse(resp *mcp.Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = t.out.Write(b)
	return err
}
