package initconfig

// Purpose: the [roots], [nodes] and [policy] contract (R-16.34): what the
//   tables parse to, the MaxFiles default, the defaults < file < env <
//   flags precedence, and the exact refusal for every bad value.
// Constraints: every refusal is checked for its kind AND its exact
//   message, because the key in the message is what sends an author to
//   the right line.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The R-16.34 seed bodies, shared with the FuzzInitConfigSchema seed list.
const (
	seedDiscoveryBody     = "schema = \"cascade.init/v1\"\n\n[roots]\npaths = [\"~/Code\", \"~/Sites\"]\nmaintain = true\nmax_files = 200000\naccept_discovered = false\n\n[nodes]\nscan_lan = false\npair = [\"mini.local\"]\ntravel = true\n\n[policy]\npreset = \"balanced\"\n"
	seedUnknownPresetBody = "schema = \"cascade.init/v1\"\n\n[policy]\npreset = \"lax\"\n"
	seedMalformedPairBody = "schema = \"cascade.init/v1\"\n\n[nodes]\npair = [\"\", \"ok.local\"]\n"
)

// discoveryTables is appended to validConfig in the schema test.
const discoveryTables = `
[roots]
paths = ["~/Code", "~/Sites"]
maintain = true
max_files = 5000
accept_discovered = true

[nodes]
scan_lan = true
pair = ["mini.local", "nas.invalid"]
travel = true

[policy]
preset = "strict"
`

// wantRefusal requires err to be KindInvalidInput with exactly msg.
func wantRefusal(t *testing.T, err error, msg string) {
	t.Helper()
	var e *cascade.Error
	if !errors.As(err, &e) {
		t.Fatalf("want a taxonomy refusal %q, got %v", msg, err)
	}
	if e.Kind != cascade.KindInvalidInput || e.Msg != msg {
		t.Fatalf("got %v %q, want %v %q", e.Kind, e.Msg, cascade.KindInvalidInput, msg)
	}
}

// parseRefused requires Parse to refuse body with exactly msg and no config.
func parseRefused(t *testing.T, body, msg string) {
	t.Helper()
	cfg, err := Parse([]byte(body))
	if cfg != nil {
		t.Fatalf("Parse returned a config alongside a refusal: %+v", cfg)
	}
	wantRefusal(t, err, msg)
}

func TestInitConfigDiscoverySchema(t *testing.T) {
	cfg, err := Parse([]byte(validConfig + discoveryTables))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantRoots := RootsConfig{Paths: []string{"~/Code", "~/Sites"}, Maintain: true, MaxFiles: 5000, AcceptDiscovered: true}
	wantNodes := NodesConfig{ScanLAN: true, Pair: []string{"mini.local", "nas.invalid"}, Travel: true}
	if !reflect.DeepEqual(cfg.Roots, wantRoots) || !reflect.DeepEqual(cfg.Nodes, wantNodes) ||
		cfg.Policy != (PolicyConfig{Preset: PresetStrict}) {
		t.Fatalf("got %+v %+v %+v", cfg.Roots, cfg.Nodes, cfg.Policy)
	}
	head := "schema = \"cascade.init/v1\"\n"
	parseRefused(t, head+"[roots]\nmax_file = 1\n",
		`cascade init: unknown key "roots.max_file" in the setup file; did you mean "roots.max_files"?`)
	cfg, err = Parse([]byte(head + "[nodes]\nextra = true\n"))
	if cfg != nil || !cascade.HasKind(err, cascade.KindInvalidInput) ||
		!strings.Contains(err.Error(), `unknown key "nodes.extra"`) {
		t.Fatalf("nodes.extra: cfg %v, err %v", cfg, err)
	}
	parseRefused(t, head+"[nodes]\npair = [1]\n", "cascade init: the setup file is not valid TOML")
}

func TestDiscoveryMaxFilesDefaults(t *testing.T) {
	d, err := ResolveDiscovery(nil, DiscoveryOverlay{}, DiscoveryOverlay{})
	if err != nil || !reflect.DeepEqual(d, Discovery{Roots: RootsConfig{MaxFiles: DefaultRootsMaxFiles}}) {
		t.Fatalf("defaults: %+v, %v", d, err)
	}
	if DefaultRootsMaxFiles != 200000 {
		t.Fatalf("DefaultRootsMaxFiles = %d", DefaultRootsMaxFiles)
	}
	head := "schema = \"cascade.init/v1\"\n[roots]\n"
	for body, want := range map[string]int{head + "maintain = true\n": 200000, head + "max_files = 5000\n": 5000} {
		cfg, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("Parse %q: %v", body, err)
		}
		if d, err := ResolveDiscovery(cfg, DiscoveryOverlay{}, DiscoveryOverlay{}); err != nil || d.Roots.MaxFiles != want {
			t.Fatalf("%q: MaxFiles %d, err %v; want %d", body, d.Roots.MaxFiles, err, want)
		}
	}
	parseRefused(t, head+"max_files = -1\n",
		"cascade init: roots.max_files: -1 is negative; use 0 for the default or a positive count")
}

func ptr[T any](v T) *T { return &v }

