// Purpose: NodeFacing, the controller's node-only registry. It carries the
// node-originated verbs (node.channel.hello, node.heartbeat and
// node.dispatch.claim, report and journal) and nothing an owner calls.
// Inputs: the node identity of the tunnel a request arrived on (stamped by
// DialAs, never read from params) and the live device records, sequence
// store, rendezvous, journal sink, clock and installation signer.
// Outputs: the node-facing registry and a pipe dialer that serves one
// connection as a given channel node.
// Constraints: the calling node comes from the channel identity only, and a
// param naming another node is refused. Every verb rechecks the live device
// record first: a removed or revoked node, or a claim from a drained node,
// is refused before the handler runs, so nothing changes. Later
// node-originated verbs register on this registry only.

package nodes

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

const (
	nodeHeartbeatMethod    = "node.heartbeat"
	nodeChannelHelloMethod = "node.channel.hello"

	// nodeFacingReadHeaderTimeout bounds how long a node may take to send
	// its request headers.
	nodeFacingReadHeaderTimeout = 10 * time.Second
)

var _ = rpc.DeclareAccess(nodeHeartbeatMethod, rpc.AccessNode)

// paramsNodeIDExempt lists the verbs whose params carry no node_id, so the
// generic params check cannot apply. The hello signs over the channel
// identity and its params hold only a nonce. A journal record has no node_id
// field yet; its own channel check arrives with that field.
var paramsNodeIDExempt = map[string]bool{
	nodeChannelHelloMethod: true,
	DispatchJournalMethod:  true,
}

// errChannelNodeMismatch is the one refusal for an absent, empty or
// different channel node.
var errChannelNodeMismatch = cascade.New(cascade.KindPermissionDenied, "channel node mismatch")

// ChannelIdentity is the node identity of the tunnel a request arrived on.
type ChannelIdentity struct {
	NodeID string
}

type channelIdentityKey struct{}

// WithChannelIdentity binds the tunnel's node identity to ctx. Only the
// node-facing connection setup calls it.
func WithChannelIdentity(ctx context.Context, id ChannelIdentity) context.Context {
	return context.WithValue(ctx, channelIdentityKey{}, id)
}

// ChannelIdentityFrom returns the channel identity bound to ctx, if any.
func ChannelIdentityFrom(ctx context.Context) (ChannelIdentity, bool) {
	id, ok := ctx.Value(channelIdentityKey{}).(ChannelIdentity)
	return id, ok
}

// RequireChannelNode refuses unless ctx carries a non-empty channel identity
// equal to claimed. An empty claimed id names no node and is refused too.
func RequireChannelNode(ctx context.Context, claimed string) error {
	id, ok := ChannelIdentityFrom(ctx)
	if !ok || id.NodeID == "" || claimed == "" || claimed != id.NodeID {
		return errChannelNodeMismatch
	}
	return nil
}

// channelNodeOf adapts ChannelIdentityFrom to the hello handler's identity seam.
func channelNodeOf(ctx context.Context) (string, bool) {
	id, ok := ChannelIdentityFrom(ctx)
	return id.NodeID, ok && id.NodeID != ""
}

// NodeFacingDeps are the collaborators the node-facing verbs need.
type NodeFacingDeps struct {
	Records    *RecordStore
	Sequences  *SequenceStore
	Rendezvous *Rendezvous
	Journal    JournalStreamDeps
	Clock      Clock
	// Timeout is the liveness freshness window; zero means the default.
	Timeout time.Duration
	// Signer is the installation signer that answers node.channel.hello.
	Signer envelope.Signer
}

// NodeFacing is the node-only registry and the server that feeds it.
type NodeFacing struct {
	deps     NodeFacingDeps
	registry *rpc.Registry

	once  sync.Once
	conns chan net.Conn
}

// NewNodeFacing builds the node-facing registry over d. The existing
// handlers are mounted as they are; the guard middleware adds the channel
// and record checks around each of them, and around any verb registered
// later through Registry.
func NewNodeFacing(d NodeFacingDeps) *NodeFacing {
	if d.Rendezvous == nil {
		// No dispatch is ever published on an empty rendezvous, so every
		// claim and report refuses instead of dereferencing nil.
		d.Rendezvous = NewRendezvous()
	}
	f := &NodeFacing{deps: d, registry: rpc.NewRegistry(), conns: make(chan net.Conn)}
	f.registry.Use(f.guard)
	f.registry.Register(nodeChannelHelloMethod, controllerHelloHandler(d.Signer, channelNodeOf))
	RegisterHeartbeatHandler(f.registry, HeartbeatDeps{
		Records: d.Records, Sequences: d.Sequences, Clock: d.Clock, Timeout: d.Timeout,
	})
	RegisterDispatchNodeHandlers(f.registry, d.Rendezvous)
	RegisterDispatchJournalHandler(f.registry, d.Journal)
	return f
}

