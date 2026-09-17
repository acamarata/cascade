package plugins

import (
	"context"
	"fmt"
	"os"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/claude"
)

// Purpose (this file): the cascade-claude composition root. plugins/** may
//   not import internal/** (Art.10.2), so every collaborator the harness
//   plugin needs is injected from here — the one package free to import
//   both sides — exactly as registry.go does for cascade-codex and
//   cascade-opencode, and as cascadepa_wiring.go does for cascade-pa.
// Inputs: internal/context's CC instruction writer; the process-wide
//   hookpacks.DefaultRegistry.
// Outputs: cascade-claude's Generate and RenderHookPack seams, wired to
//   their real implementations; the "cascade-claude" pack registered in the
//   hook registry (R-16.48).
// Constraints: importing this package is what makes the plugin real. The
//   blank-import effect of naming plugins/claude here also runs its init(),
//   so the manifest reaches plugin.Builtins().
// SPORT: internal/plugins cascade-claude wiring (ADD) — P1-E16-W4-S34-T1.

// # EVERY SEAM THIS PLUGIN DECLARES IS WIRED HERE
//
// cascade-claude declares exactly two seams and both are wired below, so
// nothing it ships is unreachable from a running program. That is the point
// of the shape: the contract's other two capabilities (session watch,
// uninstall hook) were left OUT of the plugin entirely rather than shipped
// against seams with no real implementation to inject, because a capability
// wired to nothing is a dead surface — built, tested and unreachable — and
// the tree's own test-only gate correctly refuses to let one land.
//
// Their blockers are real and recorded in the ticket journal: the watch
// needs a client of the fleet sessions SSE stream (internal/fleet/sessions
// ships only the server half), and the uninstall audit needs a kind the
// closed, ratified audit taxonomy (R-21.235) does not contain.

// init wires every cascade-claude seam that has a real counterpart, and
// registers this plugin's hook pack.
//
// It always succeeds: the closures below are never nil, so the setters'
// only failure mode cannot occur. A panic here would mean the adapter's own
// construction is broken, matching registry.go's established
// init-time-registry-failure pattern.
func init() {
	// R-16.48: the pack is registered under this plugin's own name. The
	// descriptors come from hookpacks.SessionsPack(), whose event types
	// are each backed by a captured fixture — this registers that pack
	// rather than authoring a second, unfixtured copy of it.
	hookpacks.DefaultRegistry.RegisterPack(claudePackName, hookpacks.SessionsPack())

	// Four seams, one failure policy, stated once. Written as a table
	// rather than as four if-err-panic blocks because the policy is the
	// same for all of them and repeating it invites the fifth seam to be
	// wired with a slightly different one.
	//
	//   generator          — renders the instruction files
	//   hook-pack renderer — renders the session hook pack
	//   socket resolver    — DERIVES the daemon socket from the cascade
	//                        home, so an unset override is not a refusal
	//   instruction writer — merges the managed block, so an operator's
	//                        edits around it survive a regeneration
	for _, seam := range []struct {
		what string
		err  error
	}{
		{"generator", claude.SetGenerator(harnessGeneratorCC(&casctx.CCInstructionWriter{}))},
		{"hook-pack renderer", claude.SetHookPackRenderer(renderDefaultHookPacks)},
		{"socket resolver", claude.SetSocketResolver(runtimeSocket)},
		{"instruction writer", claude.SetInstructionWriter(managedBlockWriter)},
	} {
		if seam.err != nil {
			panic("internal/plugins: wire cascade-claude " + seam.what + ": " + seam.err.Error())
		}
	}
}

