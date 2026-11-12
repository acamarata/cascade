package nodes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNodeRefusesUnpinnedControllerSocket fences all node traffic behind hello.
func TestNodeRefusesUnpinnedControllerSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty pin", func(t *testing.T) {
		err := VerifyControllerHello(nil, "node-1", make([]byte, 32), nil)
		assertHelloError(t, err, errControllerHelloPin, cascade.KindPermissionDenied)
	})
	for _, mode := range []string{"correct", "wrong key", "unsigned", "late", "absent", "cancel"} {
		t.Run(mode, func(t *testing.T) { testControllerSocket(t, mode, pub, key) })
	}
	t.Run("channel identity", func(t *testing.T) { testHelloChannelIdentity(t, pub, key) })
	t.Run("transcript binding", func(t *testing.T) { testHelloTranscript(t, pub, key) })
}

func assertHelloError(t *testing.T, got, want error, kind cascade.Kind) {
	t.Helper()
	k, ok := cascade.KindOf(got)
	if got != want || got == nil || got.Error() != want.Error() || !ok || k != kind {
		t.Fatalf("hello refusal = %v, want identity and message %v (%v)", got, want, kind)
	}
}

type helloPipe struct {
	*io.PipeReader
	w *io.PipeWriter
}

func (p helloPipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p helloPipe) Close() error {
	_ = p.PipeReader.Close()
	return p.w.Close()
}

func helloPipePair() (Conn, Conn) {
	a, b := io.Pipe()
	c, d := io.Pipe()
	return helloPipe{a, d}, helloPipe{c, b}
}

func testControllerSocket(t *testing.T, mode string, pub ed25519.PublicKey, key ed25519.PrivateKey) {
	t.Helper()
	node, controller := helloPipePair()
	t.Cleanup(func() { _ = node.Close(); _ = controller.Close() })
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	expire := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan []byte, 1)
	go serveHelloPeer(t, controller, mode, key, clock, expire, cancel, got)
	authenticated, err := authenticateControllerChannel(ctx, node, pub, "node-1", clock, expire)
	if mode == "correct" {
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"node.heartbeat", "node.report", "node.journal", "node.relay"} {
			if err := json.NewEncoder(authenticated).Encode(map[string]string{"method": method, "body": "private"}); err != nil {
				t.Fatal(err)
			}
		}
		_ = authenticated.Close()
	} else {
		assertSocketRefusal(t, mode, err)
		if authenticated != nil {
			t.Fatal("refused connection returned to sender")
		}
	}
	data := <-got
	if mode == "correct" && bytes.Count(data, []byte("private")) != 4 {
		t.Fatalf("authenticated traffic = %q", data)
	}
	if mode != "correct" && len(data) != 0 {
		t.Fatalf("unauthenticated controller received %q", data)
	}
}

func assertSocketRefusal(t *testing.T, mode string, err error) {
	t.Helper()
	want, kind := errControllerHelloSignature, cascade.KindPermissionDenied
	if mode == "late" || mode == "absent" {
		want, kind = errControllerHelloTimeout, cascade.KindUnavailable
	}
	if mode == "cancel" {
		want, kind = errControllerHelloCanceled, cascade.KindCanceled
	}
	assertHelloError(t, err, want, kind)
}

func serveHelloPeer(t *testing.T, conn Conn, mode string, key ed25519.PrivateKey, clock *runtime.FixedClock, expire chan<- time.Time, cancel context.CancelFunc, got chan<- []byte) {
	t.Helper()
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	reqBody, err := readTestRequestBody(r)
	if err != nil {
		t.Error(err)
		got <- nil
		return
	}
	var call rpc.Request
	err = json.Unmarshal(reqBody, &call)
	if err != nil || call.Method != "node.channel.hello" {
		t.Errorf("first controller.sock request = %q, %v", call.Method, err)
		got <- nil
		return
	}
	if mode == "absent" || mode == "cancel" {
		if mode == "cancel" {
			cancel()
		} else {
			expire <- clock.Advance(6 * time.Second)
		}
	} else {
		writeHelloReply(t, conn, call, mode, key, clock)
	}
	data, _ := io.ReadAll(r)
	got <- data
}

func writeHelloReply(t *testing.T, conn Conn, call rpc.Request, mode string, key ed25519.PrivateKey, clock *runtime.FixedClock) {
	t.Helper()
	var params controllerHelloParams
	if err := json.Unmarshal(call.Params, &params); err != nil || len(params.Nonce) != 32 {
		t.Errorf("hello nonce is not 32 random bytes: %v", err)
	}
	if mode == "wrong key" {
		_, key, _ = ed25519.GenerateKey(rand.Reader)
	}
	sig := ed25519.Sign(key, []byte("cascade.node.channel.v1\x1fnode-1\x1f"+hex.EncodeToString(params.Nonce)))
	if mode == "unsigned" {
		sig = nil
	}
	if mode == "late" {
		clock.Advance(6 * time.Second)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": controllerHelloResult{Signature: sig}})
	if _, err := fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		t.Error(err)
	}
}

// readTestRequestBody reads one HTTP/1.1 request head and returns its
// Content-Length body. Unit tests may not import net/http (no-network lane).
func readTestRequestBody(r *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			if n, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		}
	}
	if n < 0 {
		return nil, io.ErrUnexpectedEOF
	}
	body := make([]byte, n)
	_, err := io.ReadFull(r, body)
	return body, err
}

func testHelloChannelIdentity(t *testing.T, pub ed25519.PublicKey, key ed25519.PrivateKey) {
	t.Helper()
	h := controllerHelloHandler(payloadSigner{key}, func(context.Context) (string, bool) { return "channel-node", true })
	nonce := bytes.Repeat([]byte{7}, 32)
	raw, _ := json.Marshal(map[string]any{"nonce": nonce, "node_id": "other-node"})
	result, err := h(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	sig := result.(controllerHelloResult).Signature
	if err := VerifyControllerHello(pub, "channel-node", nonce, sig); err != nil {
		t.Fatal(err)
	}
	assertHelloError(t, VerifyControllerHello(pub, "other-node", nonce, sig), errControllerHelloSignature, cascade.KindPermissionDenied)
	a, ok := rpc.AccessOf("node.channel.hello")
	if !ok || a != rpc.AccessNode {
		t.Fatalf("hello access = %v, %v", a, ok)
	}
}

func testHelloTranscript(t *testing.T, pub ed25519.PublicKey, key ed25519.PrivateKey) {
	t.Helper()
	nonce := bytes.Repeat([]byte{1}, 32)
	message := []byte("cascade.node.channel.v1\x1fnode-1\x1f" + hex.EncodeToString(nonce))
	sig := ed25519.Sign(key, message)
	if err := VerifyControllerHello(pub, "node-1", nonce, sig); err != nil {
		t.Fatal(err)
	}
	nonce[0]++
	assertHelloError(t, VerifyControllerHello(pub, "node-1", nonce, sig), errControllerHelloSignature, cascade.KindPermissionDenied)
	for _, wrong := range [][]byte{message[1:], append([]byte("other-domain"), message...)} {
		sig = ed25519.Sign(key, wrong)
		nonce[0] = 1
		assertHelloError(t, VerifyControllerHello(pub, "node-1", nonce, sig), errControllerHelloSignature, cascade.KindPermissionDenied)
	}
	h := controllerHelloHandler(payloadSigner{key}, func(context.Context) (string, bool) { return "", false })
	_, err := h(context.Background(), json.RawMessage(`{"nonce":null,"node_id":"node-1"}`))
	assertHelloError(t, err, errControllerHelloIdentity, cascade.KindPermissionDenied)
}
