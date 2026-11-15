// Purpose: the node-facing HTTP handler. It never goes through
// rpc.NewHandler, the local-socket guard or a peer-credential path.
// Inputs: one JSON-RPC POST from a node tunnel whose request context carries
// the channel identity.
// Outputs: a JSON-RPC envelope from the node-facing registry, or a refusal.
// Constraints: checks run in a fixed order. Caller identity headers are
// refused 400. An unregistered method gets method-not-found and goes nowhere
// else. A method not declared AccessNode is refused before its handler. Only
// then is the node principal stamped and the call dispatched. The body is
// capped at 1 MiB and there is no event stream.

package nodes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxNodeFacingBodyBytes caps a node-facing request body.
const maxNodeFacingBodyBytes = 1 << 20

// nodeFacingHandler serves node-facing JSON-RPC requests for f.
type nodeFacingHandler struct{ f *NodeFacing }

// ServeHTTP serves one node-facing request.
func (h nodeFacingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != rpc.RPCPath || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if carriesIdentityHeader(r.Header) {
		writeNodeFacing(w, http.StatusBadRequest, rpc.NewEnvelope(nil, nil, nodeFacingError(cascade.New(
			cascade.KindInvalidInput, "nodes: a node-facing request carries no caller identity header"))))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxNodeFacingBodyBytes+1))
	if err != nil || len(body) > maxNodeFacingBodyBytes {
		writeNodeFacing(w, http.StatusRequestEntityTooLarge, rpc.NewEnvelope(nil, nil, nodeFacingError(cascade.New(
			cascade.KindInvalidInput, "nodes: a node-facing request body is unreadable or exceeds 1 MiB"))))
		return
	}
	req, parseErr := rpc.Parse(body)
	if parseErr != nil {
		writeNodeFacing(w, http.StatusOK, rpc.NewEnvelope(nil, nil, parseErr))
		return
	}
	result, errObj := h.f.dispatch(r.Context(), req)
	writeNodeFacing(w, http.StatusOK, rpc.NewEnvelope(nodeFacingID(req), result, errObj))
}

// dispatch applies the registered, access and caller steps in order, then
// dispatches into the node-facing registry.
func (f *NodeFacing) dispatch(ctx context.Context, req *rpc.Request) (any, *rpc.ErrorObject) {
	if !f.registry.Registered(req.Method) {
		// The registry answers method-not-found before any middleware or
		// handler runs; the call is never forwarded anywhere else.
		return f.registry.Dispatch(ctx, req)
	}
	if access, ok := rpc.AccessOf(req.Method); !ok || access != rpc.AccessNode {
		return nil, nodeFacingError(cascade.Newf(cascade.KindPermissionDenied,
			"nodes: %s is not a node-facing verb", req.Method))
	}
	id, ok := ChannelIdentityFrom(ctx)
	if !ok || id.NodeID == "" {
		return nil, nodeFacingError(errChannelNodeMismatch)
	}
	ctx = principal.WithCaller(ctx, principal.Caller{
		Principal: &principal.Principal{Kind: principal.KindNode, ID: id.NodeID},
	})
	return f.registry.Dispatch(ctx, req)
}

// carriesIdentityHeader reports whether h names a principal token or the
// owner client marker. Neither means anything on a node tunnel.
func carriesIdentityHeader(h http.Header) bool {
	return len(h.Values(principal.HeaderToken)) > 0 || len(h.Values(principal.HeaderOwnerCLI)) > 0
}

// nodeFacingError maps a taxonomy error onto the wire error shape.
func nodeFacingError(err error) *rpc.ErrorObject {
	wire := cascade.NewRPCError(err)
	return &rpc.ErrorObject{Code: wire.Code, Message: wire.Message, Data: wire.Data}
}

// nodeFacingID echoes the request id; a notification has none.
func nodeFacingID(req *rpc.Request) any {
	if req.IsNotification() {
		return nil
	}
	return req.ID
}

// writeNodeFacing writes env with an explicit length, so a node reads exactly
// one response per request.
func writeNodeFacing(w http.ResponseWriter, status int, env *rpc.ResponseEnvelope) {
	body, err := json.Marshal(env)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"nodes: encode response"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