// runtimeSocket resolves the daemon socket the way every other consumer in
// this tree does, through the runtime's path provider.
//
// The seam exists because the socket is DERIVED from the cascade home and
// the environment variable is only an override. plugins/** may not import
// internal/** (Art.10.2), so without this the plugin's own resolver could
// reach nothing but that override -- and an unset override made the
// hook-pack install refuse on every machine whose operator had never
// exported one (R-14.258 Finding 4).
func runtimeSocket() (string, error) {
	paths, err := runtime.NewPathProvider(os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	// No empty-socket guard here on purpose. SocketPath returns the
	// override or a path derived from the cascade home, so it cannot be
	// empty once the provider constructed -- and the hook-pack renderer
	// already refuses an empty socket, where that refusal is reachable and
	// tested. A second check that no test can drive is a branch nobody can
	// prove still works.
	return paths.SocketPath(), nil
}

// managedBlockWriter materializes one instruction file through the context
// engine's managed-block writer, so whatever the operator wrote around
// that block survives a regeneration.
//
// RefuseIfEdited, and the refusal is translated into a PRESERVED result
// rather than an error: 08 §2's rule for a second setup run is "modified
// -> report, no silent overwrite", and failing the whole run would be a
// third behaviour neither the rule nor the operator asked for. Every other
// failure is still an error.
func managedBlockWriter(path string, content []byte) (claude.InstallResult, error) {
	res, err := casctx.WriteHarnessFile(path, casctx.HarnessFile{Content: content}, casctx.RefuseIfEdited)
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindConflict {
			return claude.InstallResult{
				Path: path, Preserved: true,
				Reason: "left alone: the managed block has been edited by hand",
			}, nil
		}
		return claude.InstallResult{Path: path}, err
	}
	return claude.InstallResult{Path: path, Changed: res.Action != casctx.ActionUnchanged}, nil
}

// RegisterHydrationPack registers the P1-E16-W4-S34-T4 prompt-hydration
// hook pack when cfg says hydration is enabled, and returns whether it
// did.
//
// It is NOT called from init, unlike the sessions pack, because the
// decision depends on configuration and init runs before any config is
// loaded. The composition root calls it once the config is in hand.
//
// [context.hydration].enabled gates INSTALLATION, not per-invocation
// behaviour (R-16.6a): with it false the descriptor is never written into
// the harness's hook config, so no hook runs at all. Scope safety is the
// scope resolver's job, never this switch's.
//
// Registering the SAME pack name twice is how a re-registration after a
// config reload replaces the previous descriptor set rather than
// appending to it -- HookRegistry.RegisterPack keys by name.
func RegisterHydrationPack(cfg hydration.Config) bool {
	if !cfg.Enabled {
		return false
	}
	hookpacks.DefaultRegistry.RegisterPack(hookpacks.HydrationPackName, hookpacks.HydrationPack())
	return true
}

// claudePackName is the hook-registry key for cascade-claude's pack. It
// matches the plugin's own manifest id, which is what the contract names.
const claudePackName = "cascade-claude"

// renderDefaultHookPacks adapts hookpacks.DefaultRegistry.Render into the
// plugin's seam.
//
// Render is fail-closed: it returns nil rather than a partially rendered
// configuration when any pack fails to render (today, only an empty socket
// path). The plugin's seam reports failure as an error, so that nil is
// translated here into a real error naming the socket — otherwise a
// fail-closed render would reach the installer as "no configuration",
// which is the silent under-install the fail-closed design exists to
// prevent.
func renderDefaultHookPacks(socket string) ([]byte, error) {
	rendered := hookpacks.DefaultRegistry.Render(socket)
	if rendered == nil {
		return nil, fmt.Errorf("internal/plugins: hook registry declined to render for socket %q", socket)
	}
	return rendered, nil
}

// harnessGeneratorCC adapts w into cascade-claude's GeneratorFunc shape.
// A third function, rather than a generic helper, because each harness
// plugin owns its own internal/-free GeneratedFile type — the same reason
// registry.go carries harnessGenerator and harnessGeneratorOC separately.
func harnessGeneratorCC(w casctx.HarnessGenerator) claude.GeneratorFunc {
	return func(ctx context.Context, cwd string) ([]claude.GeneratedFile, error) {
		files, err := resolveHarnessFiles(ctx, cwd, w)
		if err != nil {
			return nil, err
		}
		out := make([]claude.GeneratedFile, 0, len(files))
		for _, f := range files {
			out = append(out, claude.GeneratedFile{Path: f.path, Content: f.content})
		}
		return out, nil
	}
}
