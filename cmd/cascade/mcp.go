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
// `serve --socket` binds its own owner-only socket (the daemon SocketPath
// plus "-mcp"); the daemon serves mcp.dispatch on its own socket through
// wire_mcp.go. Caller-principal rules: mcp_scoped_forward.go.
//
// SPORT: cmd/cascade/mcp (ADD, per T-6 sport_updates).
package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/principal"
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
	// ServeStdio runs the stdio transport over in/out: cobra's
	// cmd.InOrStdin()/cmd.OutOrStdout() in production, never os.Stdout named
	// here (internal/output.NewDefault is the only cmd/** site allowed to;
	// forbidigo and internal/build's outputgate.go enforce it).
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
		return mcpServeTools(wiring, mcpPrincipalGetenv(os.LookupEnv), dialMCPDaemon(paths), plugin.Builtins, core)
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

// serveStdioReal runs the stdio transport over in/out (injected by RunE,
// never bare os.Stdin/os.Stdout). Every tool handler, builtin plugin tools
// included, gets the owner caller: Principal nil, OwnerCLI false.
func serveStdioReal(ctx context.Context, tools *mcp.ToolRegistry, in io.Reader, out io.Writer) error {
	srv := mcp.NewServer(tools)
	tr := transport.NewStdioTransport(srv, in, out)
	return tr.Serve(principal.WithCaller(ctx, principal.Caller{}))
}

// serveSocketReal binds a dedicated owner-only MCP unix socket and serves
// mcp.dispatch over it via the same rpc.Registry/Handler pipeline the
// daemon itself uses.
func serveSocketReal(ctx context.Context, paths runtime.PathProvider, tools *mcp.ToolRegistry) error {
	sockPath := paths.SocketPath() + "-mcp"
	if sockPath == "-mcp" {
		return cascade.New(cascade.KindUnavailable, "cascade mcp serve --socket: could not resolve a socket path")
	}
	// No custody session here: every elevated verb is refused before a handler.
	registry, err := buildSocketMCPRegistry(tools, daemon.RefuseAllElevation(runtime.NewSystemClock()))
	if err != nil {
		return err
	}
	// The daemon socket's owner-only bind: a planted symlink, a non-socket
	// or a foreign-owned socket is refused; the socket is 0600 before Serve.
	ln, err := runtime.ListenOwnerSocket(sockPath)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	// mcpSocketConnContext resolves the peer credential as rpc.ConnContext
	// does (without it guardLocalRequest refuses every request), so the
	// owner-UID and browser-shaped-request guard is the daemon's own.
	return serveRPCListener(ctx, ln, rpc.NewHandler(registry), mcpSocketConnContext)
}

// buildSocketMCPRegistry builds the MCP socket's registry: mcp.dispatch over
// tools behind mw (production passes RefuseAllElevation; never returned
// without one).
// The access policy has no Minter: a token peer is refused on every non-Open
// verb before any handler runs; stampMCPOwner hands each admitted request
// Caller{Principal: nil, OwnerCLI: false} (the owner-CLI marker is ignored).
func buildSocketMCPRegistry(tools *mcp.ToolRegistry, mw rpc.MiddlewareFunc) (*rpc.Registry, error) {
	registry := rpc.NewRegistry()
	registry.SetAccessPolicy(refuseAllTokens)
	registry.Use(stampMCPOwner)
	registry.Use(mw)
	if err := transport.RegisterSocketMCP(registry, mcp.NewServer(tools)); err != nil {
		return nil, err
	}
	return registry, nil
}

// stampMCPOwner re-stamps every admitted MCP socket request as the owner
// (Principal nil, OwnerCLI false); no Caller or a principal refuses.
func stampMCPOwner(_ string, next rpc.HandlerFunc) rpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		c, ok := principal.CallerFrom(ctx)
		if !ok || c.Principal != nil {
			return nil, errNoOwnerCaller
		}
		return next(principal.WithCaller(ctx, principal.Caller{}), params)
	}
}

// mcpSocketConnContext is rpc.ConnContext plus the owner caller a header-less
// MCP client is (real MCP clients send no Cascade header). Admission still
// refuses a token; stampMCPOwner clears OwnerCLI for an owner marker.
func mcpSocketConnContext(ctx context.Context, conn net.Conn) context.Context {
	return principal.WithCaller(rpc.ConnContext(ctx, conn), principal.Caller{})
}

// serveRPCListener serves h on ln until ctx ends, resolving each connection
// through connCtx.
func serveRPCListener(ctx context.Context, ln net.Listener, h http.Handler, connCtx connContextFunc) error {
	srv := &http.Server{Handler: h, ConnContext: connCtx, ReadHeaderTimeout: 10 * time.Second}
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
