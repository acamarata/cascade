package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestHeartbeatSenderRPCErrorIsFailure rejects HTTP success carrying an RPC error.
func TestHeartbeatSenderRPCErrorIsFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	send := channelTestSender(t, 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"refused"}}`, false)
	assertHeartbeatError(t, send(context.Background(), nodes.HeartbeatFrame{}), errHeartbeatRPC)
	testHeartbeatLoopFailure(t, send)
}

// TestHeartbeatSenderRefusesHTTPEndpoint proves rejection precedes dialing.
func TestHeartbeatSenderRefusesHTTPEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	for _, endpoint := range []string{"http://controller", "https://controller", "tcp:controller:80", "", "unix:relative", "unix://controller/socket", "unix:/bad\x00socket"} {
		t.Run(endpoint, func(t *testing.T) {
			dial := func(context.Context, string) (nodes.Conn, error) {
				t.Fatal("invalid endpoint reached dial")
				return nil, nil
			}
			send := httpHeartbeatSender(nodes.ControllerBinding{Endpoint: endpoint}, "node-1", nil, dial)
			assertHeartbeatError(t, send(context.Background(), nodes.HeartbeatFrame{}), errHeartbeatEndpoint)
		})
	}
}

// TestHeartbeatChannelResponses covers malformed responses and a successful send.
func TestHeartbeatChannelResponses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"success", `{"jsonrpc":"2.0","id":1,"result":{}}`, nil},
		{"malformed", `{`, errHeartbeatReply},
		{"missing", `{"jsonrpc":"2.0","id":1}`, errHeartbeatReply},
		{"null result", `{"jsonrpc":"2.0","id":1,"result":null}`, errHeartbeatReply},
		{"error with result", `{"jsonrpc":"2.0","id":1,"result":{},"error":null}`, errHeartbeatRPC},
		{"trailing data", `{"jsonrpc":"2.0","id":1,"result":{}} {}`, errHeartbeatReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send := channelTestSender(t, 200, tc.body, false)
			assertHeartbeatError(t, send(context.Background(), nodes.HeartbeatFrame{}), tc.want)
		})
	}
	t.Run("non-2xx", func(t *testing.T) {
		err := channelTestSender(t, 503, `{}`, false)(context.Background(), nodes.HeartbeatFrame{})
		e, ok := err.(*cascade.Error)
		if !ok || e.Kind != cascade.KindUnavailable || e.Msg != "node serve: heartbeat POST returned status 503 or closed stream" {
			t.Fatalf("status failure = %v", err)
		}
	})
}

// TestHeartbeatChannelWrongPinSendsNoFrame fences heartbeat bytes behind hello.
func TestHeartbeatChannelWrongPinSendsNoFrame(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	err := channelTestSender(t, 200, `{}`, true)(context.Background(), nodes.HeartbeatFrame{})
	e, ok := err.(*cascade.Error)
	if !ok || e.Kind != cascade.KindPermissionDenied || e.Msg != "nodes: controller channel hello does not verify against the pinned controller key" {
		t.Fatalf("hello refusal = %v", err)
	}
}

func assertHeartbeatError(t *testing.T, got, want error) {
	t.Helper()
	if got != want || (want != nil && got.Error() != want.Error()) {
		t.Fatalf("error = %v, want identity and message %v", got, want)
	}
}

type heartbeatPipe struct {
	*io.PipeReader
	w *io.PipeWriter
}

func (p heartbeatPipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p heartbeatPipe) Close() error {
	_ = p.PipeReader.Close()
	return p.w.Close()
}

func channelTestSender(t *testing.T, status int, body string, wrongPin bool) nodes.HeartbeatSender {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if wrongPin {
		pub, _, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "controller.sock")
	binding := nodes.ControllerBinding{Endpoint: "unix:" + path, ControllerPubKeyB64: base64.StdEncoding.EncodeToString(pub)}
	dial := func(_ context.Context, got string) (nodes.Conn, error) {
		if got != path {
			t.Errorf("dial path = %q, want %q", got, path)
		}
		a, b := io.Pipe()
		c, d := io.Pipe()
		node, peer := heartbeatPipe{a, d}, heartbeatPipe{c, b}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = peer.Close() }()
			serveHeartbeatPeer(t, peer, key, status, body, wrongPin)
		}()
		t.Cleanup(func() { _ = node.Close(); _ = peer.Close(); <-done })
		return node, nil
	}
	return httpHeartbeatSender(binding, "node-1", cruntime.NewFixedClock(time.Unix(100, 0)), dial)
}

func serveHeartbeatPeer(t *testing.T, peer nodes.Conn, key ed25519.PrivateKey, status int, body string, wrongPin bool) {
	t.Helper()
	r := bufio.NewReader(peer)
	call := readHeartbeatCall(t, r, "node.channel.hello")
	var params struct {
		Nonce []byte `json:"nonce"`
	}
	if err := json.Unmarshal(call, &params); err != nil {
		t.Error(err)
		return
	}
	sig := ed25519.Sign(key, []byte("cascade.node.channel.v1\x1fnode-1\x1f"+hex.EncodeToString(params.Nonce)))
	hello, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"signature": sig}})
	writeHeartbeatReply(peer, 200, string(hello))
	if wrongPin {
		raw, _ := io.ReadAll(r)
		if len(raw) != 0 {
			t.Error("heartbeat bytes crossed an unverified channel")
		}
		return
	}
	readHeartbeatCall(t, r, "node.heartbeat")
	writeHeartbeatReply(peer, status, body)
}

func readHeartbeatCall(t *testing.T, r *bufio.Reader, method string) json.RawMessage {
	t.Helper()
	path, contentType, body, err := readHeartbeatRequest(r)
	if err != nil {
		t.Error(err)
		return nil
	}
	var call struct {
		Method string
		Params json.RawMessage
	}
	if err := json.Unmarshal(body, &call); err != nil {
		t.Error(err)
	}
	if call.Method != method || path != nodes.RPCPath || contentType != "application/json" {
		t.Errorf("unexpected controller request: %q %s", call.Method, path)
	}
	return call.Params
}

// readHeartbeatRequest reads one HTTP/1.1 request: its path, Content-Type
// and Content-Length body. Unit tests may not import net/http.
func readHeartbeatRequest(r *bufio.Reader) (path, contentType string, body []byte, err error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", "", nil, err
	}
	if fields := strings.Fields(line); len(fields) == 3 {
		path = fields[1]
	}
	n := -1
	for {
		h, err := r.ReadString('\n')
		if err != nil {
			return "", "", nil, err
		}
		h = strings.TrimRight(h, "\r\n")
		if h == "" {
			break
		}
		k, v, _ := strings.Cut(h, ":")
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "content-type":
			contentType = strings.TrimSpace(v)
		case "content-length":
			if n, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return "", "", nil, err
			}
		}
	}
	if n < 0 {
		return "", "", nil, io.ErrUnexpectedEOF
	}
	body = make([]byte, n)
	_, err = io.ReadFull(r, body)
	return path, contentType, body, err
}

func writeHeartbeatReply(w io.Writer, status int, body string) {
	// Failure responses may be closed immediately without reading their bodies.
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d Status\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
}

func testHeartbeatLoopFailure(t *testing.T, send nodes.HeartbeatSender) {
	t.Helper()
	ks, err := nodes.NewNodeKeystore(secrets.Config{Dir: t.TempDir(), ForceFileVault: true, Passphrase: "test-pass"})
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(context.Background(), "node-1", key); err != nil {
		t.Fatal(err)
	}
	ticker := newFakeProbeTicker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		nodes.RunHeartbeatLoop(ctx, nodes.HeartbeatLoopOptions{
			Ticker: ticker, NextSequence: func() uint64 { return 1 }, Send: send,
			BuildReport: func() nodes.CapabilityReport { return nodes.CapabilityReport{K12: nodes.NewK12Preset(0, 1)} },
			Keystore:    ks, NodeID: "node-1", EnrollmentID: "enrollment-1", OnError: func(err error) { errs <- err },
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	ticker.ch <- struct{}{}
	select {
	case err := <-errs:
		e, ok := err.(*cascade.Error)
		if !ok || e.Kind != cascade.KindUnavailable || e.Err != errHeartbeatRPC || e.Msg != "nodes: heartbeat send failed, will retry on the next tick" {
			t.Fatalf("loop failure = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("heartbeat RPC error was counted as success")
	}
}
