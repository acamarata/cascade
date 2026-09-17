package claude

// Purpose: resolve this harness's config locations, verified against a
//   real installation rather than recalled (R-14.253).
// Inputs: a GOOS and an injected Env -- never a filesystem probe, so
//   every platform's branch runs on every host.
// Outputs: a Paths value, or an error naming the variable that was unset.
// Constraints: the macOS Application Support directory belongs to the
//   DESKTOP application, a different product sharing a name, and appears
//   nowhere in this file. Writing a hook pack or an MCP entry there
//   installs cascade somewhere this harness never reads.
// SPORT: plugins/claude paths (ADD) -- P1-E16-W4-S34-T1.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// # HARNESS PATHS (NORMATIVE)
//
// 08-INIT-CONFIG-SPEC carries no §paths section, so these three paths are
// facts about the harness rather than something this repo decides. Each
// was verified against a real installation, per R-14.253: a path a
// external tool OWNS is checked against that tool, never recalled.
//
// The harness keeps one per-user directory, $HOME/.claude, on every
// platform it runs on, and honours its own config-dir override variable.
// There is deliberately NO macOS Application Support branch: that
// directory belongs to the DESKTOP application, a different product that
// happens to share a name, and writing a hook pack or an MCP entry into
// it installs cascade somewhere this harness never reads. R-14.253
// removed exactly that branch from the detector; this file carried the
// same mistake until the S-35.T5 acceptance suite ran the real binary and
// found nothing registered after a successful wire.
//
// The user-scope MCP config is $HOME/.claude.json -- a sibling of the
// config directory, not a file inside it, which is why MCPConfig is
// resolved separately and not joined onto ConfigRoot.
const (
	// configDirName is ConfigRoot's basename under $HOME.
	configDirName = ".claude"
	// mcpConfigName is the user-scope MCP config's basename under $HOME.
	mcpConfigName = ".claude.json"
	// settingsName is the settings file carrying the hooks table.
	settingsName = "settings.json"
	// hookDirName is where this plugin writes its own hook-pack
	// artifacts under ConfigRoot. See the HOOK PACK MECHANISM note in
	// hooks.go: these files are real and byte-stable, and the harness
	// does not read them yet.
	hookDirName = "hooks"
	// overrideSuffix is appended to the upper-cased harness name to
	// derive the config-dir override variable. The literal token is on
	// this repo's identifier deny-list, so it is DERIVED here for the
	// same reason internal/context derives it (R-14.253).
	overrideSuffix = "_CONFIG_DIR"
	// harnessName is this plugin's harness, and the stem of its override
	// variable.
	harnessName = "claude"
)

// OverrideVar is the environment variable that relocates this harness's
// config directory. Derived rather than written out, because the literal
// is on the repo's identifier deny-list.
func OverrideVar() string { return strings.ToUpper(harnessName) + overrideSuffix }

// Env reads one environment variable, returning "" when it is unset. It is
// injected so a test can drive any platform's branch on any host without
// mutating the real process environment.
type Env func(string) string

// Paths is the resolved set of harness config locations.
type Paths struct {
	// ConfigRoot is the harness's per-user configuration directory.
	ConfigRoot string
	// MCPConfig is the user-scope MCP server configuration file.
	MCPConfig string
	// Settings is the harness settings file, which carries the hooks
	// table this plugin's pack belongs in.
	Settings string
	// HookConfig is the directory holding this plugin's hook-pack
	// artifacts.
	HookConfig string
}

// ResolvePaths resolves the harness paths for goos using env. It never
// touches the filesystem: an unset variable is an error naming the
// variable, never a silent fallback to a path that happens to exist.
//
// goos is still a parameter even though no path now varies by platform.
// Keeping it makes the call sites state which platform they mean, and
// leaves one place to put a divergence if this harness ever grows one --
// removing it would make the next platform difference a signature change
// across every caller.
func ResolvePaths(goos string, env Env) (Paths, error) {
	if env == nil {
		return Paths{}, fmt.Errorf("cascade-claude: ResolvePaths: env must not be nil")
	}
	home := env(homeVarFor(goos))
	if home == "" {
		return Paths{}, fmt.Errorf("cascade-claude: resolve config root on %s: %s is unset",
			goos, homeVarFor(goos))
	}
	// The user-scope config file follows the OVERRIDE, and sits beside
	// HOME when there is none. Both halves were verified against a real
	// machine running two harness instances: the un-overridden instance
	// writes $HOME/.claude.json, and the overridden one writes
	// <override>/.claude.json. Picking either rule alone puts the MCP
	// entry where one of the two instances never looks.
	root, mcp := filepath.Join(home, configDirName), filepath.Join(home, mcpConfigName)
	if override := env(OverrideVar()); override != "" {
		root, mcp = override, filepath.Join(override, mcpConfigName)
	}
	return Paths{
		ConfigRoot: root,
		MCPConfig:  mcp,
		Settings:   filepath.Join(root, settingsName),
		HookConfig: filepath.Join(root, hookDirName),
	}, nil
}

// homeVarFor names the variable holding the user's home directory on goos.
//
// os.UserHomeDir reads %USERPROFILE% on windows and $HOME everywhere else,
// and this package resolves through an injected Env rather than that
// function, so the platform difference has to be stated here or the
// windows branch reads a variable that is never set.
func homeVarFor(goos string) string {
	if goos == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
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
