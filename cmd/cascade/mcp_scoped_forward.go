// Purpose: caller-principal rules for the in-process MCP servers (`cascade
//
//	mcp serve` over stdio and --socket). A process that carries a caller
//	principal token serves no gated method from its own store: each one is
//	forwarded to the daemon with the token, and refused when the daemon is
//	unreachable. The MCP socket admits no principal at all: its access
//	policy has no Minter, and every peer it does admit is stamped as the
//	owner (never the owner CLI).
//
// Inputs: the in-process method registry, an injected getenv and a daemon
//
//	dialer (tests inject both; production passes the real environment and
//	internal/client).
//
// Outputs: re-bound handlers on the registry; the socket access policy,
//
//	caller stamp middleware and connection context.
//
// Constraints: fail closed. A method's access class is read from the
//
//	internal/rpc declarations through the registry's own admission check,
//	never from a list kept here; an undeclared method refuses. A dial or
//	transport failure refuses KindUnavailable and never falls back to the
//	in-process handler. Without a token the process is the owner, exactly
//	like the owner CLI (same uid is not a boundary).
//
// SPORT: cmd/cascade/mcp principal mode [ADD].
package main

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// mcpForwardTimeout bounds one forwarded call. A recall over a large store
// is the slowest read these tools make.
const mcpForwardTimeout = 2 * time.Minute

// emptyTokenMarker is what mcpPrincipalGetenv reports for a token variable
// that is present but empty. The client SDK sends a present token whatever
// its value, so an empty one is a principal-bearing process too (the daemon
// refuses its empty credential); it must never read as the owner.
const emptyTokenMarker = "present-but-empty"

// mcpServeTools composes the tool registry `cascade mcp serve` serves.
// Without a principal token it is the owner view: builtin plugin tools, the
// in-process core tools and the first-party tools over wiring.Methods. With
// one, wiring.Methods is put in principal mode (each gated method forwarded
// to the daemon, refused when it is unreachable) and the in-process tools
// that bypass the method table (builtin plugins, the core registrations) are
// withheld: they would answer from the owner's own data. The token is read
// once, here: the process stays in the mode it started in.
func mcpServeTools(wiring mcpToolWiring, getenv func(string) string, dial func(token string) (*client.Client, error),
	builtins mcp.ManifestSource, core []mcp.CoreRegistration) *mcp.ToolRegistry {
	if getenv == nil {
		return mcp.NewToolRegistry(noBuiltinTools, mcp.DenyAllFilter{})
	}
	token := getenv(principal.EnvToken)
	if token != "" {
		bound := func(key string) string {
			if key == principal.EnvToken {
				return token
			}
			return ""
		}
		if err := applyPrincipalMode(wiring.Methods, bound, dial); err != nil {
			return mcp.NewToolRegistry(noBuiltinTools, mcp.DenyAllFilter{})
		}
		return mcp.NewToolRegistry(noBuiltinTools, wiring.Filter, coretools.Registrations(wiring.Methods)...)
	}
	return mcp.NewToolRegistry(builtins, wiring.Filter, append(core, coretools.Registrations(wiring.Methods)...)...)
}

// noBuiltinTools is the manifest source of a principal-bearing process.
func noBuiltinTools() []plugin.BuiltinRegistration { return nil }

// daemonCaller is the one client operation the forwarder needs;
// *client.Client satisfies it.
type daemonCaller interface {
	Do(ctx context.Context, method string, params any, out any) error
}

var _ daemonCaller = (*client.Client)(nil)

// connContextFunc resolves per-connection context (http.Server.ConnContext).
type connContextFunc = func(context.Context, net.Conn) context.Context

// mcpHandler is the method handler shape of the MCP method table
// (rpc.HandlerFunc's underlying type; mcp_tools.go registers these).
type mcpHandler = func(ctx context.Context, params json.RawMessage) (any, error)

// scopedForwarder forwards method and its params to the daemon through c.
// c carries the principal token bound when the mode was chosen
// (client.NewWithToken never reads the environment), so the daemon applies
// that caller's scope. A transport failure is the client's own KindUnavailable
// (or KindTimeout/KindCanceled); no in-process handler is ever consulted.
func scopedForwarder(c daemonCaller, method string) mcpHandler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var out json.RawMessage
		var sent any
		if len(params) > 0 {
			sent = params
		}
		if err := c.Do(ctx, method, sent, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}

func refuseUnclassed(method string) mcpHandler {
	return func(context.Context, json.RawMessage) (any, error) {
		return nil, cascade.Newf(cascade.KindPermissionDenied,
			"mcp: %s has no caller-principal access class; refused for a principal-bearing process", method)
	}
}

func refuseUnreachable(method string, cause error) mcpHandler {
	return func(context.Context, json.RawMessage) (any, error) {
		return nil, cascade.Wrapf(cascade.KindUnavailable, cause,
			"mcp: %s: daemon unreachable; a principal-bearing process serves no in-process read", method)
	}
}

