package nodes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// edgeConn is an in-memory Conn that records Close.
type edgeConn struct {
	io.Reader
	closed bool
}

func (c *edgeConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *edgeConn) Close() error                { c.closed = true; return nil }

type failingSigner struct{ payloadSigner }

func (failingSigner) Sign(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("sign failed")
}

// TestControllerHelloMessageRefusesBadInput covers the message builder's
// refusals and VerifyControllerHello's pin and nonce checks.
func TestControllerHelloMessageRefusesBadInput(t *testing.T) {
	nonce := bytes.Repeat([]byte{3}, 32)
	for _, c := range []struct {
		node  string
		nonce []byte
	}{{"", nonce}, {"a\x1fb", nonce}, {"node-1", nonce[:31]}} {
		if _, err := controllerHelloMessage(c.node, c.nonce); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("controllerHelloMessage(%q, %d bytes) = %v, want KindInvalidInput", c.node, len(c.nonce), err)
		}
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertHelloError(t, VerifyControllerHello(pub[:5], "node-1", nonce, nil), errControllerHelloPin, cascade.KindPermissionDenied)
	if err := VerifyControllerHello(pub, "node-1", nonce[:3], nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("short nonce = %v, want KindInvalidInput", err)
	}
}

// TestControllerHelloHandlerRefusals covers the handler's missing signer,
// missing identity, malformed params, bad nonce and failing signer.
func TestControllerHelloHandlerRefusals(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ok := func(context.Context) (string, bool) { return "node-1", true }
	ctx := context.Background()
	if _, err := controllerHelloHandler(nil, ok)(ctx, nil); !errors.Is(err, errControllerHelloPin) {
		t.Errorf("nil signer = %v", err)
	}
	none := func(context.Context) (string, bool) { return "", false }
	if _, err := controllerHelloHandler(payloadSigner{key}, none)(ctx, nil); !errors.Is(err, errControllerHelloIdentity) {
		t.Errorf("no identity = %v", err)
	}
	h := controllerHelloHandler(payloadSigner{key}, ok)
	if _, err := h(ctx, json.RawMessage(`{`)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("malformed params = %v", err)
	}
	if _, err := h(ctx, json.RawMessage(`{"nonce":"AAAA"}`)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("short nonce = %v", err)
	}
	params, _ := json.Marshal(controllerHelloParams{Nonce: bytes.Repeat([]byte{9}, 32)})
	if _, err := controllerHelloHandler(failingSigner{payloadSigner{key}}, ok)(ctx, params); err == nil || err.Error() != "sign failed" {
		t.Errorf("failing signer = %v", err)
	}
}

// TestAuthenticateControllerChannelPreconditions covers the refusals taken
// before any bytes move: nil clock or conn, a bad pin and a canceled context.
// Every refusal closes the stream it was handed.
func TestAuthenticateControllerChannelPreconditions(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := authenticateControllerChannel(ctx, nil, pub, "node-1", clock, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("nil conn = %v", err)
	}
	conn := &edgeConn{Reader: strings.NewReader("")}
	if _, err := authenticateControllerChannel(ctx, conn, pub, "node-1", nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) || !conn.closed {
		t.Errorf("nil clock = %v, closed=%v", err, conn.closed)
	}
	conn = &edgeConn{Reader: strings.NewReader("")}
	_, err = authenticateControllerChannel(ctx, conn, pub[:4], "node-1", clock, nil)
	assertHelloError(t, err, errControllerHelloPin, cascade.KindPermissionDenied)
	if !conn.closed {
		t.Error("bad pin left the stream open")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	conn = &edgeConn{Reader: strings.NewReader("")}
	_, err = authenticateControllerChannel(canceled, conn, pub, "node-1", clock, nil)
	assertHelloError(t, err, errControllerHelloCanceled, cascade.KindCanceled)
	if !conn.closed {
		t.Error("canceled hello left the stream open")
	}
}

// TestControllerHelloConnReadsBufferedBytes proves the released stream keeps
// bytes the hello reader had already buffered.
func TestControllerHelloConnReadsBufferedBytes(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("after-hello"))
	c := controllerHelloConn{Conn: &edgeConn{Reader: strings.NewReader("")}, reader: r}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "after-hello" {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

// TestExchangeControllerHelloRefusesBadReplies covers unreadable, oversized
// and non-2.0 replies: each is a signature refusal.
func TestExchangeControllerHelloRefusesBadReplies(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, reply := range map[string]string{
		"garbage":   "not http",
		"oversized": "HTTP/1.1 200 OK\r\nContent-Length: 5000\r\n\r\n" + strings.Repeat("x", 5000),
		"not json":  "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{]",
		"wrong rpc": "HTTP/1.1 200 OK\r\nContent-Length: 29\r\n\r\n{\"jsonrpc\":\"1.0\",\"id\":1}     ",
		"bad id":    "HTTP/1.1 200 OK\r\nContent-Length: 24\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":2}",
	} {
		conn := &edgeConn{Reader: strings.NewReader(reply)}
		err := exchangeControllerHello(context.Background(), conn, bufio.NewReader(conn), pub, "node-1")
		if err == nil {
			t.Errorf("%s: reply accepted", name)
		}
	}
	conn := &edgeConn{Reader: strings.NewReader("")}
	if err := exchangeControllerHello(context.Background(), conn, bufio.NewReader(conn), pub, "a\x1fb"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("bad node id = %v", err)
	}
}
