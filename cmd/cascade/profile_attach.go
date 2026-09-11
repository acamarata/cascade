// Purpose: attachServerProfile is root.go's PersistentPreRunE hook for the
//
//	server profile (P1-E17-W4-S38-T4) — resolves the flag/env/file profile
//	cascade (runtime.ResolveProfile) and, only when "server" is selected,
//	constructs the real drivers via assembleServerProfile (profile_server.go
//	when built with -tags=postgres, profile_server_stub.go's typed refusal
//	otherwise) and attaches the result to ctx for subcommands to read via
//	runtime.ServerProfileFrom. The local and worker profiles pass ctx
//	through unchanged — this hook does nothing for them.
//
// Constraints: no build tag — compiled into every binary regardless of
//
//	-tags=postgres, since it only ever calls the tag-split
//	assembleServerProfile, never providers/** directly (Art.10.2).
//
// SPORT: cmd/cascade.profile-server/ADDED (P1-E17-W4-S38-T4).
package main

import (
	"context"
	"os"

	"github.com/acamarata/cascade/internal/runtime"
)

// attachServerProfile resolves the active runtime.Profile (local/server/
// worker) and, for "server", dials the real Postgres + pgvector drivers
// and attaches them to ctx. A construction failure is returned as-is
// (fail-closed: the command fails rather than silently running against
// local storage). local/worker pass ctx through unchanged.
//
// DELIBERATELY does not read globalFlags.Profile: that flag is "a named
// config profile" (root.go's own doc comment, 07-CLI-COMMAND-TREE
// §global-flags) — an arbitrary caller-chosen string with no
// local/server/worker validation, proven by root_test.go's
// TestGlobalFlagsAccessibleFromSubcommand setting it to "work" and
// asserting no error. runtime.Profile (local|server|worker) is a
// DIFFERENT concept that happens to share the word "profile"; feeding
// globalFlags.Profile into runtime.ResolveProfile made that test fail
// ("invalid profile \"work\" from flag") the first time this file was
// wired in — a real contract-vs-tree collision this ticket found live,
// not a hypothetical. Until a later ticket reconciles the two "profile"
// concepts (or gives runtime.Profile its own flag), this resolves ONLY
// via CASCADE_PROFILE env / config.toml [runtime].profile, per
// ResolveProfile's documented cascade with an empty flag value.
func attachServerProfile(ctx context.Context) (context.Context, error) {
	profile, _, err := runtime.ResolveProfile("", osEnvLookup, "")
	if err != nil {
		return ctx, err
	}
	if profile != runtime.ProfileServer {
		return ctx, nil
	}
	sp, err := assembleServerProfile(ctx, osEnvLookup, runtime.NewSystemClock())
	if err != nil {
		return ctx, err
	}
	return runtime.WithServerProfile(ctx, sp), nil
}

// osEnvLookup adapts os.LookupEnv to runtime.EnvLookup's shape.
func osEnvLookup(key string) (string, bool) { return os.LookupEnv(key) }
