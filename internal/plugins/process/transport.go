// Purpose: newline-delimited JSON-RPC 2.0 framing over a plugin process's
//
//	stdin/stdout: one goroutine reads frames and routes them by
//	correlation ID, the caller's goroutine writes a Request and blocks on
//	its own response channel until it arrives or its context is done.
//
// Inputs: an io.Writer (the plugin's stdin) and an io.Reader (its
//
//	stdout), plus a per-call timeout.
//
// Outputs: Call (request/response round trip), Notify (fire-and-forget),
//
//	Notifications (the channel of frames the plugin sent that were not a
//	response to any pending call).
//
// Constraints: every error path returns a typed pkg/cascade error. No
//
//	bare time.Now/After; per-call timeout composes onto the caller's ctx
//	with context.WithTimeout, which is not itself a forbidden identifier.
//
// SPORT: internal/plugins/process transport (ADD) — P1-E15-W4-S31-T3.

package process

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultCallTimeout is the per-call timeout used when a Transport is
// built with a zero timeout.
const DefaultCallTimeout = 30 * time.Second

// pendingCall is one in-flight request awaiting its response.
type pendingCall struct {
	resp chan Response
}

// Transport frames JSON-RPC 2.0 messages, newline-delimited, over a
// plugin process's stdio. The zero value is not usable; build one with
// NewTransport.
type Transport struct {
	w           io.Writer
	writeMu     sync.Mutex
	callTimeout time.Duration

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]*pendingCall
	closed  bool

	notifications chan Notification
	readErr       chan error
}

// NewTransport builds a Transport over w (the plugin's stdin) and r (its
// stdout), and starts the read loop. callTimeout of zero uses
// DefaultCallTimeout.
func NewTransport(w io.Writer, r io.Reader, callTimeout time.Duration) *Transport {
	if callTimeout <= 0 {
		callTimeout = DefaultCallTimeout
	}
	t := &Transport{
		w:             w,
		callTimeout:   callTimeout,
		pending:       make(map[uint64]*pendingCall),
		notifications: make(chan Notification, 16),
		readErr:       make(chan error, 1),
	}
	go t.readLoop(r)
	return t
}

// Notifications returns the channel of frames received that carried no ID
// pending caller never claims (server-initiated notifications).
func (t *Transport) Notifications() <-chan Notification { return t.notifications }

// readLoop scans newline-delimited frames from r until EOF or a decode
// error, routing each to its pending call or the notification channel.
func (t *Transport) readLoop(r io.Reader) {
	defer close(t.notifications)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		t.routeFrame(line)
	}
	t.finishReading(scanner.Err())
}

// routeFrame decodes one frame and delivers it as a Response (if a
// pending call claims its ID) or a Notification (otherwise).
func (t *Transport) routeFrame(line []byte) {
	var probe struct {
		ID     *uint64 `json:"id"`
		Method string  `json:"method"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return
	}
	if probe.ID == nil || probe.Method != "" {
		var n Notification
		if json.Unmarshal(line, &n) == nil {
			t.notifications <- n
		}
		return
	}
	var resp Response
	if json.Unmarshal(line, &resp) != nil {
		return
	}
	t.mu.Lock()
	pc, ok := t.pending[resp.ID]
	if ok {
		delete(t.pending, resp.ID)
	}
	t.mu.Unlock()
	if ok {
		pc.resp <- resp
	}
}

// finishReading records the terminal read error (io.EOF on a clean
// close) and fails every call still pending.
func (t *Transport) finishReading(err error) {
	if err == nil {
		err = io.EOF
	}
	t.readErr <- err
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for id, pc := range t.pending {
		delete(t.pending, id)
		close(pc.resp)
	}
}

// Call sends a JSON-RPC request and blocks for its response, the
// transport's per-call timeout, or ctx's cancellation, whichever comes
// first.
func (t *Transport) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, t.callTimeout)
	defer cancel()

	pc, id, err := t.register()
	if err != nil {
		return nil, err
	}
	req := Request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := t.writeFrame(req); err != nil {
		t.unregister(id)
		return nil, err
	}
	return t.awaitResponse(ctx, pc, id)
}

// register allocates a call ID and its pending slot.
func (t *Transport) register() (*pendingCall, uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, 0, cascade.New(cascade.KindUnavailable, "process: transport is closed")
	}
	t.nextID++
	id := t.nextID
	pc := &pendingCall{resp: make(chan Response, 1)}
	t.pending[id] = pc
	return pc, id, nil
}

// unregister removes id's pending slot without waiting for a response,
// used when the write itself failed.
func (t *Transport) unregister(id uint64) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// awaitResponse blocks for pc's response, ctx's deadline, or the
// transport closing, and converts the outcome to a typed return.
func (t *Transport) awaitResponse(ctx context.Context, pc *pendingCall, id uint64) (json.RawMessage, error) {
	select {
	case resp, ok := <-pc.resp:
		if !ok {
			return nil, cascade.New(cascade.KindUnavailable, "process: transport closed while a call was pending")
		}
		if resp.Error != nil {
			return nil, cascade.Newf(cascade.KindInternal, "process: plugin returned error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		t.unregister(id)
		return nil, cascade.Wrap(cascade.KindTimeout, ctx.Err(), "process: call timed out waiting for plugin response")
	}
}

// Close closes the underlying writer if it implements io.Closer. Used to
// terminate a plugin process cleanly (e.g. after a version-mismatch
// refusal): closing its stdin lets a well-behaved plugin exit on EOF
// before the caller reaps it with Wait.
func (t *Transport) Close() error {
	if c, ok := t.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Notify sends a fire-and-forget notification frame.
func (t *Transport) Notify(method string, params json.RawMessage) error {
	return t.writeFrame(Notification{JSONRPC: "2.0", Method: method, Params: params})
}

// writeFrame marshals v and writes it newline-terminated, serialized
// against concurrent writers.
func (t *Transport) writeFrame(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "process: encoding a frame")
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.w.Write(data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "process: writing to plugin stdin")
	}
	return nil
}