// errEmptyToken refuses every classed method of a process whose token
// variable is present but empty: it is principal-bearing, with no credential.
var errEmptyToken = cascade.New(cascade.KindPermissionDenied,
	"mcp: the caller principal token is empty; refused")

func refuseWith(err error) mcpHandler {
	return func(context.Context, json.RawMessage) (any, error) { return nil, err }
}

// mcpPrincipalGetenv adapts a lookup to the getenv applyPrincipalMode takes,
// reporting a present but empty token as non-empty (see emptyTokenMarker).
func mcpPrincipalGetenv(lookup func(string) (string, bool)) func(string) string {
	return func(key string) string {
		v, ok := lookup(key)
		if key == principal.EnvToken && ok && v == "" {
			return emptyTokenMarker
		}
		return v
	}
}

// dialMCPDaemon returns the production dialer over the daemon socket. It
// fails when no socket path resolves.
func dialMCPDaemon(paths runtime.PathProvider) func(token string) (*client.Client, error) {
	return func(token string) (*client.Client, error) {
		if paths == nil || paths.SocketPath() == "" {
			return nil, cascade.New(cascade.KindUnavailable, "mcp: could not resolve the daemon socket path")
		}
		return dialMCPDaemonAt(paths.SocketPath())(token)
	}
}

// dialMCPDaemonAt binds token into an internal/client for sock. The token is
// passed explicitly and never re-read from the environment.
func dialMCPDaemonAt(sock string) func(token string) (*client.Client, error) {
	return func(token string) (*client.Client, error) {
		if token == "" {
			return nil, errEmptyToken
		}
		return client.NewWithToken(sock, client.UnixDialer, mcpForwardTimeout, token), nil
	}
}

// errOwnerOnlyTool refuses a daemon MCP tool that answers from the owner's
// own data without passing through the method registry's access check.
var errOwnerOnlyTool = cascade.New(cascade.KindPermissionDenied, "mcp: owner-only tool")

// requireOwnerCaller admits only a transport-stamped owner caller (no
// principal); a missing Caller refuses.
func requireOwnerCaller(ctx context.Context) error {
	if c, ok := principal.CallerFrom(ctx); !ok || c.Principal != nil {
		return errOwnerOnlyTool
	}
	return nil
}

// ownerOnlyHandlers gates every entry point of a builtin plugin on the owner.
type ownerOnlyHandlers struct{ next plugin.BuiltinHandlers }

func (h ownerOnlyHandlers) DispatchTool(ctx context.Context, name string, input []byte) ([]byte, error) {
	if err := requireOwnerCaller(ctx); err != nil {
		return nil, err
	}
	return h.next.DispatchTool(ctx, name, input)
}

func (h ownerOnlyHandlers) DispatchIntent(ctx context.Context, name string, input []byte) ([]byte, error) {
	if err := requireOwnerCaller(ctx); err != nil {
		return nil, err
	}
	return h.next.DispatchIntent(ctx, name, input)
}

func (h ownerOnlyHandlers) RunCommand(ctx context.Context, name string, args []string) error {
	if err := requireOwnerCaller(ctx); err != nil {
		return err
	}
	return h.next.RunCommand(ctx, name, args)
}

// ownerOnlyBuiltins returns source with every registration's handlers gated
// on the owner caller. It copies the slice: plugin.Builtins is shared.
func ownerOnlyBuiltins(source mcp.ManifestSource) mcp.ManifestSource {
	return func() []plugin.BuiltinRegistration {
		regs := source()
		out := make([]plugin.BuiltinRegistration, len(regs))
		copy(out, regs)
		for i := range out {
			if out[i].Handlers != nil {
				out[i].Handlers = ownerOnlyHandlers{out[i].Handlers}
			}
		}
		return out
	}
}

// ownerOnlyCore gates each core registration's handler on the owner caller.
func ownerOnlyCore(core []mcp.CoreRegistration) []mcp.CoreRegistration {
	out := make([]mcp.CoreRegistration, len(core))
	for i, reg := range core {
		out[i] = reg
		if next := reg.Handler; next != nil {
			out[i].Handler = func(ctx context.Context, input []byte) ([]byte, error) {
				if err := requireOwnerCaller(ctx); err != nil {
					return nil, err
				}
				return next(ctx, input)
			}
		}
	}
	return out
}

// refuseAllTokens is the MCP socket's principal resolver: this socket has no
// Minter, so every presented token is refused before any handler runs.
func refuseAllTokens(string) (principal.Principal, error) {
	return principal.Principal{}, cascade.New(cascade.KindPermissionDenied,
		"mcp socket: caller principals are not admitted here")
}

// errNoOwnerCaller refuses an MCP socket request that reached a handler
// without an admitted owner caller.
var errNoOwnerCaller = cascade.New(cascade.KindPermissionDenied, "mcp socket: no admitted owner caller")
