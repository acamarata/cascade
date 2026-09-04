// Package client is the Go IPC client SDK for the cascade daemon: the ONLY
// way any part of the cascade binary dials the daemon's JSON-RPC 2.0
// channel over its unix socket. cmd/cascade's commands construct a Client
// and call its typed method wrappers (Status, today's only registered
// method) rather than assembling an http.Request by hand — a depguard rule
// (.golangci.yml) plus this package's own boundary test enforce that no
// cmd/cascade file still does the latter.
//
// Purpose: Client wraps the daemon's POST /rpc JSON-RPC 2.0 request/response
//
//	cycle (internal/rpc.Handler is the server side this dials); streamClient
//	(stream.go) wraps the parallel GET /events SSE channel.
//
// Inputs: an injected DialFunc (production passes UnixDialer, which dials
//
//	the real unix socket; integration tests substitute a loopback dialer
//	against a real internal/rpc.Handler fixture — never a hand-rolled fake
//	that skips the real wire framing), a socket path, and a per-call
//	timeout.
//
// Outputs: typed results, or a *pkg/cascade.Error whose Kind is drawn from
//
//	the frozen 14-kind taxonomy (decode.go's kindForRPCError and
//	classifyTransportError below) — never a raw net/http error escaping
//	this package.
//
// Constraints: every byte the far end sends is untrusted. The whole
//
//	response path is pure and socket-free (codec.go's decodeResponse over
//	decode.go's decodeEnvelope, a fuzz target), so the encode/decode
//	contract is asserted in the default no-network unit lane and only the
//	irreducible request/response transport itself needs a real socket
//	(client_integration_test.go). No bare time.Now/time.Since — timeouts
//	run through context deadlines derived from the injected timeout, never
//	a clock read. This package never writes to os.Stdout/os.Stderr.
//
// SPORT: internal/client (ADD).
package client

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DialFunc resolves the transport-level connection to the daemon's unix
// socket. Production passes UnixDialer; integration tests substitute a
// loopback dialer against a real internal/rpc.Handler fixture. Matches
// cmd/cascade/status.go's statusDeps.DialContext field shape exactly, so
// callers pass that field's value straight through without an adapter.
type DialFunc func(ctx context.Context, socketPath string) (net.Conn, error)

// UnixDialer is the production DialFunc: it connects to the daemon's unix
// domain socket at socketPath. Exported so every command that builds a
// Client shares one dialer rather than re-deriving the same three lines
// (cmd/cascade/status.go's productionStatusDeps is its production caller).
func UnixDialer(ctx context.Context, socketPath string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
}

// rpcPath is the daemon's single JSON-RPC route (internal/rpc.RPCPath).
// Duplicated as a literal rather than importing internal/rpc: this
// package's whole reason to exist is that cmd/cascade never imports
// internal/rpc for outbound calls again, and a self import of the server
// package here would defeat that boundary for every downstream importer.
const rpcPath = "/rpc"

// protocolVersion is the client_version this SDK sends on every request,
// mirroring internal/rpc/version.go's ProtocolVersion. Duplicated rather
// than imported for the same reason as rpcPath.
const protocolVersion = "1.0.0"

// unixBaseURL is the URL scheme/host component every request carries;
// "unix" is never resolved by net/http itself (the Transport's DialContext
// ignores it and dials socketPath directly) — it exists only because
// http.NewRequestWithContext requires a well-formed URL.
const unixBaseURL = "http://unix"

// Client is the Go IPC client SDK: one JSON-RPC 2.0 channel over a unix
// socket, plus the typed method wrappers built on it.
type Client struct {
	// target names what dialing failed to reach, for error messages only.
	target     string
	httpClient *http.Client
}

// New builds a Client dialing socketPath through dial, bounding every call
// (Do and the typed wrappers built on it) by timeout. dial is required;
// New panics if it is nil — a programmer error (a missing injection), not
// a runtime condition a caller can recover from.
func New(socketPath string, dial DialFunc, timeout time.Duration) *Client {
	if dial == nil {
		panic("client: New called with a nil DialFunc")
	}
	return &Client{
		target:     socketPath,
		httpClient: newHTTPClient(socketPath, dial, timeout),
	}
}

// newHTTPClient wraps dial into the http.Client both halves of this SDK
// (Do's request/response exchange and streamClient's SSE channel) issue
// their requests through.
func newHTTPClient(socketPath string, dial DialFunc, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dial(ctx, socketPath)
			},
		},
		Timeout: timeout,
	}
}

// Do issues one JSON-RPC 2.0 call: method with params (marshaled as the
// request's "params" member; nil is sent as an omitted member), decoding
// the result into out (a pointer, or nil to discard the result). Do is the
// ONE mechanism every typed wrapper (and any future one) is built on.
//
// Its three stages are deliberately separable: encodeRequest and
// decodeResponse (codec.go) are pure functions over bytes, so request
// framing, id correlation, taxonomy mapping and every malformed-response
// case are asserted without a socket; exchange below is the only part that
// needs one.
func (c *Client) Do(ctx context.Context, method string, params any, out any) error {
	id := requestSeq(method)
	body, err := encodeRequest(method, params, id)
	if err != nil {
		return err
	}
	respBody, err := c.exchange(ctx, method, body)
	if err != nil {
		return err
	}
	return decodeResponse(method, id, respBody, out)
}

// exchange performs the one irreducible socket-bound step: POST body to
// the daemon's /rpc route and read the whole response back, capped by
// readCapped. Every transport failure is classified into the taxonomy
// here, so no net/http error ever escapes this package.
func (c *Client) exchange(ctx context.Context, method string, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, unixBaseURL+rpcPath, bytes.NewReader(body))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "client: build request "+method)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(ctx, method, c.target, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	return readCapped(httpResp.Body, method)
}

// classifyTransportError maps a failure from the underlying http.Client.Do
// call (dial refused, socket missing, deadline exceeded, context canceled)
// to a taxonomy Kind: connection refused and no socket are both "a
// dependency is temporarily unreachable" -> KindUnavailable; a
// deadline/timeout -> KindTimeout; an explicit caller cancellation ->
// KindCanceled. Anything else falls back to KindUnavailable (the safe
// default for "could not complete the transport exchange"), never
// KindInternal, since no response was ever decoded.
func classifyTransportError(ctx context.Context, method, socketPath string, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return cascade.Wrap(cascade.KindCanceled, err, "client: "+method+" canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cascade.Wrap(cascade.KindTimeout, err, "client: "+method+" timed out dialing "+socketPath)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return cascade.Wrap(cascade.KindTimeout, err, "client: "+method+" timed out dialing "+socketPath)
	}
	return cascade.Wrap(cascade.KindUnavailable, err,
		"client: "+method+" daemon not running or unreachable at "+socketPath)
}
