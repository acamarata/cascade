package pbd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// Purpose (this file): the `pbd dispatch` command — the production caller
//
//	of the dispatch seam, and the real transport it reaches the daemon on.
//
// Inputs: a tree root, a ticket id, and the daemon socket to dial.
// Outputs: the model's output on the plugin's own command result, or a
//
//	typed refusal.
//
// Constraints: this package may import pkg/** and stdlib only, so the
//
//	transport is built here from net/http rather than taken from
//	internal/client (02-TARGET-STRUCTURE, depguard). It is a REAL
//	JSON-RPC 2.0 client over the daemon's unix socket — the same wire the
//	frozen SDK speaks — not a stand-in for one. pkg/provider.RPCCaller is
//	a single method precisely so a plugin can supply its own transport
//	without importing a core one.
//
// SPORT: plugins/pbd:dispatch-cmd (ADD) — P1-E14-W3-S30-T2.

// dispatchRPCTimeout bounds one model.execute round trip. A model call is
// long; this is a ceiling on a hung socket, not on thinking time.
const dispatchRPCTimeout = 30 * time.Minute

// socketRPCCaller is a real JSON-RPC 2.0 caller over a unix socket.
type socketRPCCaller struct {
	client *http.Client
}

// NewSocketCaller builds an RPCCaller dialing the daemon at socketPath.
func NewSocketCaller(socketPath string) provider.RPCCaller {
	return socketRPCCaller{client: &http.Client{
		Timeout: dispatchRPCTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

// rpcEnvelope is the JSON-RPC 2.0 response shape this caller decodes.
type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Do issues one JSON-RPC call and decodes its result into out.
func (c socketRPCCaller) Do(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params, "id": 1,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "pbd: encoding "+method)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/rpc", bytes.NewReader(body))
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "pbd: building "+method)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "pbd: calling "+method)
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeRPCResult(resp.Body, method, out)
}

// decodeRPCResult reads one response envelope and unpacks it.
//
// A server-reported error is returned as a typed error rather than left in
// the envelope: a caller that only checked the transport error would read a
// refused dispatch as a successful one with an empty result.
func decodeRPCResult(body interface{ Read([]byte) (int, error) }, method string, out any) error {
	var envelope rpcEnvelope
	if err := json.NewDecoder(body).Decode(&envelope); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "pbd: decoding the "+method+" response")
	}
	if envelope.Error != nil {
		return cascade.Newf(cascade.KindUnavailable, "pbd: %s: %s", method, envelope.Error.Message)
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "pbd: decoding the "+method+" result")
	}
	return nil
}

// runDispatchCommand services `pbd dispatch <tree-root> <ticket-id> <socket>`.
//
// It loads the ticket from the real tree rather than taking its fields on
// the command line: the ticket file is the source of truth for model_class
// and tasks, and accepting them as arguments would let a caller dispatch
// under a class the ticket does not declare.
func runDispatchCommand(ctx context.Context, args []string) error {
	if len(args) < 3 {
		return cascade.New(cascade.KindInvalidInput,
			"pbd dispatch: usage: pbd dispatch <tree-root> <ticket-id> <daemon-socket>")
	}
	ticket, err := findTicket(args[0], args[1])
	if err != nil {
		return err
	}
	// The result itself is the Dispatcher API's return; this command's
	// contract is the plugin ABI's, which is an error or nil. A refused
	// dispatch is therefore a typed failure the host reports, never a
	// silent success with an empty result.
	_, err = NewConductorDispatcher(NewSocketCaller(args[2])).Dispatch(ctx, ticket)
	return err
}

// findTicket loads one ticket out of the tree at root.
//
// The ticket FILE is the source of truth for model_class and tasks. They
// are deliberately not accepted as command arguments: a caller that could
// pass them could dispatch under a class the ticket does not declare, and
// the class decides which lane runs the work.
func findTicket(root, ticketID string) (*pews.Ticket, error) {
	tree, err := pews.NewStore(root, DefaultPhase).Load()
	if err != nil {
		return nil, err
	}
	for _, rec := range tree.Tickets {
		if rec.ID == ticketID {
			found := rec.Ticket
			return &found, nil
		}
	}
	return nil, cascade.Newf(cascade.KindNotFound,
		"pbd dispatch: no ticket %q in the tree at %s", ticketID, root)
}

// agenticIntentName is the intent a host raises to ask for agent-runtime
// execution of a ticket.
const agenticIntentName = "pbd.dispatch.agentic"

// agenticIntent answers the agentic-execution intent.
//
// handled is false for every other intent name, so the caller falls through
// to its own unknown-intent refusal. A host asking for agentic execution
// gets an actionable error naming the ticket that will provide it, never a
// silent no-op (Art.1): P1 dispatches through model.execute only
// (06 §5.18, R-16.12).
func agenticIntent(ctx context.Context, name string) (handled bool, err error) {
	if name != agenticIntentName {
		return false, nil
	}
	_, err = DispatchAgentic(ctx, nil)
	return true, err
}
