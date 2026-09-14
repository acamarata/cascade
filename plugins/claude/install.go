package claude

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Purpose: resolve the three normative harness config paths, and install
//   the harness instruction golden atomically and idempotently.
// Inputs: a working directory; the process environment (through an
//   injectable Env, never read by probing the filesystem); the
//   package-level Generate variable, which internal/plugins/registry.go
//   sets to a real adapter over internal/context's CC instruction writer
//   (the E/S-08.T3 generator).
// Outputs: one InstallResult per generated file; a Paths value.
// Constraints: plugins/** may import pkg/** only, never internal/**
//   (Art.10.2, plugins-providers-boundary depguard rule) — see the
//   GENERATOR SEAM note in plugins/opencode/install.go, which this package
//   follows exactly. Windows is tier-2: its paths resolve (so the branch is
//   real and testable) but install refuses.
// SPORT: plugins/claude install (ADD) — P1-E16-W4-S34-T1.

// # HARNESS PATHS (NORMATIVE)
//
// 08-INIT-CONFIG-SPEC carries no §paths section, so P1-E16-W4-S34-T1's own
// contract is the source of truth for these three paths and they are read
// from nowhere else in the tree. Every branch is selected by an explicit
// goos argument rather than discovered by probing, so all three run on any
// host and CI's GOOS matrix confirms rather than provides the coverage.
const (
	// configDirDarwin is ConfigRoot's tail under $HOME on darwin.
	configDirDarwin = "Library/Application Support/Claude"
	// configDirLinux is ConfigRoot's tail under the XDG config home.
	configDirLinux = "claude"
	// configDirWindows is ConfigRoot's tail under %APPDATA%.
	configDirWindows = "Claude"
	// mcpConfigName is MCPConfig's basename under ConfigRoot.
	mcpConfigName = "mcp.json"
	// hookConfigName is HookConfig's basename under ConfigRoot.
	hookConfigName = "hooks"
)

// Env reads one environment variable, returning "" when it is unset. It is
// injected so a test can drive any platform's branch on any host without
// mutating the real process environment.
type Env func(string) string

// Paths is the resolved set of harness config locations.
type Paths struct {
	// ConfigRoot is the harness's per-user configuration directory.
	ConfigRoot string
	// MCPConfig is the MCP server configuration file.
	MCPConfig string
	// HookConfig is the directory holding installed hook-pack configs.
	HookConfig string
}

// ResolvePaths resolves the three harness paths for goos using env. It
// never touches the filesystem: an unset variable is an error naming the
// variable, never a silent fallback to a path that happens to exist.
func ResolvePaths(goos string, env Env) (Paths, error) {
	if env == nil {
		return Paths{}, fmt.Errorf("cascade-claude: ResolvePaths: env must not be nil")
	}
	var root string
	switch goos {
	case "darwin":
		home := env("HOME")
		if home == "" {
			return Paths{}, fmt.Errorf("cascade-claude: resolve config root on darwin: HOME is unset")
		}
		root = filepath.Join(home, filepath.FromSlash(configDirDarwin))
	case "windows":
		appData := env("APPDATA")
		if appData == "" {
			return Paths{}, fmt.Errorf("cascade-claude: resolve config root on windows: APPDATA is unset")
		}
		root = filepath.Join(appData, configDirWindows)
	default:
		// Every non-darwin, non-windows GOOS follows the XDG base-directory
		// spec, which is what the contract's "linux" row describes; naming
		// the rule rather than the one GOOS keeps freebsd and friends from
		// falling into a branch that was never written for them.
		if xdg := env("XDG_CONFIG_HOME"); xdg != "" {
			root = filepath.Join(xdg, configDirLinux)
			break
		}
		home := env("HOME")
		if home == "" {
			return Paths{}, fmt.Errorf("cascade-claude: resolve config root on %s: neither XDG_CONFIG_HOME nor HOME is set", goos)
		}
		root = filepath.Join(home, ".config", configDirLinux)
	}
	return Paths{
		ConfigRoot: root,
		MCPConfig:  filepath.Join(root, mcpConfigName),
		HookConfig: filepath.Join(root, hookConfigName),
	}, nil
}

// HostPaths resolves the paths for the running host from its real
// environment. It is the production entry point; hostPathsFor is the
// testable core.
func HostPaths() (Paths, error) { return hostPathsFor(runtime.GOOS, os.Getenv) }

// hostPathsFor resolves goos's paths and applies the tier-2 refusal.
//
// The refusal lives HERE, at the one gate every host-facing entry point
// passes through, rather than inside Install/RegisterMCP. Art.5 makes
// Windows tier-2 for this integration, so nothing may install there; but
// putting the GOOS check inside each capability would make those pure
// functions behave differently per platform and force every one of their
// tests to skip on the Windows lane — and a skip is not a pass. With the
// gate here, the capabilities stay platform-independent and directly
// testable everywhere, while no host path can reach them on Windows.
// ResolvePaths still resolves the Windows root, so that branch remains
// real and asserted rather than unreachable.
func hostPathsFor(goos string, env Env) (Paths, error) {
	if goos == "windows" {
		return Paths{}, errWindowsTier2("resolve the harness config root")
	}
	return ResolvePaths(goos, env)
}

