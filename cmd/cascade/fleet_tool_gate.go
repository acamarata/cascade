// Purpose: the hidden `cascade fleet tool-gate --socket <path>` subcommand:
//
//	the whole body of the tool-gate hook. The harness runs it for
//	PreToolUse; it reads the harness's hook JSON from stdin once (at most
//	1 MiB), checks only that it is valid JSON, and forwards it verbatim as
//	the params of ONE JSON-RPC call over the daemon socket. The request
//	envelope is built by hand so the params bytes equal stdin byte for byte:
//	encoding/json would compact the object and escape <, > and &. The
//	command extracts nothing from stdin and reads no session id from the
//	environment; the daemon decides.
//
// Inputs: the --socket flag, stdin, a dial function (fleetSessionsDeps).
//
// Outputs: exit 0 only for a decoded `deny: false`. Every other outcome
//
//	(daemon down, socket missing, deadline, empty or non-JSON stdin, a
//	non-JSON or non-matching reply, a JSON-RPC error, a denial) is a one-line
//	invalid-input error, which is exit 2: the harness reads exit 2 as "block".
//	The rendered command's trailing `|| exit 2` covers a missing binary.
//
// Constraints: fail closed; one request under the tool gate client deadline;
//
//	the root pre-run is skipped (no daemon probe, no profile attach).
//
// SPORT: cmd/cascade/fleet-tool-gate (ADD).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

const (
	// toolGateMaxStdin bounds the harness JSON read from stdin.
	toolGateMaxStdin = 1 << 20
	// toolGateMaxReply bounds the daemon reply read from the socket.
	toolGateMaxReply = 1 << 20
	// toolGateRPCID is the id of the command's single JSON-RPC request.
	toolGateRPCID = "cascade-fleet-tool-gate"
)

// toolGateEnvelope is the JSON-RPC reply envelope. Error is kept raw: any
// non-null error member refuses, whatever its shape.
type toolGateEnvelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// toolGateReply is the typed result. Deny is a pointer so an omitted or null
// deny is distinguishable from an explicit false: only false allows.
type toolGateReply struct {
	Deny   *bool  `json:"deny"`
	Reason string `json:"reason"`
}

// newFleetToolGateCmd builds the hidden `tool-gate` command.
func newFleetToolGateCmd(deps fleetSessionsDeps) *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:    "tool-gate --socket <path>",
		Short:  "Ask the daemon whether a tool call may proceed (installed by the tool-gate hook pack)",
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
		// Skip the root pre-run: no daemon probe, no profile attach.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		SilenceUsage:      true,
		SilenceErrors:     true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runToolGate(cmd, deps, socket)
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return toolGateRefusal("invalid flag: " + err.Error())
	})
	cmd.Flags().StringVar(&socket, "socket", "", "daemon socket path")
	return cmd
}

// mountFleetToolGate attaches the hidden command to the `fleet` noun
// mountFleetCmd has just registered on root. It never creates a root noun.
func mountFleetToolGate(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == "fleet" {
			c.AddCommand(newFleetToolGateCmd(productionFleetSessionsDeps()))
			return
		}
	}
}

// runToolGate is the whole command: nil only for an explicit allow.
func runToolGate(cmd *cobra.Command, deps fleetSessionsDeps, socket string) error {
	if socket == "" {
		return toolGateRefusal("no daemon socket given")
	}
	deadline := time.Duration(hookpacks.ToolGateClientDeadline) * time.Second
	ctx, cancel := context.WithTimeout(cmd.Context(), deadline)
	defer cancel()
	params, err := readToolGateStdin(ctx, cmd.InOrStdin())
	if err != nil {
		return err
	}
	body, err := postToolGate(ctx, deps, socket, params)
	if err != nil {
		return err
	}
	return toolGateVerdict(body)
}

