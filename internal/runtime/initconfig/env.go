package initconfig

// Purpose: the CASCADE_INIT_* environment map and the precedence that
//   merges flags, environment, file and defaults into one spec
//   (P1-E16-W4-S35-T7, 08-INIT-CONFIG-SPEC §2).
// Inputs: the parsed flags, an environment, and an optional *InitConfig.
// Outputs: a *Spec the wizard substitutes at each step site, or a
//   refusal.
// Constraints: the order is flags > env > file > defaults, applied by ONE
//   function; four call sites would be four chances to get it wrong.
// SPORT: internal/runtime/initconfig env map (ADD) — P1-E16-W4-S35-T7.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The environment variables this package reads.
const (
	// EnvConfig names a setup file; its presence implies no prompts.
	EnvConfig = "CASCADE_INIT_CONFIG"
	// EnvYes accepts every default, overriding both file and env.
	EnvYes = "CASCADE_YES"
	// EnvNoInput makes any would-be prompt a hard error.
	EnvNoInput = "CASCADE_NO_INPUT"
	// EnvHome overrides the cascade home.
	EnvHome = "CASCADE_HOME"
	// EnvProfile overrides the profile.
	EnvProfile = "CASCADE_PROFILE"
	// EnvTelemetry, set to "0", always wins over an enabling setup file:
	// telemetry is opt-in, and the environment outranks someone's file.
	EnvTelemetry = "CASCADE_TELEMETRY"
	// The [roots], [nodes] and [policy] map; roots split like PATH.
	EnvRootsPaths            = "CASCADE_INIT_ROOTS_PATHS"
	EnvRootsMaintain         = "CASCADE_INIT_ROOTS_MAINTAIN"
	EnvRootsMaxFiles         = "CASCADE_INIT_ROOTS_MAX_FILES"
	EnvRootsAcceptDiscovered = "CASCADE_INIT_ROOTS_ACCEPT_DISCOVERED"
	EnvNodesScanLAN          = "CASCADE_INIT_NODES_SCAN_LAN"
	EnvNodesPair             = "CASCADE_INIT_NODES_PAIR"
	EnvNodesTravel           = "CASCADE_INIT_NODES_TRAVEL"
	EnvPolicyPreset          = "CASCADE_INIT_POLICY_PRESET"
	// EnvNodeCodePrefix starts every pairing-code name; see NodeCodeEnv.
	EnvNodeCodePrefix = "CASCADE_INIT_NODE_CODE_"
)

// Flags are the command-line values that outrank everything else.
type Flags struct {
	// ConfigPath is --config, Yes is --yes, Profile is --profile.
	ConfigPath string
	Yes        bool
	Profile    string
	// Harnesses is --harness, NoDaemon is --no-daemon.
	Harnesses []string
	NoDaemon  bool
	// PolicyPreset is --policy.
	PolicyPreset string
}

// Spec is the merged, non-interactive answer set. A prompted field is
// present-or-absent, not zero-or-set: "the file said false" differs from
// "nothing said anything", and only the second may default or prompt.
type Spec struct {
	// Profile, empty when nothing supplied one.
	Profile string
	// Plugins names the catalog entries to enable; PluginsSet tells
	// "enable nothing" from "nothing said".
	Plugins    []string
	PluginsSet bool
	// Providers are the directives to run, in order; ProvidersSet tells
	// "add none" from "nothing said".
	Providers    []ProviderDirective
	ProvidersSet bool
	// Harnesses names the harnesses to wire; HarnessesSet tells "wire
	// none" from "nothing said".
	Harnesses    []string
	HarnessesSet bool
	// DetectHarnesses runs step 6 at all.
	DetectHarnesses bool
	// Server carries the server profile's storage references.
	Server ServerConfig
	// Telemetry and TelemetrySet: unset means the default, which is off.
	Telemetry    bool
	TelemetrySet bool
	// Daemon and DaemonSet: unset means the profile's default.
	Daemon    bool
	DaemonSet bool
	// Yes takes every unset default unprompted; NoInput makes an
	// unanswerable site a hard error, not a prompt.
	Yes     bool
	NoInput bool
	// Home overrides the cascade home, empty when unset.
	Home string
	// Roots, Nodes and Policy are the merged discovery sections. They
	// never carry a pairing code; NodeCodeFor reads one when needed.
	Roots  RootsConfig
	Nodes  NodesConfig
	Policy PolicyConfig
}

// EnvFunc reads one environment variable.
type EnvFunc func(key string) string

// OSEnv is the production EnvFunc.
func OSEnv(key string) string { return os.Getenv(key) }

// Resolve merges flags > env > file > defaults, here and nowhere else.
func Resolve(flags Flags, env EnvFunc, file *InitConfig) (*Spec, error) {
	if env == nil {
		env = func(string) string { return "" }
	}
	spec := &Spec{
		Yes:     flags.Yes || truthy(env(EnvYes)),
		NoInput: truthy(env(EnvNoInput)),
		Home:    env(EnvHome),
	}
	applyFile(spec, file)
	applyEnv(spec, env)
	applyFlags(spec, flags)
	envO, err := discoveryOverlay(env)
	if err != nil {
		return nil, err
	}
	var flagsO DiscoveryOverlay
	if flags.PolicyPreset != "" {
		flagsO.PolicyPreset = &flags.PolicyPreset
	}
	disc, err := ResolveDiscovery(file, envO, flagsO)
	if err != nil {
		return nil, err
	}
	spec.Roots, spec.Nodes, spec.Policy = disc.Roots, disc.Nodes, disc.Policy
	if err := spec.validate(); err != nil {
		return nil, err
	}
	// Refused before the wizard runs a step: no half-set-up, unpaired host.
	for _, host := range spec.Nodes.Pair {
		if _, err := NodeCodeFor(host, env); err != nil {
			return nil, err
		}
	}
	return spec, nil
}

