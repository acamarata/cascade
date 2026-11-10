package initconfig

// Purpose: the precedence table (P1-E16-W4-S35-T7) — flags > env > file >
//   defaults, and the two rules that are not simply "the higher one wins".
// Constraints: each case sets a conflicting value at EVERY level and
//   asserts which survives. A test that set one level at a time would
//   pass against any ordering at all.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// envFor returns an EnvFunc over a fixed map.
func envFor(pairs map[string]string) EnvFunc {
	return func(key string) string { return pairs[key] }
}

// fileWith returns a minimal valid config carrying profile.
func fileWith(profile string) *InitConfig {
	return &InitConfig{Schema: SchemaVersion, Profile: profile}
}

// TestPrecedenceIsFlagsThenEnvThenFile sets a DIFFERENT profile at every
// level at once and asserts which wins as levels are removed.
func TestPrecedenceIsFlagsThenEnvThenFile(t *testing.T) {
	env := envFor(map[string]string{EnvProfile: "server"})
	file := fileWith("worker")

	for name, tc := range map[string]struct {
		flags Flags
		env   EnvFunc
		file  *InitConfig
		want  string
	}{
		"flags win over env and file": {Flags{Profile: "local"}, env, file, "local"},
		"env wins over file":          {Flags{}, env, file, "server"},
		"file wins over the default":  {Flags{}, envFor(nil), file, "worker"},
		"nothing set leaves it empty": {Flags{}, envFor(nil), nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			spec, err := Resolve(tc.flags, tc.env, tc.file)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if spec.Profile != tc.want {
				t.Errorf("profile = %q, want %q", spec.Profile, tc.want)
			}
		})
	}
}

// TestTelemetryCanOnlyBeTurnedOffByTheEnvironment pins the asymmetry.
//
// Telemetry is opt-in. A setup file may turn it on for a machine whose
// operator chose that file — but an environment saying CASCADE_TELEMETRY=0
// must win over it, and nothing may turn it ON over an environment. The
// two directions are deliberately not symmetrical, which is exactly the
// kind of rule a later refactor straightens out by accident.
func TestTelemetryCanOnlyBeTurnedOffByTheEnvironment(t *testing.T) {
	on := &InitConfig{Schema: SchemaVersion, Telemetry: TelemetryConfig{Enabled: true}}
	off := &InitConfig{Schema: SchemaVersion, Telemetry: TelemetryConfig{Enabled: false}}

	for name, tc := range map[string]struct {
		env  map[string]string
		file *InitConfig
		want bool
	}{
		"a file may turn it on":                {nil, on, true},
		"the environment turns it back off":    {map[string]string{EnvTelemetry: "0"}, on, false},
		"the environment cannot turn it on":    {map[string]string{EnvTelemetry: "1"}, off, false},
		"an unset environment leaves the file": {nil, off, false},
	} {
		t.Run(name, func(t *testing.T) {
			spec, err := Resolve(Flags{}, envFor(tc.env), tc.file)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if spec.Telemetry != tc.want {
				t.Errorf("telemetry = %v, want %v", spec.Telemetry, tc.want)
			}
		})
	}
}

