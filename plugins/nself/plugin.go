// Package nself is the cascade-nself builtin plugin: nself-project
// DETECTION (detect.go), a doctor probe (doctor.go), and the nself
// handshake (handshake.go) proving the real server-profile diff over the
// verbs the installed nself CLI actually has.
//
// P1-E25-W5-S103-T1 REPLACES THE EARLIER FLOOR (S-52.T2). That ticket
// found the contract's `nself add cascade` and `nself project status
// --json` do not exist in v1.3.5 and shipped ONE typed refusal instead of
// a handshake. This ticket closes that gap against the CLOSEST real
// verbs, recorded read-only against the installed binary: `nself version
// --json` (one JSON object: buildDate/commit/goVersion/platform/version)
// and `nself config get <KEY>` (the raw value of one project .env key,
// non-zero exit when unset, secret values masked without --reveal). See
// handshake.go's header and plugins/nself/testdata/README.md for the
// full transcript provenance.
//
// TWO ENTRY POINTS, ONE HANDLER: nself_add_cascade (reachable by a model
// or agent) always runs in PROPOSE mode — it returns the diff and never
// calls ConfigApplier, so a tool invocation changes no host config. Only
// the human-invoked CLI command `cascade nself handshake` runs in APPLY
// mode. See handshake.go's runHandshake.
//
// RUNTIME TIER (T0 ruling, S-52.T2 PCI item A, unchanged by this ticket):
// the contract says `runtime = process, trust_tier = trusted`.
// pkg/plugin.Manifest has no trust_tier or net_scopes field, and no
// composition root in this tree can launch a RuntimeProcess manifest
// today. RuntimeBuiltin is therefore the floor, and it IS a security
// downgrade stated rather than glossed: this plugin runs IN the daemon
// process with full host trust and no supervised child. What it forks
// itself (bounded `nself version`/`config get`/`status` subprocesses) is
// an ordinary subprocess, bounded by a deadline and a process-group kill,
// not a host-launched process-tier plugin.
//
// REACHABILITY: internal/plugins/nself_wiring.go imports this package and
// binds the real egress engine AND the real ConfigApplier, so cascade-nself
// reaches plugin.Builtins() in the shipped binary. With no interceptor
// bound, every tool response refuses to emit; with no applier bound,
// APPLY mode refuses to write — never a silent pass-through.
//
// SPORT: plugins/nself entity (CHANGE) — P1-E25-W5-S103-T1.
package nself

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "cascade-nself"

// toolProjectInfo and toolAddCascade are the two tool names the contract's
// MANIFEST section names verbatim (bare, not cascade-nself.-prefixed).
// handshakeCommandName is the CLI verb `cascade nself handshake` mounts
// (cmd/cascade/plugin_namespaces.go's noun map: nself -> cascade-nself).
const (
	toolProjectInfo      = "nself_project_info"
	toolAddCascade       = "nself_add_cascade"
	handshakeCommandName = "handshake"
)

// init registers cascade-nself with the host's compile-time registry.
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns this plugin's cascade.plugin/v2 manifest, kept
// consistent with manifest.toml (plugin_test.go asserts this through the
// real loader).
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Cascade nSelf",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Tools: []plugin.ToolSpec{
				{Name: toolProjectInfo, Description: "Report whether a directory is an nself-managed project, and whether the nself CLI is reachable."},
				{Name: toolAddCascade, Description: "Propose the cascade server-profile diff for an nSelf project; writes nothing. Apply it with `cascade nself handshake`."},
			},
			Commands: []plugin.CommandSpec{
				{Name: handshakeCommandName, Description: "Apply the cascade server-profile diff for an nSelf project (writes config.toml)."},
			},
		},
		Requires: []string{"subprocess_exec"},
		Permissions: []plugin.PermissionDisplay{
			{Name: "subprocess_exec", Description: "Run one bounded, read-only nself status probe as a local subprocess in the scanned directory."},
		},
	}
}

// handlers is the real plugin.BuiltinHandlers implementation.
type handlers struct{}

func (handlers) DispatchTool(ctx context.Context, name string, input []byte) ([]byte, error) {
	switch name {
	case toolProjectInfo:
		return dispatchProjectInfo(ctx, input)
	case toolAddCascade:
		return dispatchAddCascade(ctx, input)
	default:
		return nil, cascade.Newf(cascade.KindNotFound, "cascade-nself: no such tool %q", name)
	}
}

func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindNotFound,
		"cascade-nself: no such intent %q: this plugin declares no intents", name)
}

// RunCommand services the one mounted CommandSpec: `cascade nself
// handshake`. args is the RAW token stream (plugin_namespaces.go disables
// cobra flag parsing on the generic outer mount for this noun); it is
// handed to newHandshakeCommand's OWN real --dir/--json flags via
// SetArgs, matching plugins/cascade-pa/commands.go's RunCommand ->
// pacmd.NewChatCommand precedent exactly.
func (handlers) RunCommand(ctx context.Context, name string, args []string) error {
	if name != handshakeCommandName {
		return cascade.Newf(cascade.KindNotFound,
			"cascade-nself: unknown command %q: this plugin declares no such command", name)
	}
	c := newHandshakeCommand()
	c.SetArgs(args)
	return c.ExecuteContext(ctx)
}