// readToolGateStdin reads stdin once under ctx and returns it unchanged when
// it is a non-empty JSON value within the size bound.
func readToolGateStdin(ctx context.Context, r io.Reader) ([]byte, error) {
	type read struct {
		raw []byte
		err error
	}
	done := make(chan read, 1)
	go func() {
		raw, err := io.ReadAll(io.LimitReader(r, toolGateMaxStdin+1))
		done <- read{raw, err}
	}()
	var got read
	select {
	case got = <-done:
	case <-ctx.Done():
		return nil, toolGateRefusal("request failed (timeout): stdin not closed")
	}
	switch {
	case got.err != nil:
		return nil, toolGateRefusal("stdin unreadable: " + got.err.Error())
	case len(got.raw) > toolGateMaxStdin:
		return nil, toolGateRefusal("stdin larger than 1 MiB")
	case len(bytes.TrimSpace(got.raw)) == 0:
		return nil, toolGateRefusal("empty stdin")
	case !json.Valid(got.raw):
		return nil, toolGateRefusal("stdin is not JSON")
	}
	return got.raw, nil
}

// toolGateRequest builds the JSON-RPC request with params spliced in as-is.
func toolGateRequest(params []byte) []byte {
	head := `{"jsonrpc":"2.0","id":"` + toolGateRPCID + `","method":"` + hookpacks.MethodToolGate + `","params":`
	out := make([]byte, 0, len(head)+len(params)+1)
	out = append(out, head...)
	out = append(out, params...)
	return append(out, '}')
}

// postToolGate makes the one POST over the unix socket and returns the
// reply body. The context deadline bounds dial, write and read.
func postToolGate(ctx context.Context, deps fleetSessionsDeps, socket string, params []byte) ([]byte, error) {
	httpClient := &http.Client{Transport: client.NewPrincipalTransport(&http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return deps.DialContext(ctx, socket)
		},
		DisableKeepAlives: true,
	})}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/rpc", bytes.NewReader(toolGateRequest(params)))
	if err != nil {
		return nil, toolGateRefusal("request failed: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, toolGateTransportRefusal(ctx, socket, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, toolGateMaxReply+1))
	if err != nil {
		return nil, toolGateTransportRefusal(ctx, socket, err)
	}
	if len(body) > toolGateMaxReply {
		return nil, toolGateRefusal("response malformed: larger than 1 MiB")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, toolGateRefusal("response malformed: HTTP status " + resp.Status)
	}
	return body, nil
}

// toolGateTransportRefusal names a deadline as a timeout and every other
// transport failure as an unreachable daemon.
func toolGateTransportRefusal(ctx context.Context, socket string, err error) error {
	if ctx.Err() != nil {
		return toolGateRefusal("request failed (timeout): " + err.Error())
	}
	return toolGateRefusal("request failed (unavailable): daemon not running or unreachable at " + socket + ": " + err.Error())
}

// toolGateVerdict decodes the reply: nil for an explicit deny=false in a
// matching, error-free envelope; a one-line refusal for everything else.
func toolGateVerdict(body []byte) error {
	var env toolGateEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return toolGateRefusal("response malformed: " + err.Error())
	}
	if len(env.Error) != 0 && string(env.Error) != "null" {
		return toolGateRefusal("daemon error: " + string(env.Error))
	}
	if env.ID != toolGateRPCID {
		return toolGateRefusal("response malformed: reply id does not match the request")
	}
	var reply toolGateReply
	if err := json.Unmarshal(env.Result, &reply); err != nil {
		return toolGateRefusal("response malformed: " + err.Error())
	}
	switch {
	case reply.Deny == nil:
		return toolGateRefusal("response malformed: no explicit deny")
	case !*reply.Deny:
		return nil
	case reply.Reason == "":
		return toolGateRefusal("denied")
	}
	return toolGateRefusal("denied: " + reply.Reason)
}

// toolGateRefusal is the one-line exit-2 error, prefixed with the capability.
func toolGateRefusal(reason string) error {
	return completionRefusal(hookpacks.ToolGateCapability + ": " + reason)
}