// TestSetFlagsDistinguishSilenceFromFalse is what the prompt guard reads:
// "the file said no harnesses" and "the file said nothing about
// harnesses" are different, and only the second may fall back to a
// default or a prompt.
func TestSetFlagsDistinguishSilenceFromFalse(t *testing.T) {
	silent, err := Resolve(Flags{}, envFor(nil), fileWith("local"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if silent.HarnessesSet || silent.ProvidersSet || silent.PluginsSet {
		t.Errorf("a file mentioning none of them reported them set: %+v", silent)
	}

	explicit, err := Resolve(Flags{}, envFor(nil), &InitConfig{
		Schema:    SchemaVersion,
		Harnesses: HarnessConfig{Install: []string{"claude"}},
		Plugins:   PluginsConfig{Enable: []string{"pbd"}},
		Providers: []ProviderDirective{{Name: "a", Auth: AuthKeyEnv, KeyEnv: "K"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !explicit.HarnessesSet || !explicit.PluginsSet || !explicit.ProvidersSet {
		t.Errorf("a file naming all three did not report them set: %+v", explicit)
	}
}

// TestNoDaemonFlagOverridesAnEnablingFile: a flag is the operator at the
// keyboard now, and it outranks a file they may not have written.
func TestNoDaemonFlagOverridesAnEnablingFile(t *testing.T) {
	spec, err := Resolve(Flags{NoDaemon: true}, envFor(nil),
		&InitConfig{Schema: SchemaVersion, Daemon: DaemonConfig{Install: true}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec.Daemon {
		t.Error("--no-daemon lost to a setup file that asked for the daemon")
	}
	if !spec.DaemonSet {
		t.Error("--no-daemon left the field unset, so a default could still turn it on")
	}
}

// TestYesComesFromEitherTheFlagOrTheEnvironment covers both spellings of
// one instruction.
func TestYesComesFromEitherTheFlagOrTheEnvironment(t *testing.T) {
	for name, tc := range map[string]struct {
		flags Flags
		env   map[string]string
		want  bool
	}{
		"--yes":         {Flags{Yes: true}, nil, true},
		"CASCADE_YES=1": {Flags{}, map[string]string{EnvYes: "1"}, true},
		"CASCADE_YES=0": {Flags{}, map[string]string{EnvYes: "0"}, false},
		"neither":       {Flags{}, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			spec, err := Resolve(tc.flags, envFor(tc.env), nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if spec.Yes != tc.want {
				t.Errorf("Yes = %v, want %v", spec.Yes, tc.want)
			}
		})
	}
}

// TestResolveRefusesAnInvalidMergedProfile catches a bad value wherever
// it came from: the merged spec is validated once, so a profile typo in
// the environment is refused the same way one in the file is.
func TestResolveRefusesAnInvalidMergedProfile(t *testing.T) {
	_, err := Resolve(Flags{}, envFor(map[string]string{EnvProfile: "laptop"}), nil)
	if err == nil {
		t.Fatal("Resolve accepted a profile that does not exist")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestKeyForReadsTheVariableAndNeverTheFile is the whole point of the
// key_env indirection: the file names a variable, and the value comes
// from the environment at the moment the provider is added.
func TestKeyForReadsTheVariableAndNeverTheFile(t *testing.T) {
	directive := ProviderDirective{Name: "anthropic", KeyEnv: "SOME_KEY"}

	got, err := directive.KeyFor(envFor(map[string]string{"SOME_KEY": "the-value"}))
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	if got != "the-value" {
		t.Errorf("KeyFor = %q", got)
	}

	// An unset variable is a refusal NAMING the variable. A provider
	// added with an empty key fails later, somewhere that cannot say
	// which setup file sent it.
	_, err = directive.KeyFor(envFor(nil))
	if err == nil {
		t.Fatal("KeyFor accepted an unset variable")
	}
	if !strings.Contains(err.Error(), "SOME_KEY") {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
}

// TestTheDirectiveRendersWithoutItsValue: this string reaches --check
// output and a log line, and a directive that printed its key would put
// one in both.
func TestTheDirectiveRendersWithoutItsValue(t *testing.T) {
	rendered := ProviderDirective{Name: "anthropic", KeyEnv: "SOME_KEY"}.String()
	if !strings.Contains(rendered, "anthropic") || !strings.Contains(rendered, "SOME_KEY") {
		t.Errorf("String() = %q, want the provider and the variable named", rendered)
	}
}

// TestResolveToleratesANilEnvironment: a caller passing nothing gets
// defaults rather than a panic.
func TestResolveToleratesANilEnvironment(t *testing.T) {
	spec, err := Resolve(Flags{}, nil, nil)
	if err != nil {
		t.Fatalf("Resolve(nil env): %v", err)
	}
	if spec == nil {
		t.Fatal("Resolve returned a nil spec and a nil error")
	}
}

// discoveryFile sets every discovery field to a value the env below
// contradicts, so a mapping that read the wrong source fails.
func discoveryFile() *InitConfig {
	return &InitConfig{
		Schema: SchemaVersion,
		Roots:  RootsConfig{Paths: []string{"/file"}, Maintain: true, MaxFiles: 7, AcceptDiscovered: true},
		Nodes:  NodesConfig{ScanLAN: true, Pair: []string{"file-host"}, Travel: true},
		Policy: PolicyConfig{Preset: "strict"},
	}
}

// discoveryEnv sets every listed key, plus the codes both hosts need.
var discoveryEnv = map[string]string{
	EnvRootsPaths: "/a" + string(filepath.ListSeparator) + "/b", EnvRootsMaintain: "0",
	EnvRootsMaxFiles: "42", EnvRootsAcceptDiscovered: "false", EnvNodesScanLAN: "no",
	EnvNodesPair: "a.invalid, b.invalid", EnvNodesTravel: "off", EnvPolicyPreset: "custom",
	"CASCADE_INIT_NODE_CODE_A_INVALID": "code-one", "CASCADE_INIT_NODE_CODE_B_INVALID": "code-two",
	"CASCADE_INIT_NODE_CODE_FILE_HOST": "code-three",
}

// wantInvalid asserts a KindInvalidInput refusal carrying want, with no spec.
func wantInvalid(t *testing.T, spec *Spec, err error, want string) {
	t.Helper()
	if spec != nil || err == nil {
		t.Fatalf("Resolve = %+v, %v; want a refusal and no spec", spec, err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v (kind %v), want KindInvalidInput naming %q", err, kind, want)
	}
}

// TestInitEnvMappings pins every CASCADE_INIT_ discovery key to its
// DiscoveryOverlay field, an unset key to a nil one, and the refusal of a
// paired host without its code. Precedence rows live in 12a's test.
func TestInitEnvMappings(t *testing.T) {
	o, err := discoveryOverlay(envFor(discoveryEnv))
	if err != nil {
		t.Fatalf("discoveryOverlay: %v", err)
	}
	if o.RootsPaths == nil || fmt.Sprint(*o.RootsPaths) != "[/a /b]" || o.NodesPair == nil || fmt.Sprint(*o.NodesPair) != "[a.invalid b.invalid]" ||
		o.RootsMaxFiles == nil || *o.RootsMaxFiles != 42 || o.PolicyPreset == nil || *o.PolicyPreset != "custom" {
		t.Fatalf("list, number or preset keys misrouted: %+v", o)
	}
	for name, p := range map[string]*bool{"maintain": o.RootsMaintain, "accept": o.RootsAcceptDiscovered,
		"scan": o.NodesScanLAN, "travel": o.NodesTravel} {
		if p == nil || *p {
			t.Fatalf("%s: want a non-nil false, got %v", name, p)
		}
	}
	if o, err = discoveryOverlay(envFor(nil)); err != nil || fmt.Sprintf("%+v", o) != fmt.Sprintf("%+v", DiscoveryOverlay{}) {
		t.Fatalf("empty env must give a silent overlay: %+v, %v", o, err)
	}
	spec, err := Resolve(Flags{}, envFor(discoveryEnv), discoveryFile())
	if err != nil || spec.Policy.Preset != "custom" || spec.Roots.MaxFiles != 42 {
		t.Fatalf("Resolve did not pass the env overlay through the resolver: %+v, %v", spec, err)
	}
	if dump := fmt.Sprintf("%+v", *spec); strings.Contains(dump, "code-") {
		t.Fatalf("a pairing code entered the spec: %s", dump)
	}
	if spec, _ = Resolve(Flags{PolicyPreset: "balanced"}, envFor(discoveryEnv), nil); spec == nil || spec.Policy.Preset != "balanced" {
		t.Fatalf("the --policy flag did not reach the resolver: %+v", spec)
	}
	testInitEnvRefusals(t)
}

// testInitEnvRefusals covers the typed refusals, each before any spec.
func testInitEnvRefusals(t *testing.T) {
	spec, err := Resolve(Flags{}, envFor(map[string]string{EnvNodesPair: "a.invalid,b.invalid",
		"CASCADE_INIT_NODE_CODE_B_INVALID": "code-two"}), nil)
	wantInvalid(t, spec, err, "CASCADE_INIT_NODE_CODE_A_INVALID")
	if strings.Contains(err.Error(), "code-two") {
		t.Fatalf("the refusal quoted a code: %v", err)
	}
	spec, err = Resolve(Flags{}, envFor(nil), discoveryFile())
	wantInvalid(t, spec, err, "CASCADE_INIT_NODE_CODE_FILE_HOST")
	spec, err = Resolve(Flags{}, envFor(map[string]string{EnvRootsMaxFiles: "many"}), nil)
	wantInvalid(t, spec, err, EnvRootsMaxFiles)
	spec, err = Resolve(Flags{}, envFor(map[string]string{EnvRootsMaxFiles: "-1"}), nil)
	wantInvalid(t, spec, err, "max_files")
	spec, err = Resolve(Flags{}, envFor(map[string]string{EnvPolicyPreset: "lax"}), nil)
	wantInvalid(t, spec, err, "lax")
}