func TestDiscoveryPrecedence(t *testing.T) {
	cfg, err := Parse([]byte("schema = \"cascade.init/v1\"\n[roots]\nmax_files = 1000\n" +
		"[nodes]\nscan_lan = false\npair = [\"file.local\"]\n[policy]\npreset = \"strict\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	env := DiscoveryOverlay{PolicyPreset: ptr(PresetBalanced), RootsMaxFiles: ptr(2000),
		NodesScanLAN: ptr(true), NodesPair: ptr([]string{"env.local"})}
	flags := DiscoveryOverlay{PolicyPreset: ptr(PresetCustom), RootsMaxFiles: ptr(3000),
		NodesScanLAN: ptr(false), NodesPair: ptr([]string{"flag.local"})}
	check := func(name string, env, flags DiscoveryOverlay, preset string, maxFiles int, scan bool, pair string) {
		t.Helper()
		d, err := ResolveDiscovery(cfg, env, flags)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Policy.Preset != preset || d.Roots.MaxFiles != maxFiles || d.Nodes.ScanLAN != scan ||
			!reflect.DeepEqual(d.Nodes.Pair, []string{pair}) {
			t.Fatalf("%s: got %+v", name, d)
		}
	}
	check("flags win", env, flags, PresetCustom, 3000, false, "flag.local")
	check("env over file", env, DiscoveryOverlay{}, PresetBalanced, 2000, true, "env.local")
	check("file alone", DiscoveryOverlay{}, DiscoveryOverlay{}, PresetStrict, 1000, false, "file.local")

	d, _ := ResolveDiscovery(cfg, env, flags)
	(*flags.NodesPair)[0] = "mutated.local"
	if d.Nodes.Pair[0] != "flag.local" {
		t.Fatalf("a resolved slice aliases the overlay: %v", d.Nodes.Pair)
	}
	d, err = ResolveDiscovery(cfg, DiscoveryOverlay{PolicyPreset: ptr("lax")}, DiscoveryOverlay{})
	if !reflect.DeepEqual(d, Discovery{}) {
		t.Fatalf("a refusal returned %+v", d)
	}
	wantRefusal(t, err, `cascade init: policy.preset: unknown preset "lax"; use "balanced", "strict" or "custom"`)
}

func TestDiscoveryRefusalsNameTheKey(t *testing.T) {
	for _, tc := range []struct {
		overlay DiscoveryOverlay
		body    string
		msg     string
	}{
		{DiscoveryOverlay{PolicyPreset: ptr("lax")}, "[policy]\npreset = \"lax\"\n",
			`cascade init: policy.preset: unknown preset "lax"; use "balanced", "strict" or "custom"`},
		{DiscoveryOverlay{RootsPaths: ptr([]string{"a", " "})}, "[roots]\npaths = [\"a\", \" \"]\n",
			"cascade init: roots.paths[1]: the path is empty"},
		{DiscoveryOverlay{RootsPaths: ptr([]string{"a", "a"})}, "[roots]\npaths = [\"a\", \"a\"]\n",
			`cascade init: roots.paths[1]: "a" is listed twice`},
		{DiscoveryOverlay{NodesPair: ptr([]string{"bad host"})}, "[nodes]\npair = [\"bad host\"]\n",
			`cascade init: nodes.pair[0]: "bad host" is not a host name`},
		{DiscoveryOverlay{NodesPair: ptr([]string{"x.local", "x.local"})}, "[nodes]\npair = [\"x.local\", \"x.local\"]\n",
			`cascade init: nodes.pair[1]: "x.local" is listed twice`},
	} {
		d, err := ResolveDiscovery(nil, DiscoveryOverlay{}, tc.overlay)
		if !reflect.DeepEqual(d, Discovery{}) {
			t.Fatalf("%s: a refusal returned %+v", tc.msg, d)
		}
		wantRefusal(t, err, tc.msg)
		parseRefused(t, "schema = \"cascade.init/v1\"\n"+tc.body, tc.msg)
	}
	long := DiscoveryOverlay{NodesPair: ptr([]string{strings.Repeat("a", 254)})}
	_, err := ResolveDiscovery(nil, DiscoveryOverlay{}, long)
	wantRefusal(t, err, "cascade init: nodes.pair[0]: the host is longer than 253 characters")
}

// readSeed decodes one "go test fuzz v1" corpus file holding a string.
func readSeed(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fuzz", "FuzzInitConfigSchema", name))
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	header, rest, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	inner, ok := strings.CutPrefix(rest, "string(")
	if header != "go test fuzz v1" || !ok || !strings.HasSuffix(inner, ")") {
		t.Fatalf("seed %s is not a go fuzz string corpus file", name)
	}
	body, err := strconv.Unquote(strings.TrimSuffix(inner, ")"))
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return body
}

func TestDiscoveryCorpusSeedsPresent(t *testing.T) {
	body := readSeed(t, "seed_r16_34_discovery")
	if body != seedDiscoveryBody {
		t.Fatalf("seed_r16_34_discovery drifted from the f.Add body")
	}
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse seed_r16_34_discovery: %v", err)
	}
	d, err := ResolveDiscovery(cfg, DiscoveryOverlay{}, DiscoveryOverlay{})
	if err != nil || !reflect.DeepEqual(d.Roots.Paths, []string{"~/Code", "~/Sites"}) ||
		d.Roots.MaxFiles != 200000 || !reflect.DeepEqual(d.Nodes.Pair, []string{"mini.local"}) ||
		d.Policy.Preset != PresetBalanced || !d.Roots.Maintain || !d.Nodes.Travel {
		t.Fatalf("seed resolved to %+v, %v", d, err)
	}
	for name, want := range map[string]struct{ body, msg string }{
		"seed_unknown_preset": {seedUnknownPresetBody,
			`cascade init: policy.preset: unknown preset "lax"; use "balanced", "strict" or "custom"`},
		"seed_malformed_pair": {seedMalformedPairBody, "cascade init: nodes.pair[0]: the host is empty"},
	} {
		got := readSeed(t, name)
		if got != want.body {
			t.Fatalf("%s drifted from the f.Add body", name)
		}
		parseRefused(t, got, want.msg)
	}
}