// Registry returns the node-facing registry. Later node-originated verbs
// register here and never on the owner registry.
func (f *NodeFacing) Registry() *rpc.Registry { return f.registry }

// guard runs before every node-facing handler: the channel check, then the
// live record check. A refusal returns before the handler, so no state moves.
func (f *NodeFacing) guard(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		id, ok := ChannelIdentityFrom(ctx)
		if !ok {
			return nil, errChannelNodeMismatch
		}
		claimed := id.NodeID
		if !paramsNodeIDExempt[method] {
			claimed = paramsNodeID(params)
		}
		if err := RequireChannelNode(ctx, claimed); err != nil {
			return nil, err
		}
		if err := f.recheck(id.NodeID, method); err != nil {
			return nil, err
		}
		return next(ctx, params)
	}
}

// paramsNodeID reads the node_id a call's params name. Params that do not
// decode name no node, which RequireChannelNode refuses.
func paramsNodeID(params json.RawMessage) string {
	var p struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	return p.NodeID
}

// recheck loads the live device record for nodeID. A removed or revoked node
// is refused for every verb; a drained node is refused only new work, so
// heartbeats and reports of fenced work still land and drain can finish.
func (f *NodeFacing) recheck(nodeID, method string) error {
	if f.deps.Records == nil {
		return cascade.New(cascade.KindPermissionDenied, "nodes: node-facing calls need the device records")
	}
	rec, err := f.deps.Records.Get(nodeID)
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindNotFound {
			return cascade.Newf(cascade.KindPermissionDenied, "nodes: node %q is not enrolled", nodeID)
		}
		return err
	}
	if SignatureRevoked(rec, rec.PubKeyB64) {
		return cascade.Newf(cascade.KindPermissionDenied, "nodes: node %q is revoked", nodeID)
	}
	if rec.Drained && method == DispatchClaimMethod {
		return cascade.Newf(cascade.KindPermissionDenied, "nodes: node %q is drained and takes no new work", nodeID)
	}
	return nil
}

// DialAs returns a dialer for TunnelConfig.LocalDial. Each call opens an
// in-process pipe whose far end the node-facing server reads with nodeID as
// the channel identity. No socket exists on the controller's filesystem.
func (f *NodeFacing) DialAs(nodeID string) func(ctx context.Context) (Conn, error) {
	return func(ctx context.Context) (Conn, error) {
		if nodeID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "nodes: a node-facing dial needs a node id")
		}
		if ctx.Err() != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, ctx.Err(), "nodes: node-facing dial canceled")
		}
		f.once.Do(f.serve)
		client, server := net.Pipe()
		select {
		case f.conns <- channelConn{Conn: server, nodeID: nodeID}:
			return client, nil
		case <-ctx.Done():
			_ = client.Close()
			_ = server.Close()
			return nil, cascade.Wrap(cascade.KindCanceled, ctx.Err(), "nodes: node-facing dial canceled")
		}
	}
}

// serve starts the one node-facing HTTP server over the pipe listener.
func (f *NodeFacing) serve() {
	srv := &http.Server{
		Handler:           nodeFacingHandler{f: f},
		ReadHeaderTimeout: nodeFacingReadHeaderTimeout,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if cc, ok := c.(channelConn); ok {
				return WithChannelIdentity(ctx, ChannelIdentity{NodeID: cc.nodeID})
			}
			return ctx
		},
	}
	go func() { _ = srv.Serve(pipeListener{conns: f.conns}) }()
}

// channelConn is the server end of one DialAs pipe and the node it serves.
type channelConn struct {
	net.Conn
	nodeID string
}

// pipeListener hands the server the pipes DialAs opens.
type pipeListener struct{ conns chan net.Conn }

func (l pipeListener) Accept() (net.Conn, error) { return <-l.conns, nil }
func (l pipeListener) Close() error              { return nil }
func (l pipeListener) Addr() net.Addr            { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "node-facing" }
