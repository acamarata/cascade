// Purpose: `cascade status` (07-CLI-COMMAND-TREE §status: "one-shot
//
//	summary ✦; rpc: status.get"), the read surface a user or CI script
//	dials first against the daemon. This file is the CLI-side half: a
//	cobra subcommand that dials the daemon through the Go IPC client SDK
//	(internal/client, D/S-07.T3) — never a hand-rolled JSON-RPC request of
//	its own (hard requirement 1; enforced by .golangci.yml's
//	cmd-rpc-server-boundary depguard rule) — and renders the response
//	through internal/output - a human table by default, the versioned
//	--json envelope with --json.
//
// Inputs: cobra args/flags; a statusDeps injected at construction so no
//
//	test touches the real environment or a real socket (Art.7.1).
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error (daemon unreachable, decode failure) on failure.
//
// Constraints: read-only verb - never prompts, and CASCADE_NO_INPUT has no
//
//	effect on it (automation parity, §5.8). Never writes to
//	os.Stdout/os.Stderr directly (internal/output's contract, enforced by
//	forbidigo + internal/build's AST output gate).
//
// SPORT: cmd/cascade/status (CHANGE, per T-3 sport_updates — routed
//
//	through internal/client.Client).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// statusDialTimeout bounds the whole dial+request+response round trip
// against the daemon's local unix socket. A fixed duration literal, not a
// clock read (Art.7.3 governs bare time.Now/After/Tick/Sleep in non-test
// code; a static http.Client.Timeout value is neither).
const statusDialTimeout = 5 * time.Second

// statusDeps carries every external input `cascade status` needs, mirroring
// daemonDeps's established injection pattern so no test touches the real
// environment or a real socket.
type statusDeps struct {
	Paths   runtime.PathProvider
	Getenv  runtime.Getenv
	Environ func() []string
	// DialContext resolves the transport-level connection to the daemon's
	// socket. Production dials the real unix socket at socketPath; tests
	// substitute an in-memory or loopback dialer against a fake server.
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
}

// productionStatusDeps builds statusDeps against the real environment.
func productionStatusDeps() statusDeps {
	return statusDeps{
		Paths:   lazyPaths{},
		Getenv:  os.Getenv,
		Environ: os.Environ,
		// The SDK's own dialer, so the daemon socket is reached one way
		// from every command rather than by a re-derived local copy.
		DialContext: client.UnixDialer,
	}
}

// mountStatusCmd attaches the top-level `status` command, following
// mountDaemonCmd's exact pattern.
func mountStatusCmd(root *cobra.Command) {
	root.AddCommand(newStatusCmd(productionStatusDeps()))
}

// newStatusCmd builds the `status` command.
func newStatusCmd(deps statusDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "One-shot summary of the running daemon",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := fetchStatus(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return statusOutputWriter(cmd).Result(statusHumanView{resp})
		},
	}
}

// fetchStatus resolves the daemon's socket path from config and calls
// status.get through internal/client.Client — the ONLY way this file
// reaches the daemon (hard requirement 1). No JSON-RPC request/response
// framing is assembled here; that lives entirely in internal/client, and
// its own tests (client_integration_test.go) prove it against a real
// daemon-shaped socket.
func fetchStatus(ctx context.Context, deps statusDeps) (daemon.StatusResponse, error) {
	settings, err := resolveStatusSocket(ctx, deps)
	if err != nil {
		return daemon.StatusResponse{}, err
	}

	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), statusDialTimeout)
	return c.Status(ctx)
}

// resolveStatusSocket loads config.toml (the single resolution model every
// CLI command uses, 08 §2) and resolves the daemon's socket path from it,
// falling back to the path provider's derived default.
func resolveStatusSocket(ctx context.Context, deps statusDeps) (daemon.Settings, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return daemon.Settings{}, cascade.Wrap(cascade.KindInvalidInput, err, "cascade status: load config.toml")
	}
	settings, err := daemon.ResolveSettings(cfg, deps.Paths)
	if err != nil {
		return daemon.Settings{}, err
	}
	return settings, nil
}

// rpcErrorObject is the subset of the JSON-RPC 2.0 error member this
// command needs to decode.
type rpcErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// decodeStatusEnvelope decodes body as a JSON-RPC 2.0 response envelope and
// extracts status.get's result. Takes an io.Reader (a response body, or a
// plain strings.Reader in a unit test) rather than *http.Response, so the
// decoding logic itself is testable without an "net/http" import - that
// import is reserved for this file's real dial path and the integration-
// tagged command tests that exercise it (internal/build's no-network-unit-
// lane gate, Art.7.2).
func decodeStatusEnvelope(body io.Reader) (daemon.StatusResponse, error) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcErrorObject `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&envelope); err != nil {
		return daemon.StatusResponse{}, cascade.Wrap(cascade.KindInternal, err, "cascade status: decode response")
	}
	if envelope.Error != nil {
		return daemon.StatusResponse{}, cascade.Newf(cascade.KindInternal,
			"cascade status: daemon returned error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	var result daemon.StatusResponse
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return daemon.StatusResponse{}, cascade.Wrap(cascade.KindInternal, err, "cascade status: decode result")
	}
	return result, nil
}

// statusHumanView wraps daemon.StatusResponse so this package can attach a
// human table String() method - StatusResponse itself lives in
// internal/daemon and cannot gain a method from cmd/cascade. The
// embedding is anonymous so json.Marshal(statusHumanView{...}) promotes
// StatusResponse's own fields (version/daemon/health) unchanged: the
// --json envelope's data shape is identical to StatusResponse's, per this
// ticket's AC.
type statusHumanView struct {
	daemon.StatusResponse
}

// String renders statusHumanView as the default human-readable table.
func (v statusHumanView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	// bytes.Buffer never errors on Write, so tabwriter's writes through it
	// never fail either; the errcheck lint rule still requires the
	// returned error be handled, so each line discards it explicitly.
	_, _ = fmt.Fprintf(tw, "FIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "version\t%s\n", v.Version)
	_, _ = fmt.Fprintf(tw, "pid\t%d\n", v.Daemon.PID)
	_, _ = fmt.Fprintf(tw, "uptime_s\t%.3f\n", v.Daemon.UptimeS)
	_, _ = fmt.Fprintf(tw, "connections\t%d\n", v.Daemon.Connections)
	_, _ = fmt.Fprintf(tw, "socket_path\t%s\n", v.Daemon.SocketPath)
	_, _ = fmt.Fprintf(tw, "health\t%s\n", v.Health)
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// statusOutputWriter mirrors daemon.go's outputWriter helper - duplicated
// per this file's established local convention (mcp.go's mcpOutputWriter
// does the same) rather than reaching into an unrelated package.
func statusOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
