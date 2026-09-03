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

	"github.com/acamarata/cascade/internal/mcp"
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
	tools := func() *mcp.ToolRegistry { return mcp.NewToolRegistry(plugin.Builtins) }
	paths := lazyPaths{}
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
	srv := &http.Server{Handler: rpc.NewHandler(registry)}
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
			return deps.ServeStdio(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&stdioFlag, "stdio", false, "serve MCP over line-framed stdio")
	cmd.Flags().BoolVar(&socketFlag, "socket", false, "serve MCP over the cascade unix socket")
	return cmd
}

func newMCPToolsCmd(deps mcpDeps) *cobra.Command {
	root := &cobra.Command{Use: "tools", Short: "Inspect the MCP tool registry"}
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Print the policy-filtered MCP tool registry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := mcpOutputWriter(cmd)
			return w.Result(map[string]any{"tools": deps.NewTools().List()})
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
