// Purpose: the `cascade mcp` subcommand group (07-CLI-COMMAND-TREE.md
//
//	§mcp) — serve [--stdio|--socket] and tools list, mounted on the D/S-
//	06.T1 root.
//
// Inputs: cobra args/flags; an mcpDeps injected at construction (Art.7.1 —
//
//	no test touches the real environment).
//
// Outputs: process output via internal/output.Writer for `tools list`;
//
//	the stdio transport writes MCP protocol frames to its own injected
//	stream, never through internal/output (MCP's wire framing is not CLI
//	text output — see stdioStreams' doc comment).
//
// Constraints: cmd/ is the sole composition root (Art.10.2) — internal/mcp
//
//	takes every dependency by injection. `tools list` is [local]-capable
//	per the 07 tree: it loads the registry in-process from compile-time
//	plugin manifests, no running daemon required.
//
// KNOWN WIRING GAP (see this ticket's completion report): `serve --socket`
// cannot register onto the daemon's own shared unix socket, because the
// daemon composition root (internal/daemon/daemon.go,
// internal/runtime/bootstrap.go, and their cmd/cascade siblings) was held
// by a concurrently dispatched ticket for the whole of this one and is
// outside this ticket's files_scope. Registering MCP as a method namespace
// on THAT socket — the design internal/mcp/transport/socket.go's doc
// comment describes — needs a call to transport.RegisterSocketMCP from
// inside the daemon's own registry construction, which this ticket cannot
// make land. `serve --socket` here instead binds its OWN dedicated unix
// socket (the daemon's SocketPath with an "-mcp" suffix) running the exact
// same transport.RegisterSocketMCP/rpc.Handler pipeline, so the transport
// itself is real and fully tested — only the "one shared socket" wiring is
// deferred.
//
// SPORT: cmd/cascade/mcp (ADD, per T-6 sport_updates).
package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// mcpDeps carries every external input the mcp command tree needs.
// ServeStdio and ServeSocket are the two transport entry points; tests
// inject fakes so neither a real stdin/stdout pipe nor a real unix socket
// bind is ever exercised outside their own dedicated internal/mcp/transport
// tests.
type mcpDeps struct {
	Paths    runtime.PathProvider
	NewTools func() *mcp.ToolRegistry
	// ServeStdio runs the stdio transport over in/out. RunE supplies
	// cobra's cmd.InOrStdin()/cmd.OutOrStdout() in production — these
	// default to the real os.Stdin/os.Stdout, resolved inside the cobra
	// dependency itself rather than named directly anywhere in this file:
	// internal/output.NewDefault's doc comment documents that it is the
	// ONLY place in cmd/** allowed to reference os.Stdout/os.Stderr
	// directly (D/S-06.T5's forbidigo rule, enforced across all of cmd/**,
	// and internal/build's outputgate.go AST gate enforces the same thing
	// whole-program) — mcpOutputWriter below reaches the real stdout the
	// same indirect way for `tools list`.
	ServeStdio  func(ctx context.Context, in io.Reader, out io.Writer) error
	ServeSocket func(ctx context.Context) error
	// StdinIsPipe reports whether stdin is a non-TTY pipe, per the
	// contract's "stdio default when stdin is a pipe" rule.
	StdinIsPipe func() bool
}

// productionMCPDeps builds mcpDeps against the real environment.
func productionMCPDeps() mcpDeps {
	paths := lazyPaths{}
	// The first-party v1-parity tool set (P1-E16-W4-S34-T2) and the
	// capability filter that gates it. Both are built per call, because
	// the filter is consulted once per registry and a grant change is
	// meant to take effect on the NEXT registration pass — which is this
	// call.
	tools := func() *mcp.ToolRegistry {
		bDeps := productionBackupDeps()
		wiring := buildMCPToolWiring(context.Background(), paths, runtime.SystemClock{})
		core := []mcp.CoreRegistration{
			backup.MCPRegistration(backupSnapshotLister(bDeps)),
			backup.VerifyMCPRegistration(backupVerifyRunner(bDeps)),
		}
		return mcp.NewToolRegistry(plugin.Builtins, wiring.Filter,
			append(core, coretools.Registrations(wiring.Methods)...)...)
	}
	return mcpDeps{
		Paths:    paths,
		NewTools: tools,
		ServeStdio: func(ctx context.Context, in io.Reader, out io.Writer) error {
			return serveStdioReal(ctx, tools(), in, out)
		},
		ServeSocket: func(ctx context.Context) error { return serveSocketReal(ctx, paths, tools()) },
		StdinIsPipe: stdinIsPipe,
	}
}

func stdinIsPipe() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) == 0
}

// serveStdioReal runs the stdio transport over in/out — the real process
// streams in production, injected by RunE rather than touched as bare
// os.Stdin/os.Stdout globals inside this file or internal/mcp/transport.
func serveStdioReal(ctx context.Context, tools *mcp.ToolRegistry, in io.Reader, out io.Writer) error {
	srv := mcp.NewServer(tools)
	tr := transport.NewStdioTransport(srv, in, out)
	return tr.Serve(ctx)
}