// applyFile lays the file down first, for env and flags to overwrite.
func applyFile(spec *Spec, file *InitConfig) {
	if file == nil {
		return
	}
	spec.Profile = file.Profile
	spec.Server = file.Server
	spec.DetectHarnesses = file.Harnesses.Detect
	if len(file.Plugins.Enable) > 0 || len(file.Plugins.Disable) > 0 {
		spec.Plugins, spec.PluginsSet = file.Plugins.Enable, true
	}
	if len(file.Providers) > 0 {
		spec.Providers, spec.ProvidersSet = file.Providers, true
	}
	if len(file.Harnesses.Install) > 0 {
		spec.Harnesses, spec.HarnessesSet = file.Harnesses.Install, true
	}
	spec.Telemetry, spec.TelemetrySet = file.Telemetry.Enabled, true
	spec.Daemon, spec.DaemonSet = file.Daemon.Install, true
}

// discoveryOverlay maps the CASCADE_INIT_* discovery keys onto an overlay;
// an unset key leaves its field nil. Precedence is ResolveDiscovery's.
func discoveryOverlay(env EnvFunc) (DiscoveryOverlay, error) {
	var o DiscoveryOverlay
	if v := env(EnvRootsPaths); v != "" {
		p := filepath.SplitList(v)
		o.RootsPaths = &p
	}
	if v := env(EnvRootsMaxFiles); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return DiscoveryOverlay{}, cascade.Newf(cascade.KindInvalidInput,
				"cascade init: %s=%q is not a whole number", EnvRootsMaxFiles, v)
		}
		o.RootsMaxFiles = &n
	}
	for key, field := range map[string]**bool{
		EnvRootsMaintain: &o.RootsMaintain, EnvRootsAcceptDiscovered: &o.RootsAcceptDiscovered,
		EnvNodesScanLAN: &o.NodesScanLAN, EnvNodesTravel: &o.NodesTravel,
	} {
		if v := env(key); v != "" {
			b := truthy(v)
			*field = &b
		}
	}
	if v := env(EnvNodesPair); v != "" {
		p := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		o.NodesPair = &p
	}
	if v := env(EnvPolicyPreset); v != "" {
		o.PolicyPreset = &v
	}
	return o, nil
}

// applyEnv overwrites the file's values where the environment speaks.
func applyEnv(spec *Spec, env EnvFunc) {
	if p := env(EnvProfile); p != "" {
		spec.Profile = p
	}
	// CASCADE_TELEMETRY=0 always wins, in ONE direction only. Telemetry
	// is opt-in: an environment may turn it OFF over a file that turned
	// it on, and nothing may turn it on over an environment that said 0.
	if v := env(EnvTelemetry); v != "" && !truthy(v) {
		spec.Telemetry, spec.TelemetrySet = false, true
	}
}

// applyFlags overwrites everything below them.
func applyFlags(spec *Spec, flags Flags) {
	if flags.Profile != "" {
		spec.Profile = flags.Profile
	}
	if len(flags.Harnesses) > 0 {
		spec.Harnesses, spec.HarnessesSet = flags.Harnesses, true
	}
	if flags.NoDaemon {
		spec.Daemon, spec.DaemonSet = false, true
	}
}

// validate refuses a merged spec that cannot produce a run.
func (s *Spec) validate() error {
	if s.Profile != "" && !validProfileName(s.Profile) {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: %q is not a profile; choose local, server or worker", s.Profile)
	}
	for i := range s.Providers {
		if err := s.Providers[i].validate(i); err != nil {
			return err
		}
	}
	return nil
}

// validProfileName reports whether name is one of the three. Duplicated
// from internal/runtime/init on purpose (the import runs one way); a test
// in the wizard's package asserts the two agree.
func validProfileName(name string) bool {
	return name == "local" || name == "server" || name == "worker"
}

// truthy reads an environment flag: anything but "", "0", "false", "no"
// and "off" is true, as for every other CASCADE_* variable.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// KeyFor reads the key a directive points at, once, when the provider is
// added; the value goes straight to intake, never to the Spec, journal or
// a log. An empty variable is a refusal NAMING the variable.
func (p ProviderDirective) KeyFor(env EnvFunc) (string, error) {
	if p.KeyEnv == "" {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"cascade init: provider %q names no key_env", p.Name)
	}
	value := env(p.KeyEnv)
	if value == "" {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"cascade init: provider %q reads its key from %s, which is unset or empty",
			p.Name, p.KeyEnv)
	}
	return value, nil
}

// NodeCodeEnv names the variable host's pairing code is read from: the
// host upper-cased, every byte outside [A-Z0-9] turned into '_'.
func NodeCodeEnv(host string) string {
	return EnvNodeCodePrefix + strings.Map(func(r rune) rune {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return '_'
		}
		return r
	}, strings.ToUpper(host))
}

// NodeCodeFor reads host's pairing code from NodeCodeEnv(host) only; the
// value never enters the Spec, and a refusal names the variable alone.
func NodeCodeFor(host string, env EnvFunc) (string, error) {
	key := NodeCodeEnv(host)
	if env == nil || env(key) == "" {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"cascade init: [nodes] pairs %q, but %s is unset or empty", host, key)
	}
	return env(key), nil
}
