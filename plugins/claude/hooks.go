package claude

import (
	"fmt"
	"os"
	"path/filepath"
)

// Purpose: install and remove this plugin's hook pack in the harness hook
//   config directory, idempotently and version-stamped.
// Inputs: resolved Paths; a daemon socket path; the injected
//   RenderHookPack seam, which internal/plugins/registry.go sets to a real
//   adapter over internal/fleet/hookpacks.HookRegistry.
// Outputs: the installed hook config file and its companion version file.
// Constraints: plugins/** may import pkg/** only, never internal/**
//   (Art.10.2) — see the HOOK REGISTRY SEAM note below for where R-16.48's
//   RegisterPack call actually happens and why it cannot happen here.
// SPORT: plugins/claude hook-pack install (ADD) — P1-E16-W4-S34-T1.

// HookPackVersion is the installed pack's schema version. It drives the
// re-install-on-bump rule below and is written to a companion file so it
// stays readable after install — the forward requirement R-21.77 places on
// this ticket, so AP/S-81.T4's later startup handshake can carry a real
// pack_version. This ticket adds no handshake and no child accounting.
const HookPackVersion = "1"

// hookPackFile is the installed union config's basename under HookConfig.
const hookPackFile = packName + ".json"

// hookPackVersionFile is the companion holding HookPackVersion.
//
// The version lives beside the config rather than inside it because the
// config's bytes are produced whole by the host's hook registry: editing
// that document to insert a version would mean re-marshaling another
// component's output, and a reformat there would look like a content
// change on every run. A companion file keeps the rendered union
// byte-exact and the version trivially readable.
const hookPackVersionFile = packName + ".version"

// # HOOK REGISTRY SEAM
//
// R-16.48 says this ticket calls HookRegistry.RegisterPack("cascade-claude",
// HookPack{...}) at plugin init. HookRegistry and HookPack live in
// internal/fleet/hookpacks, which this package is denied from importing
// (Art.10.2), so the RegisterPack call itself is made by
// internal/plugins/registry.go — the one place free to import both sides,
// and the same bridge the generator seam uses. What this package owns is
// the installation half: RenderHookPack returns the registry's rendered
// union for a socket, and InstallHookPack materializes it. The registry is
// authoritative for pack CONTENT; this file is authoritative for where that
// content lands and when it is rewritten.

// HookPackRendererFunc renders the registered hook packs' union config for
// socket. It mirrors HookRegistry.Render, except that it reports failure as
// an error instead of a nil slice: Render is fail-closed and returns nil
// rather than a partial config, and silently installing nothing is exactly
// the outcome this seam must not allow.
type HookPackRendererFunc func(socket string) ([]byte, error)

// RenderHookPack is the active renderer. internal/plugins/registry.go
// overwrites it via SetHookPackRenderer at process boot; tests overwrite it
// directly.
var RenderHookPack HookPackRendererFunc = unwiredRenderer

// unwiredRenderer is RenderHookPack's default: a real, typed error
// reporting that no host has wired the hook registry yet.
func unwiredRenderer(string) ([]byte, error) {
	return nil, fmt.Errorf("cascade-claude: hook-pack renderer not wired (internal/plugins/registry.go must call claude.SetHookPackRenderer)")
}

// SetHookPackRenderer installs r as the active renderer. A nil r is refused
// rather than silently disabling hook-pack install.
func SetHookPackRenderer(r HookPackRendererFunc) error {
	if r == nil {
		return fmt.Errorf("cascade-claude: SetHookPackRenderer: renderer must not be nil")
	}
	RenderHookPack = r
	return nil
}

// SocketResolverFunc reports the daemon socket the hook pack's commands
// will post to.
type SocketResolverFunc func() (string, error)

// ResolveSocket is the active resolver. internal/plugins wires it to the
// runtime's own path provider at process boot, the same way Generate and
// RenderHookPack are wired.
//
// The seam exists because the socket is DERIVED, not supplied: the
// runtime computes it from the cascade home and treats the environment
// variable as an override. This package cannot import internal/**
// (Art.10.2), so without the seam its only reachable answer was the
// override -- and an unset override made the hook-pack install refuse on
// every machine whose operator had never exported one, which is nearly
// all of them. `cascade init` failed outright at step 6 for exactly that
// reason.
//
// The refusal on an empty result is kept: the renderer refuses an empty
// socket, and a hook pack installed against a guessed path fails silently
// at session time. What changed is that "derive it" is now an answer, and
// only a genuinely unresolvable socket refuses.
var ResolveSocket SocketResolverFunc = envSocketResolver

// SetSocketResolver installs r as the active resolver. A nil r is refused
// rather than silently reverting to the environment-only resolver.
func SetSocketResolver(r SocketResolverFunc) error {
	if r == nil {
		return fmt.Errorf("cascade-claude: SetSocketResolver: resolver must not be nil")
	}
	ResolveSocket = r
	return nil
}

// envSocketResolver is ResolveSocket's default, for a process that never
// wired the runtime: the override, or a refusal naming it.
func envSocketResolver() (string, error) { return SocketPath(os.Getenv) }

// SocketPath returns the daemon socket read from the override variable via
// env. An unset variable is an error rather than a guess -- callers that
// can DERIVE the socket inject a resolver instead of calling this.
func SocketPath(env Env) (string, error) {
	if env == nil {
		return "", fmt.Errorf("cascade-claude: SocketPath: env must not be nil")
	}
	socket := env(socketOverrideVar)
	if socket == "" {
		return "", fmt.Errorf("cascade-claude: resolve daemon socket: %s is unset", socketOverrideVar)
	}
	return socket, nil
}

// socketOverrideVar names the environment override for the daemon socket.
const socketOverrideVar = "CASCADE_SOCKET"

// InstallHookPack renders the hook-pack union for the host's daemon socket
// and installs it under paths.HookConfig, together with its version
// companion.
//
// Idempotency: the install is skipped when the version companion matches
// HookPackVersion AND the rendered bytes match what is already on disk. The
// contract's rule is "re-install only on schema version bump or missing
// file"; the content comparison is an addition, not a relaxation — without
// it a changed socket path would render different commands that the version
// check alone would silently decline to install.
func InstallHookPack(paths Paths) ([]InstallResult, error) {
	socket, err := ResolveSocket()
	if err != nil {
		return nil, err
	}
	return installHookPackAt(paths, socket)
}

// installHookPackAt is InstallHookPack's testable core: it takes the socket
// explicitly instead of reading the process environment.
func installHookPackAt(paths Paths, socket string) ([]InstallResult, error) {
	rendered, err := RenderHookPack(socket)
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: render hook pack: %w", err)
	}
	if len(rendered) == 0 {
		return nil, fmt.Errorf("cascade-claude: render hook pack: renderer produced no configuration for socket %q", socket)
	}

	configPath := filepath.Join(paths.HookConfig, hookPackFile)
	versionPath := filepath.Join(paths.HookConfig, hookPackVersionFile)

	configChanged, err := writeIfChanged(configPath, rendered)
	if err != nil {
		return nil, err
	}
	versionChanged, err := writeIfChanged(versionPath, []byte(HookPackVersion))
	if err != nil {
		return nil, err
	}
	return []InstallResult{
		{Path: configPath, Changed: configChanged},
		{Path: versionPath, Changed: versionChanged},
	}, nil
}
