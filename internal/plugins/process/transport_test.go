package process

import (
	"bufio"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakePlugin echoes one Request back as a Response with a fixed result,
// or replies with an RPCError when the method starts with "fail.".
type fakePlugin struct {
	toClient   io.Writer
	fromClient io.Reader
}

func newFakePlugin(toClient io.Writer, fromClient io.Reader) *fakePlugin {
	p := &fakePlugin{toClient: toClient, fromClient: fromClient}
	go p.run()
	return p
}

func (p *fakePlugin) run() {
	scanner := bufio.NewScanner(p.fromClient)
	for scanner.Scan() {
		var req Request
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.Method == "" {
			continue
		}
		if req.Method == "silence" {
			continue // deliberately never respond, to exercise timeout
		}
		resp := Response{JSONRPC: "2.0", ID: req.ID}
		if req.Method == "fail.method" {
			resp.Error = &RPCError{Code: 7, Message: "boom"}
		} else {
			resp.Result = json.RawMessage(`{"ok":true}`)
		}
		data, _ := json.Marshal(resp)
		data = append(data, '\n')
		if _, err := p.toClient.Write(data); err != nil {
			return
		}
	}
}

func newTransportPair(t *testing.T, callTimeout time.Duration) (client *Transport, closeClient func()) {
	t.Helper()
	clientReads, pluginWrites := io.Pipe()
	pluginReads, clientWrites := io.Pipe()
	newFakePlugin(pluginWrites, pluginReads)
	tr := NewTransport(clientWrites, clientReads, callTimeout)
	return tr, func() { _ = clientWrites.Close(); _ = clientReads.Close() }
}

func TestTransportCallRoundTrip(t *testing.T) {
	tr, closeFn := newTransportPair(t, 2*time.Second)
	defer closeFn()
	result, err := tr.Call(context.Background(), "cascade.hello", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}

func TestTransportCallErrorResponse(t *testing.T) {
	tr, closeFn := newTransportPair(t, 2*time.Second)
	defer closeFn()
	_, err := tr.Call(context.Background(), "fail.method", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("expected KindInternal, got %v", err)
	}
}

func TestTransportCallTimeout(t *testing.T) {
	tr, closeFn := newTransportPair(t, 50*time.Millisecond)
	defer closeFn()
	_, err := tr.Call(context.Background(), "silence", nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("expected KindTimeout, got %v", err)
	}
}

func TestTransportCallContextCanceled(t *testing.T) {
	tr, closeFn := newTransportPair(t, 5*time.Second)
	defer closeFn()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tr.Call(ctx, "silence", nil)
	if err == nil {
		t.Fatal("expected an error for a canceled context")
	}
}

func TestTransportClosedTransportRefusesNewCalls(t *testing.T) {
	clientReads, pluginWrites := io.Pipe()
	_, clientWrites := io.Pipe()
	tr := NewTransport(clientWrites, clientReads, time.Second)
	_ = pluginWrites.Close() // plugin "exited": EOF on the client's read side
	// Give the read loop a moment to observe EOF and mark closed.
	deadline := time.Now().Add(time.Second)
	for !trClosedSnapshot(tr) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_, err := tr.Call(context.Background(), "cascade.hello", nil)
	if err == nil {
		t.Fatal("expected an error from a closed transport")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("expected KindUnavailable, got %v", err)
	}
}

// trClosedSnapshot reads Transport.closed under its own mutex via a
// tiny reflection-free accessor kept local to the test: it calls Call
// with an already-canceled context, which takes the closed-check path
// without allocating a real pending slot when closed is true, and reuses
// register()'s own locking. Simpler: just probe via register().
func trClosedSnapshot(tr *Transport) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.closed
}

func TestTransportNotify(t *testing.T) {
	tr, closeFn := newTransportPair(t, time.Second)
	defer closeFn()
	if err := tr.Notify("cascade.version_mismatch", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

func TestTransportNotifyOnClosedWriter(t *testing.T) {
	clientReads, pluginWrites := io.Pipe()
	clientReads2, clientWrites := io.Pipe()
	defer func() { _ = clientReads2.Close() }()
	tr := NewTransport(clientWrites, clientReads, time.Second)
	_ = pluginWrites.Close()
	_ = clientWrites.Close()
	if err := tr.Notify("x", nil); err == nil {
		t.Fatal("expected a write error on a closed pipe")
	}
}

func TestTransportNotificationsChannel(t *testing.T) {
	clientReads, pluginWrites := io.Pipe()
	_, clientWrites := io.Pipe()
	tr := NewTransport(clientWrites, clientReads, time.Second)
	go func() {
		_, _ = pluginWrites.Write([]byte(`{"jsonrpc":"2.0","method":"cascade.version_mismatch","params":{}}` + "\n"))
	}()
	select {
	case n := <-tr.Notifications():
		if n.Method != "cascade.version_mismatch" {
			t.Fatalf("unexpected notification: %+v", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a notification, none arrived")
	}
}

func TestTransportCloseNonCloserWriter(t *testing.T) {
	tr := &Transport{w: nonCloserWriter{}, pending: make(map[uint64]*pendingCall), notifications: make(chan Notification, 1)}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close on a non-Closer writer should be a no-op, got: %v", err)
	}
}

// nonCloserWriter satisfies io.Writer but deliberately not io.Closer, to
// exercise Transport.Close's "not a Closer" branch.
type nonCloserWriter struct{}

func (nonCloserWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestRouteFrameParsesOnce: every frame shape is decoded exactly once (a
// counter over the Transport's envelope decoder) and still routed as
// before; and routeFrame's body holds no other decode a counter could
// miss (no Unmarshal call, one decode call).
func TestRouteFrameParsesOnce(t *testing.T) {
	var decodes int
	tr := &Transport{
		pending: make(map[uint64]*pendingCall), notifications: make(chan Notification, 4),
		decode: func(line []byte, env *frameEnvelope) error { decodes++; return decodeEnvelope(line, env) },
	}
	pc := &pendingCall{resp: make(chan Response, 1)}
	tr.pending[5] = pc
	frames := []string{
		`{"jsonrpc":"2.0","id":5,"result":{"ok":true},"error":{"code":3,"message":"m"}}`,
		`{"jsonrpc":"2.0","method":"host_log","params":{"k":"v"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"host_secret_ref","params":{"key":"x"}}`,
		`not json`,
	}
	for _, f := range frames {
		tr.routeFrame([]byte(f))
	}
	if decodes != len(frames) {
		t.Fatalf("decoder ran %d times for %d frames, want exactly once per frame", decodes, len(frames))
	}
	resp := <-pc.resp
	if resp.ID != 5 || string(resp.Result) != `{"ok":true}` || resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("response = %+v, want id 5 with its result and error", resp)
	}
	for _, want := range []string{"host_log", "host_secret_ref"} {
		n := <-tr.notifications
		if n.Method != want || n.JSONRPC != "2.0" || len(n.Params) == 0 {
			t.Fatalf("notification = %+v, want method %q with params", n, want)
		}
	}
	if len(tr.notifications) != 0 {
		t.Fatal("the malformed frame was delivered")
	}
	assertRouteFrameDecodesOnce(t)
}

func TestRouteFrameDropsInvalidEnvelope(t *testing.T) {
	for _, frame := range []string{
		`null`, `{}`, `{"jsonrpc":"2.0"}`, `{"jsonrpc":"2.0","method":""}`,
		`{"method":"host_log"}`, `{"jsonrpc":"1.0","method":"host_log"}`,
		`{"id":5,"result":true}`, `{"jsonrpc":"1.0","id":5,"result":true}`,
	} {
		t.Run(frame, func(t *testing.T) {
			pc := &pendingCall{resp: make(chan Response, 1)}
			tr := &Transport{pending: map[uint64]*pendingCall{5: pc}, notifications: make(chan Notification, 1)}
			tr.routeFrame([]byte(frame))
			if len(tr.notifications) != 0 || len(pc.resp) != 0 || tr.pending[5] != pc {
				t.Fatal("invalid envelope delivered or removed a pending call")
			}
		})
	}
}

// assertRouteFrameDecodesOnce checks transport.go's routeFrame body: one
// call through decode and no Unmarshal anywhere in it.
func assertRouteFrameDecodesOnce(t *testing.T) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "transport.go", nil, 0)
	if err != nil {
		t.Fatalf("parse transport.go: %v", err)
	}
	var body *ast.BlockStmt
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "routeFrame" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("routeFrame not found in transport.go")
	}
	decodeCalls := 0
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "decode" {
				decodeCalls++
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name == "Unmarshal" {
				t.Errorf("routeFrame calls %s.Unmarshal: a second parse of the frame", fn.X)
			}
		}
		return true
	})
	if decodeCalls != 1 {
		t.Fatalf("routeFrame calls decode %d times, want 1", decodeCalls)
	}
}
