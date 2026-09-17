package initconfig

// Purpose: the CASCADE_INIT_* environment map and the precedence that
//   merges flags, environment, file and defaults into one spec
//   (P1-E16-W4-S35-T7, 08-INIT-CONFIG-SPEC §2).
// Inputs: the parsed flags, an environment, and an optional *InitConfig.
// Outputs: a *Spec the wizard substitutes at each step site, or a
//   refusal.
// Constraints: the order is flags > env > file > defaults, applied by ONE
//   function. Four sources merged at four call sites is four chances to
//   apply them in a different order, and the symptom is a machine
//   configured from the wrong one.
// SPORT: internal/runtime/initconfig env map (ADD) — P1-E16-W4-S35-T7.

import (
	"os"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The environment variables this package reads.
const (
	// EnvConfig names a setup file, and its presence implies a
	// non-interactive run.
	EnvConfig = "CASCADE_INIT_CONFIG"
	// EnvYes accepts every default, overriding both file and env.
	EnvYes = "CASCADE_YES"
	// EnvNoInput makes any would-be prompt a hard error.
	EnvNoInput = "CASCADE_NO_INPUT"
	// EnvHome overrides the cascade home.
	EnvHome = "CASCADE_HOME"
	// EnvProfile overrides the profile.
	EnvProfile = "CASCADE_PROFILE"
	// EnvTelemetry, set to "0", always wins over an enabling setup file.
	// Telemetry is opt-in, and a file somebody else wrote must not be
	// able to opt this machine in over its own environment.
	EnvTelemetry = "CASCADE_TELEMETRY"
)

// Flags are the command-line values that outrank everything else.
type Flags struct {
	// ConfigPath is --config.
	ConfigPath string
	// Yes is --yes.
	Yes bool
	// Profile is --profile.
	Profile string
	// Harnesses is --harness.
	Harnesses []string
	// NoDaemon is --no-daemon.
	NoDaemon bool
}

// Spec is the merged, non-interactive answer set.
//
// Every field a step might otherwise prompt for is present-or-absent
// rather than zero-or-set: "the file said false" and "nothing said
// anything" are different, and only the second may fall back to a default
// or to a prompt. That distinction is what the prompt guard reads.
type Spec struct {
	// Profile, empty when nothing supplied one.
	Profile string
	// Plugins names the catalog entries to enable, nil when unset.
	Plugins []string
	// PluginsSet distinguishes "enable nothing" from "nothing said".
	PluginsSet bool
	// Providers are the directives to run, in order.
	Providers []ProviderDirective
	// ProvidersSet distinguishes "add no providers" from "nothing said".
	ProvidersSet bool
	// Harnesses names the harnesses to wire, nil when unset.
	Harnesses []string
	// HarnessesSet distinguishes "wire none" from "nothing said".
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
	// Yes reports that every unset field may take its default without a
	// prompt.
	Yes bool
	// NoInput makes an unanswerable site a hard error rather than a
	// prompt.
	NoInput bool
	// Home overrides the cascade home, empty when unset.
	Home string
}

// EnvFunc reads one environment variable.
type EnvFunc func(key string) string

// OSEnv is the production EnvFunc.
func OSEnv(key string) string { return os.Getenv(key) }

// Resolve merges the three sources under the ratified precedence.
//
// flags > env > file > defaults, applied here and nowhere else. A caller
// that merged two of them itself before calling would silently change
// that order for the fields it touched.
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
	if err := spec.validate(); err != nil {
		return nil, err
	}
	return spec, nil
}

// applyFile lays the file's values down first, so env and then flags can
// overwrite them.
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

// validProfileName reports whether name is one of the three.
//
// Duplicated from internal/runtime/init on purpose: the dependency runs
// one way — the wizard imports this package and this package imports
// nothing of the wizard's — and three string constants are a smaller cost
// than inverting that. A test in the wizard's package asserts the two
// agree, so the duplication cannot drift silently.
func validProfileName(name string) bool {
	switch name {
	case "local", "server", "worker":
		return true
	default:
		return false
	}
}

// truthy reads an environment flag. Anything but the empty string, "0",
// "false", "no" and "off" is true, which is the convention every other
// CASCADE_* variable in this codebase already follows.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// KeyFor reads the key a directive points at, once.
//
// The VALUE never enters the Spec, the journal or any log line: this is
// called at the moment the provider is added and the result goes straight
// to intake. An empty variable is a refusal NAMING the variable, because
// a provider added with an empty key fails later, somewhere that cannot
// say which setup file sent it.
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
