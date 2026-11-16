// Purpose: controller-to-node calls over the node's up ssh tunnel. Call
//   opens direct-streamlocal to <Route.SocketDir>/node.sock on the SAME
//   authenticated session the tunnel holds and speaks one JSON-RPC 2.0
//   POST over it. No up tunnel means KindUnavailable and nothing is
//   dialled: there is no other transport.
// Constraints: tunnelled bytes are never read beyond the one response,
//   logged or journaled; the session is only ever the one the tunnel's
//   pinned-host-key dial produced.

package nodes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// NodeSocketName is the node agent's RPC socket under Route.SocketDir.
	NodeSocketName = "node.sock"
	// ControllerSocketName is the socket the tunnel remote-forwards under
	// Route.SocketDir; the node reaches the node-facing registry through it.
	ControllerSocketName = "controller.sock"

	// maxNodeReplyBytes bounds one node.sock response body.
	maxNodeReplyBytes = 1 << 20
)

// NodeChannel calls a node's node.sock over that node's up tunnel and keeps
// the tunnels in step with the device records (nodechannel_reconcile.go).
// Call rides only tunnels Reconcile started, whose sessions it tracks; a
// tunnel started on the Manager directly reads as no tunnel up.
type NodeChannel struct {
	m       *Manager
	records *RecordStore

	mu       sync.Mutex
	sessions map[string]Session // the live session of each tunnel this channel started
	deps     ReconcileDeps
}

// NewNodeChannel returns a channel over m's tunnels and records' routes.
func NewNodeChannel(m *Manager, records *RecordStore) *NodeChannel {
	return &NodeChannel{m: m, records: records, sessions: make(map[string]Session)}
}

// Caller binds Call to nodeID: the call shape the node signing and
// attestation-put paths take.
func (c *NodeChannel) Caller(nodeID string) func(ctx context.Context, method string, params, out any) error {
	return func(ctx context.Context, method string, params, out any) error {
		return c.Call(ctx, nodeID, method, params, out)
	}
}

// Call sends method(params) to nodeID's node.sock over its up tunnel and
// decodes the result into out (nil discards it). A JSON-RPC error object
// comes back as a cascade error of its Kind.
func (c *NodeChannel) Call(ctx context.Context, nodeID, method string, params, out any) error {
	session, route, err := c.upSession(nodeID)
	if err != nil {
		return err
	}
	body, err := encodeNodeRequest(method, params)
	if err != nil {
		return err
	}
	conn, err := session.DialUnix(path.Join(route.SocketDir, NodeSocketName))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	setConnDeadline(ctx, conn)
	raw, err := exchangeNodeRequest(ctx, conn, body)
	if err != nil {
		if ctx.Err() != nil {
			return cascade.Wrap(cascade.KindCanceled, ctx.Err(), "node channel: call canceled")
		}
		return err
	}
	return decodeNodeReply(raw, out)
}

// noTunnel is the refusal for a node without an up tunnel.
func noTunnel(nodeID string) error {
	return cascade.New(cascade.KindUnavailable, "node channel: no tunnel up for "+nodeID)
}

// upSession returns nodeID's live session and route, or noTunnel when the
// tunnel was never started, is not up, or the record holds no complete,
// usable route.
func (c *NodeChannel) upSession(nodeID string) (Session, Route, error) {
	if state, ok := c.m.State(nodeID); !ok || state != TunnelUp {
		return nil, Route{}, noTunnel(nodeID)
	}
	c.mu.Lock()
	session, ok := c.sessions[nodeID]
	c.mu.Unlock()
	if !ok || sessionEnded(session) {
		return nil, Route{}, noTunnel(nodeID)
	}
	_, route, err := c.records.Target(nodeID)
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && (kind == cascade.KindNotFound || kind == cascade.KindPermissionDenied) {
			return nil, Route{}, noTunnel(nodeID)
		}
		return nil, Route{}, err
	}
	return session, route, nil
}

func sessionEnded(s Session) bool {
	select {
	case <-s.Done():
		return true
	default:
		return false
	}
}

func encodeNodeRequest(method string, params any) ([]byte, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "node channel: encode call params")
	}
	body, err := json.Marshal(rpc.Request{JSONRPC: "2.0", Method: method, Params: raw, ID: json.RawMessage("1")})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "node channel: encode call")
	}
	return body, nil
}

// setConnDeadline applies ctx's deadline when conn supports one (the ssh
// channel does).
func setConnDeadline(ctx context.Context, conn Conn) {
	d, ok := ctx.Deadline()
	if !ok {
		return
	}
	if dc, can := conn.(interface{ SetDeadline(time.Time) error }); can {
		_ = dc.SetDeadline(d)
	}
}

// exchangeNodeRequest writes one POST /rpc and returns the response body.
func exchangeNodeRequest(ctx context.Context, conn Conn, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://node"+RPCPath, bytes.NewReader(body))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "node channel: build request")
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node channel: send request")
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node channel: read response")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeReplyBytes+1))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node channel: read response body")
	}
	if len(raw) > maxNodeReplyBytes {
		return nil, cascade.New(cascade.KindIntegrity, "node channel: response exceeds the size bound")
	}
	return raw, nil
}

// decodeNodeReply maps a JSON-RPC envelope to out or a typed error.
func decodeNodeReply(raw []byte, out any) error {
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *rpc.ErrorObject `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "node channel: malformed response")
	}
	if env.Error != nil {
		return rpcErrorToCascade(env.Error)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "node channel: decode call result")
	}
	return nil
}

// rpcErrorToCascade rebuilds the node's error with its Kind; the wire
// message already carries the "<kind>: " prefix, so it is stripped once
// and Error() reads exactly what the node sent.
func rpcErrorToCascade(eo *rpc.ErrorObject) error {
	kind, ok := cascade.KindFromJSONRPCCode(eo.Code)
	if !ok {
		kind = cascade.KindInternal
		if strings.HasPrefix(eo.Message, "method not found") {
			kind = cascade.KindNotFound
		}
	}
	return cascade.New(kind, strings.TrimPrefix(eo.Message, kind.String()+": "))
}
