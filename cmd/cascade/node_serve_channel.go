// Purpose: deliver heartbeats over the forwarded, pinned controller socket.
// Constraints: only controller hello may cross an unauthenticated stream.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// heartbeatRequest is the JSON-RPC request frame sent over the forwarded
// socket. A local copy: cmd/cascade may not import internal/rpc.
type heartbeatRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

var (
	errHeartbeatEndpoint = cascade.New(cascade.KindInvalidInput, "node serve: heartbeat endpoint must be unix:<absolute path>")
	errHeartbeatRPC      = cascade.New(cascade.KindUnavailable, "node serve: heartbeat RPC error")
	errHeartbeatReply    = cascade.New(cascade.KindUnavailable, "node serve: heartbeat response is malformed or missing result")
)

func dialHeartbeatSocket(ctx context.Context, path string) (nodes.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

// httpHeartbeatSender uses HTTP framing exclusively on the forwarded socket.
// The dial seam receives only a validated absolute Unix socket path.
func httpHeartbeatSender(binding nodes.ControllerBinding, nodeID string, clock cruntime.Clock, dial func(context.Context, string) (nodes.Conn, error)) nodes.HeartbeatSender {
	return func(ctx context.Context, f nodes.HeartbeatFrame) error {
		path := strings.TrimPrefix(binding.Endpoint, "unix:")
		if !strings.HasPrefix(binding.Endpoint, "unix:") || !filepath.IsAbs(path) || strings.HasPrefix(path, "//") || strings.ContainsRune(path, 0) {
			return errHeartbeatEndpoint
		}
		pin, err := nodes.ParsePublicKey(binding.ControllerPubKeyB64)
		if err != nil {
			return cascade.Wrap(cascade.KindPermissionDenied, err, "node serve: heartbeat requires a pinned controller key")
		}
		if clock == nil || dial == nil {
			return cascade.New(cascade.KindInvalidInput, "node serve: heartbeat requires a clock and socket dialer")
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn, err := dial(ctx, path)
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "node serve: heartbeat socket dial")
		}
		defer func() { _ = conn.Close() }()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		r := bufio.NewReader(conn)
		if err := heartbeatControllerHello(ctx, conn, r, pin, nodeID, clock); err != nil {
			return err
		}
		_, err = heartbeatExchange(ctx, conn, r, "node.heartbeat", f)
		return err
	}
}

func heartbeatControllerHello(ctx context.Context, conn nodes.Conn, r *bufio.Reader, pin []byte, nodeID string, clock cruntime.Clock) error {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "node serve: controller hello nonce")
	}
	deadline := clock.Now().Add(5 * time.Second)
	helloCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(helloCtx, func() { _ = conn.Close() })
	defer stop()
	raw, err := heartbeatExchange(helloCtx, conn, r, "node.channel.hello", map[string]any{"nonce": nonce})
	if err != nil {
		return err
	}
	if helloCtx.Err() != nil || !clock.Now().Before(deadline) {
		return cascade.New(cascade.KindUnavailable, "node serve: controller hello timed out")
	}
	var result struct {
		Signature []byte `json:"signature"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return cascade.Wrap(cascade.KindPermissionDenied, err, "node serve: malformed controller hello")
	}
	return nodes.VerifyControllerHello(pin, nodeID, nonce, result.Signature)
}

func heartbeatExchange(ctx context.Context, conn nodes.Conn, r *bufio.Reader, method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "node serve: encode heartbeat parameters")
	}
	body, _ := json.Marshal(heartbeatRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: payload})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+nodes.RPCPath, bytes.NewReader(body))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "node serve: build heartbeat request")
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node serve: send controller request")
	}
	resp, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node serve: read controller response")
	}
	// The connection owner closes failures; never drain an untrusted body.
	if resp.StatusCode/100 != 2 || resp.Close {
		return nil, cascade.Newf(cascade.KindUnavailable, "node serve: heartbeat POST returned status %d or closed stream", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errHeartbeatReply
	}
	if err := resp.Body.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node serve: close controller response")
	}
	return heartbeatResult(raw)
}

func heartbeatResult(raw []byte) (json.RawMessage, error) {
	var reply map[string]json.RawMessage
	if json.Unmarshal(raw, &reply) != nil || string(reply["jsonrpc"]) != `"2.0"` || string(reply["id"]) != "1" {
		return nil, errHeartbeatReply
	}
	if _, present := reply["error"]; present {
		return nil, errHeartbeatRPC
	}
	result, present := reply["result"]
	if !present || bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		return nil, errHeartbeatReply
	}
	return result, nil
}
