// Purpose: `cascade memory`'s daemon plumbing — resolving the socket from
//
//	config.toml, performing one RPC through the injected call seam, and
//	building the command's output.Writer. Split from memory.go under
//	Art.10.3's 300-line file cap (P1-E07-W2-S14-T3); it is the same
//	composition the verbs already used, moved rather than changed.
//
// Inputs: the cobra command in flight and the memoryDeps injected at
//
//	construction.
//
// Outputs: a decoded RPC result, or a taxonomy error scrubbed of machine
//
//	paths and secret-shaped values.
//
// Constraints: scrubbing happens HERE, at the one boundary every verb
//
//	passes through, so no verb can forget to do it.
//
// SPORT: cmd.cascade.cmd.memory (CHANGED, P1-E07-W2-S14-T3).
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memoryCall resolves the daemon socket and performs one RPC, scrubbing
// every error on the way out. Scrubbing happens HERE, at the boundary,
// rather than at each call site, so no verb can forget to do it.
func memoryCall(cmd *cobra.Command, deps memoryDeps, method string, params, out any) error {
	return scrubDiagnostic(memoryCallRaw(cmd, deps, method, params, out))
}

// memoryCallRaw performs the same call and returns the error UNSCRUBBED,
// for one caller: `memory soul edit`, which must ask whether the failure
// was "no soul document yet" before starting the user from an empty
// document. scrubDiagnostic deliberately terminates the error chain, so
// that classification has to happen before the scrub; every error read
// through this function is scrubbed by its caller before it can reach a
// terminal.
func memoryCallRaw(cmd *cobra.Command, deps memoryDeps, method string, params, out any) error {
	socketPath, err := memoryResolveSocket(cmd.Context(), deps)
	if err != nil {
		return err
	}
	return deps.Call(cmd.Context(), socketPath, method, params, out)
}

// memoryResolveSocket loads config.toml — the single resolution model every
// CLI command uses (08 §2) — and resolves the daemon's socket path from it,
// falling back to the path provider's derived default.
func memoryResolveSocket(ctx context.Context, deps memoryDeps) (string, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "cascade memory: load config.toml")
	}
	settings, err := daemon.ResolveSettings(cfg, deps.Paths)
	if err != nil {
		return "", err
	}
	return settings.SocketPath, nil
}

// memoryWriter builds this command's output.Writer from the resolved global
// flags, mirroring statusOutputWriter's established local convention.
func memoryWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