// dispatchAddCascade answers nself_add_cascade: always PROPOSE mode, so a
// tool invocation (reachable by a model or agent) never writes host
// config (TestHandshakeToolPathWritesNothing).
func dispatchAddCascade(ctx context.Context, input []byte) ([]byte, error) {
	root := rootDirOf(parseToolInput(input))
	resp, err := runHandshake(ctx, handshakeModePropose, root)
	if err != nil {
		return nil, err
	}
	return marshalThroughEgress(ctx, resp)
}

// toolInput is the optional JSON body nself_project_info accepts: an
// override root directory for the detection scan, for a caller that is not
// operating from the daemon's own working directory.
type toolInput struct {
	RootDir string `json:"root_dir"`
}

// parseToolInput decodes input, treating an unusable body as "no override"
// rather than an error: a detection question with a malformed body is
// still answerable about the working directory. It never panics
// (FuzzNselfToolInput).
func parseToolInput(input []byte) toolInput {
	var t toolInput
	if len(input) == 0 {
		return t
	}
	if err := json.Unmarshal(input, &t); err != nil {
		return toolInput{}
	}
	return t
}

// getwd resolves the daemon's working directory when no override is given.
// A package var so plugin_test.go can drive the failure branch.
var getwd = os.Getwd

// rootDirOf resolves the directory to scan.
func rootDirOf(t toolInput) string {
	if t.RootDir != "" {
		return t.RootDir
	}
	wd, err := getwd()
	if err != nil {
		return "."
	}
	return wd
}

// dispatchProjectInfo answers the detection question. It returns an error
// only when the firewall refuses to pass the response — never because the
// scanned directory turned out not to be an nself project.
func dispatchProjectInfo(ctx context.Context, input []byte) ([]byte, error) {
	root := rootDirOf(parseToolInput(input))
	res := newDetector(root).Detect(ctx)
	dr, derr := runDoctor(activeLocator, nselfBinary)
	return marshalThroughEgress(ctx, newProjectInfoResponse(res, dr, derr).scrubbed())
}

// marshalThroughEgress JSON-encodes v and passes the bytes through the
// active EgressInterceptor at TierInternal. With no interceptor bound this
// REFUSES; with the real one bound (internal/plugins/nself_wiring.go) the
// substitution and sensitivity passes run on every byte this plugin emits.
func marshalThroughEgress(ctx context.Context, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "cascade-nself: encode tool response")
	}
	return egressInterceptor.InterceptClass(ctx, EgressClassNselfBackend, TierInternal, data)
}

// newHandshakeCommand builds `cascade nself handshake`'s real cobra.Command
// (--dir, --json flags) so RunCommand can dispatch into it the same way
// plugins/cascade-pa/commands.go's RunCommand dispatches into
// pacmd.NewChatCommand: SetArgs + ExecuteContext, never a second,
// divergent argument parser. Output goes through cmd.OutOrStdout()
// (fmt.Fprint*), never a bare fmt.Println/os.Stdout — plugins/** library
// code may not import internal/output (Art.10.2) and is gated against
// writing the real streams directly (internal/build's output gate); an
// unconfigured cobra.Command's OutOrStdout() is the same sanctioned
// indirection printInChatReply already uses.
func newHandshakeCommand() *cobra.Command {
	var dir string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:  handshakeCommandName,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runHandshakeCommand(c, dir, jsonOut)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "the nSelf project directory (default: the working directory)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the machine-readable response")
	return cmd
}

// runHandshakeCommand is `cascade nself handshake`'s body: the ONLY
// caller that ever runs APPLY mode. No interactive prompt — a human
// running this CLI command is the consent (contract item 5).
func runHandshakeCommand(c *cobra.Command, dir string, jsonOut bool) error {
	if dir == "" {
		dir = rootDirOf(toolInput{})
	}
	resp, err := runHandshake(c.Context(), handshakeModeApply, dir)
	if err != nil {
		return err
	}
	out, encErr := marshalThroughEgress(c.Context(), resp)
	if encErr != nil {
		return encErr
	}
	w := c.OutOrStdout()
	if jsonOut {
		_, err := fmt.Fprintln(w, string(out))
		return err
	}
	if _, err := fmt.Fprintf(w, "cascade-nself handshake: %s (%d applied, %d unchanged, %d skipped)\n",
		resp.Status, len(resp.Applied), len(resp.Unchanged), len(resp.Skipped)); err != nil {
		return err
	}
	if resp.Note != "" {
		if _, err := fmt.Fprintln(w, resp.Note); err != nil {
			return err
		}
	}
	return printHandshakeLists(w, resp)
}

// printHandshakeLists prints the human-mode lines for withheld values,
// missing env-refs and the DSN shape to export.
func printHandshakeLists(w io.Writer, resp handshakeResponse) error {
	lines := []string{}
	if len(resp.Withheld) > 0 {
		lines = append(lines, "withheld (credential-shaped, not written): "+strings.Join(resp.Withheld, ", "))
	}
	if len(resp.MissingEnv) > 0 {
		lines = append(lines, "missing env-refs: "+strings.Join(resp.MissingEnv, ", "))
	}
	if resp.DSNShape != "" {
		lines = append(lines, postgresDSNEnvRef+" shape: "+resp.DSNShape)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