// errWindowsTier2 reports the tier-2 refusal, naming the tier rather than
// failing obscurely or half-installing.
func errWindowsTier2(step string) error {
	return fmt.Errorf("cascade-claude: %s: harness integration not available on Windows tier-2", step)
}

// # GENERATOR SEAM
//
// The contract says install.go "retrieves the byte-stable CC instruction
// golden produced by internal/context". That generator's types live in
// internal/context, which this package is denied from importing.
// GeneratedFile and GeneratorFunc are this package's own internal/-free
// vocabulary for the same information; internal/plugins/registry.go — free
// to import both sides — adapts the real writer into this shape at
// registration time. Generate defaults to unwiredGenerator so a build that
// omits that wiring fails loudly rather than installing nothing.

// GeneratedFile is one instruction file the injected generator produced.
type GeneratedFile struct {
	// Path is the absolute filesystem path to materialize Content at.
	Path string
	// Content is the exact bytes to write.
	Content []byte
}

// GeneratorFunc produces the harness instruction golden for cwd. A real
// implementation is deterministic: the same cwd yields byte-identical
// output on every call.
type GeneratorFunc func(ctx context.Context, cwd string) ([]GeneratedFile, error)

// Generate is the active generator. internal/plugins/registry.go overwrites
// it via SetGenerator with a real adapter over internal/context at process
// boot; tests overwrite it directly.
var Generate GeneratorFunc = unwiredGenerator

// unwiredGenerator is Generate's default: a real, typed error reporting
// that no host has wired a generator yet.
func unwiredGenerator(context.Context, string) ([]GeneratedFile, error) {
	return nil, fmt.Errorf("cascade-claude: instruction generator not wired (internal/plugins/registry.go must call claude.SetGenerator)")
}

// SetGenerator installs g as the active generator. A nil g is refused
// rather than silently disabling install.
func SetGenerator(g GeneratorFunc) error {
	if g == nil {
		return fmt.Errorf("cascade-claude: SetGenerator: generator must not be nil")
	}
	Generate = g
	return nil
}

// InstallResult reports what happened to one file this plugin manages.
type InstallResult struct {
	// Path is the file considered.
	Path string
	// Changed is true when the file was created or its content differed
	// from what was already on disk; false when the write was skipped
	// because the content already matched (idempotent no-op).
	Changed bool
}

// Install renders the harness instruction golden for cwd via Generate and
// materializes every file atomically, skipping any file whose on-disk
// content already matches. A second run reports Changed=false for
// everything and touches no file's mtime.
func Install(ctx context.Context, cwd string) ([]InstallResult, error) {
	files, err := Generate(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: generate instructions: %w", err)
	}
	results := make([]InstallResult, 0, len(files))
	for _, f := range files {
		changed, err := writeIfChanged(f.Path, f.Content)
		if err != nil {
			return results, err
		}
		results = append(results, InstallResult{Path: f.Path, Changed: changed})
	}
	return results, nil
}

// writeIfChanged writes content to path only when it differs from what is
// already there, through a temp file in the same directory so a crash
// mid-write leaves either the old file intact or the new one, never a
// truncated hybrid.
func writeIfChanged(path string, content []byte) (bool, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // path is composed by the generator or the resolved config root, not user input.
	switch {
	case err == nil:
		if bytes.Equal(existing, content) {
			return false, nil
		}
	case !os.IsNotExist(err):
		return false, fmt.Errorf("cascade-claude: read %s: %w", path, err)
	}
	if err := writeAtomic(path, content); err != nil {
		return false, err
	}
	return true, nil
}

// writeAtomic writes data to path via create-temp-then-rename in path's own
// directory.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cascade-claude: create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".cascade-claude-*")
	if err != nil {
		return fmt.Errorf("cascade-claude: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: close %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: set mode on %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: replace %s: %w", path, err)
	}
	return nil
}

// runInstall is RunCommand's "install" handler: it installs into the
// process's real working directory and config root. Success is silent,
// matching the cascade-opencode idiom — this package may not write to
// stdout directly (internal/build/outputgate.go exempts only
// plugins/examples). A caller needing per-file detail calls the capability
// functions directly.
func runInstall(ctx context.Context, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cascade-claude: getwd: %w", err)
	}
	if _, err := Install(ctx, cwd); err != nil {
		return err
	}
	paths, err := HostPaths()
	if err != nil {
		return err
	}
	if _, err := InstallHookPack(paths); err != nil {
		return err
	}
	_, err = RegisterMCP(paths)
	return err
}
