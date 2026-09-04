package bgem3

// Purpose: the spec-conformance server the client's unit suite runs
//   against, and the small builders its tables use. This server exists
//   ONLY under _test.go (Art.1.1). It asserts the CLIENT's conformance to
//   SPEC.md; it is never shipped, is never registered as an embedder lane,
//   and is never evidence that a real sidecar works — that artifact is
//   post-P1 and its integration test is deferred with it.
// Constraints: in-memory (net.Pipe), deterministic, no network, no temp
//   files.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

var testModel = provider.EmbedModel{ID: "bge-m3", Dimensions: 2}

// handlerFunc answers one request. Returning nil means "never answer",
// which is how the silent-sidecar and cancellation cases are staged.
type handlerFunc func(t *testing.T, req wireRequest) []byte

// duplex is one end of an in-memory bidirectional byte stream: what a
// DialFunc yields, without a socket. Closing an end unblocks the other
// end's pending read and fails its pending write, which is exactly the
// behaviour the client relies on to abandon a wedged call.
type duplex struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (d duplex) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d duplex) Write(p []byte) (int, error) { return d.w.Write(p) }
func (d duplex) Close() error {
	_ = d.r.Close()
	return d.w.Close()
}

// duplexPair returns the two ends of one stream, cross-wired.
func duplexPair() (duplex, duplex) {
	toClient, fromServer := io.Pipe()
	toServer, fromClient := io.Pipe()
	return duplex{r: toClient, w: fromClient}, duplex{r: toServer, w: fromServer}
}

// seam is one wired conformance server plus the dialer that reaches it.
// closed fires once the server observes the client hanging up.
type seam struct {
	dial   DialFunc
	closed chan struct{}
}

func newSeam(t *testing.T, handle handlerFunc) *seam {
	t.Helper()
	s := &seam{closed: make(chan struct{})}
	s.dial = func(context.Context) (io.ReadWriteCloser, error) {
		clientEnd, serverEnd := duplexPair()
		done := make(chan struct{})
		t.Cleanup(func() { _ = serverEnd.Close(); <-done })
		go func() {
			defer close(done)
			defer func() { _ = serverEnd.Close() }()
			s.serve(t, serverEnd, handle)
		}()
		return clientEnd, nil
	}
	return s
}

// serve reads one request frame, answers with what handle returns, then
// waits for the client to hang up so a cancellation can be observed.
func (s *seam) serve(t *testing.T, conn io.ReadWriteCloser, handle handlerFunc) {
	if req, ok := readRequest(t, conn); ok {
		if reply := handle(t, req); reply != nil {
			// SPEC.md: a sidecar closes the connection after answering.
			// Closing is what turns a deliberately truncated reply into
			// the framing failure it is, rather than an open stall.
			_, _ = conn.Write(reply)
			_ = conn.Close()
			close(s.closed)
			return
		}
	}
	_, _ = io.Copy(io.Discard, conn)
	close(s.closed)
}

// readRequest decodes one framed request, holding the client's own output
// to SPEC.md's framing rules.
func readRequest(t *testing.T, conn io.Reader) (wireRequest, bool) {
	var header [frameHeaderBytes]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return wireRequest{}, false
	}
	declared := binary.BigEndian.Uint32(header[:])
	if declared == 0 || declared > maxFrameBytes {
		t.Errorf("client declared %d payload bytes, outside the protocol's bounds", declared)
		return wireRequest{}, false
	}
	payload := make([]byte, declared)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Errorf("client's request frame is short: %v", err)
		return wireRequest{}, false
	}
	var req wireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Errorf("client's request payload is not decodable JSON: %v", err)
		return wireRequest{}, false
	}
	return req, true
}

// answer frames a response payload.
func answer(t *testing.T, resp wireResponse) []byte {
	t.Helper()
	payload, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshaling the conformance response: %v", err)
	}
	return frameOf(payload)
}

// good builds a conforming response carrying vecs; edit applies one
// deviation from it.
func good(vecs ...[]float32) wireResponse {
	return wireResponse{
		ProtocolVersion: ProtocolVersion, Model: testModel.ID,
		Dimensions: testModel.Dimensions, Vectors: vecs,
	}
}

func edit(base wireResponse, change func(*wireResponse)) wireResponse {
	change(&base)
	return base
}

func newTestClient(t *testing.T, dial DialFunc, timeout time.Duration) *Client {
	t.Helper()
	c, err := New(Config{Dial: dial, Endpoint: "test-pipe", Model: testModel, Timeout: timeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func inputsOf(texts ...string) []provider.EmbedInput {
	out := make([]provider.EmbedInput, len(texts))
	for i, s := range texts {
		out[i] = provider.EmbedInput{Text: s}
	}
	return out
}

// embedAgainst runs one two-input call against a server that always
// answers with raw.
func embedAgainst(t *testing.T, raw []byte) ([]provider.EmbedOutput, error) {
	t.Helper()
	s := newSeam(t, func(*testing.T, wireRequest) []byte { return raw })
	return newTestClient(t, s.dial, time.Minute).Embed(context.Background(), inputsOf("a", "b"))
}
