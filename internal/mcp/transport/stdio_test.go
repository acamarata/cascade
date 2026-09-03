package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
)

// fakeDispatcher is a Dispatcher test double: it returns whatever fn
// produces, so tests can assert Serve's line-framing behavior in isolation
// from Server.Dispatch's own logic (already covered in ../server_test.go).
type fakeDispatcher struct {
	fn func(ctx context.Context, f *mcp.Frame) *mcp.Response
}

func (d fakeDispatcher) Dispatch(ctx context.Context, f *mcp.Frame) *mcp.Response {
	return d.fn(ctx, f)
}

func echoOK() fakeDispatcher {
	return fakeDispatcher{fn: func(_ context.Context, f *mcp.Frame) *mcp.Response {
		return &mcp.Response{JSONRPC: "2.0", ID: f.ID, Result: "ok"}
	}}
}

func readLines(t *testing.T, out *bytes.Buffer) []string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestStdioTransport_HappyPath(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","mcp_method":"tools/list","mcp_name":"c","id":1}` + "\n")
	out := &bytes.Buffer{}
	tr := transport.NewStdioTransport(echoOK(), in, out)

	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := readLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("got %d response lines, want 1: %v", len(lines), lines)
	}
	var resp mcp.Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("response line not valid JSON: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestStdioTransport_CleanEOF(t *testing.T) {
	tr := transport.NewStdioTransport(echoOK(), strings.NewReader(""), &bytes.Buffer{})
	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() on empty input error = %v, want nil (clean EOF)", err)
	}
}

func TestStdioTransport_NonJSONLine(t *testing.T) {
	in := strings.NewReader("not json at all\n")
	out := &bytes.Buffer{}
	tr := transport.NewStdioTransport(echoOK(), in, out)
	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v, want nil (malformed frame is an error RESPONSE, not a fatal error)", err)
	}
	lines := readLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("got %d response lines, want 1", len(lines))
	}
	var resp mcp.Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("want an error response for a non-JSON line")
	}
}

func TestStdioTransport_TruncatedLine(t *testing.T) {
	// No trailing newline: bufio.Scanner still yields the partial token on
	// EOF, and ParseFrame must reject it as malformed JSON, not panic.
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"tools/l`)
	out := &bytes.Buffer{}
	tr := transport.NewStdioTransport(echoOK(), in, out)
	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := readLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("got %d response lines, want 1", len(lines))
	}
	var resp mcp.Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("want an error response for a truncated line")
	}
}

func TestStdioTransport_OversizedLine(t *testing.T) {
	huge := strings.Repeat("a", 5<<20) // exceeds maxFrameBytes (4 MiB)
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"` + huge + `"}` + "\n")
	out := &bytes.Buffer{}
	tr := transport.NewStdioTransport(echoOK(), in, out)
	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v, want nil (oversized frame reports a bounded error response)", err)
	}
	lines := readLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("got %d response lines, want 1", len(lines))
	}
	var resp mcp.Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("want an error response for an oversized line")
	}
}

func TestStdioTransport_CanceledContext(t *testing.T) {
	in := strings.NewReader(strings.Repeat(`{"jsonrpc":"2.0","method":"tools/list","mcp_method":"tools/list","mcp_name":"c","id":1}`+"\n", 5))
	out := &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := transport.NewStdioTransport(echoOK(), in, out)
	if err := tr.Serve(ctx); err == nil {
		t.Fatal("want a non-nil error from Serve() with an already-canceled context")
	}
}