// serveSocketReal binds a dedicated MCP unix socket (see this file's doc
// comment for why it is not yet the daemon's shared socket) and serves
// mcp.dispatch over it via the same rpc.Registry/Handler pipeline the
// daemon itself uses.
func serveSocketReal(ctx context.Context, paths runtime.PathProvider, tools *mcp.ToolRegistry) error {
	sockPath := paths.SocketPath() + "-mcp"
	if sockPath == "-mcp" {
		return cascade.New(cascade.KindUnavailable, "cascade mcp serve --socket: could not resolve a socket path")
	}
	registry := rpc.NewRegistry()
	if err := transport.RegisterSocketMCP(registry, mcp.NewServer(tools)); err != nil {
		return err
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade mcp serve --socket: listen failed")
	}
	defer func() { _ = ln.Close() }()
	// ConnContext is required (P1-E04-W6-S146-T1): without it,
	// rpc.Handler's ConnContext-derived peerCred is never resolved for any
	// connection, so guardLocalRequest (request_guard.go) refuses every
	// request fail-closed — correct in direction, but this socket was
	// meant to serve its owner, not refuse them. Wiring the same
	// peer-credential resolver the daemon socket uses (handler.go's
	// ConnContext) makes this socket apply the identical owner-UID and
	// browser-shaped-request guard, never a laxer one.
	srv := &http.Server{Handler: rpc.NewHandler(registry), ConnContext: rpc.ConnContext}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade mcp serve --socket: serve failed")
	}
	return nil
}

// newMCPCmd builds the `mcp` command tree.
func newMCPCmd(deps mcpDeps) *cobra.Command {
	root := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the policy-filtered MCP tool registry",
	}
	root.AddCommand(newMCPServeCmd(deps))
	root.AddCommand(newMCPToolsCmd(deps))
	return root
}

func newMCPServeCmd(deps mcpDeps) *cobra.Command {
	var stdioFlag, socketFlag bool
	var captureDir string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server on the selected transport",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if stdioFlag && socketFlag {
				return cascade.New(cascade.KindInvalidInput, "--stdio and --socket are mutually exclusive")
			}
			useSocket := socketFlag
			if !stdioFlag && !socketFlag {
				useSocket = !deps.StdinIsPipe()
			}
			if useSocket {
				return deps.ServeSocket(cmd.Context())
			}
			in, out := cmd.InOrStdin(), cmd.OutOrStdout()
			if captureDir != "" {
				tappedIn, tappedOut, closeCapture, err := captureStreams(captureDir, in, out)
				if err != nil {
					return err
				}
				defer closeCapture()
				in, out = tappedIn, tappedOut
			}
			return deps.ServeStdio(cmd.Context(), in, out)
		},
	}
	cmd.Flags().BoolVar(&stdioFlag, "stdio", false, "serve MCP over line-framed stdio")
	cmd.Flags().BoolVar(&socketFlag, "socket", false, "serve MCP over the cascade unix socket")
	cmd.Flags().StringVar(&captureDir, "capture", "",
		"tee raw stdio frames to <dir>/{in,out}.jsonl (diagnostic; stdio only)")
	return cmd
}

func newMCPToolsCmd(deps mcpDeps) *cobra.Command {
	root := &cobra.Command{Use: "tools", Short: "Inspect the MCP tool registry"}
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Print the policy-filtered MCP tool registry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpOutputWriter(cmd).Result(mcpToolsListResult(deps))
		},
	})
	return root
}

// mcpOutputWriter mirrors config.go's outputWriter helper — this ticket
// has no shared home for it in files_scope, so it is duplicated locally
// rather than reaching into cmd/cascade/config (an unrelated package).
func mcpOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// mcpToolsListResult renders `mcp tools list`.
//
// It reports four lists, not one, because "why is tool X not here?" is
// the only interesting question this command answers and a bare list
// cannot answer it. A tool is absent for exactly one of three reasons,
// and each has its own field:
//
//   - withheld: the tool exists and its method is served, but the policy
//     engine does not grant its capability. `cascade policy grant <name>`
//     is the fix.
//   - unservable: this build does not serve the RPC method the tool
//     dispatches to, so no tool was registered at all. Nothing an
//     operator can grant will change that.
//   - deferred: the v1 tool has no v2 surface yet, with the ticket that
//     owns it. Nothing to grant, nothing to serve, and the reason is
//     recorded rather than left to be rediscovered.
//
// The wire surface deliberately says more than tools/list does: an
// operator at a terminal is not the untrusted model the registry withholds
// names from.
func mcpToolsListResult(deps mcpDeps) mcpToolsView {
	wiring := buildMCPToolWiring(context.Background(), deps.Paths, runtime.SystemClock{})
	defer wiring.Close()

	registry := deps.NewTools()
	deferred := make([]map[string]string, 0, len(coretools.Deferrals()))
	for _, d := range coretools.Deferrals() {
		deferred = append(deferred, map[string]string{"v1_name": d.V1Name, "ticket": d.Ticket, "reason": d.Reason})
	}
	return mcpToolsView{
		Tools:      registry.List(),
		Withheld:   registry.FilteredOut(),
		Unservable: coretools.Unservable(wiring.Methods),
		Deferred:   deferred,
	}
}
