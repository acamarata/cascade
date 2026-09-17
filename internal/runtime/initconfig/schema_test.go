package initconfig

// Purpose: the schema parser's contract (P1-E16-W4-S35-T7) — what a valid
//   setup file produces, and every way an invalid one is refused.
// Constraints: every refusal is checked for its taxonomy kind, because
//   the exit code an operator's CI branches on comes from that kind.

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// validConfig is the 08 §2 example, which every negative case below
// mutates one field of.
const validConfig = `
schema = "cascade.init/v1"
profile = "local"

[plugins]
enable = ["cascade-claude", "pbd"]
disable = ["cascade-pa"]
harness = "auto"

[[providers]]
name = "anthropic"
kind = "anthropic"
auth = "key-env"
key_env = "ANTHROPIC_API_KEY"
verify = true

[harnesses]
detect = true
install = ["claude", "codex"]

[server]
postgres_dsn_env = "CASCADE_PG_DSN"
redis_url_env = "CASCADE_REDIS_URL"
s3_env_prefix = "CASCADE_S3"

[telemetry]
enabled = false

[daemon]
install = true
`

// TestParseReadsEveryField requires the whole document to survive the
// parse. A field silently dropped is a machine configured differently
// from what its author wrote, with nothing anywhere saying so.
func TestParseReadsEveryField(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Schema != SchemaVersion || cfg.Profile != "local" {
		t.Errorf("schema = %q, profile = %q", cfg.Schema, cfg.Profile)
	}
	if len(cfg.Plugins.Enable) != 2 || cfg.Plugins.Harness != "auto" {
		t.Errorf("plugins = %+v", cfg.Plugins)
	}
	if len(cfg.Plugins.Disable) != 1 {
		t.Errorf("disable = %v", cfg.Plugins.Disable)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %+v", cfg.Providers)
	}
	p := cfg.Providers[0]
	// "anthropic", not "anthropic-compat": the latter is PROSE, used in
	// the probe-order comments and in one error message, and was never a
	// DriverKind. This fixture carried it until the parser started
	// checking the field (P1-E16-W4-S35-T12) -- which is what a field
	// nothing reads looks like from the test side.
	if p.Name != "anthropic" || p.KeyEnv != "ANTHROPIC_API_KEY" || !p.Verify || p.Kind != "anthropic" {
		t.Errorf("provider = %+v", p)
	}
	if !cfg.Harnesses.Detect || len(cfg.Harnesses.Install) != 2 {
		t.Errorf("harnesses = %+v", cfg.Harnesses)
	}
	if cfg.Server.PostgresDSNEnv != "CASCADE_PG_DSN" || cfg.Server.S3EnvPrefix != "CASCADE_S3" {
		t.Errorf("server = %+v", cfg.Server)
	}
	if cfg.Telemetry.Enabled {
		t.Error("telemetry read as enabled from a file that says false")
	}
	if !cfg.Daemon.Install {
		t.Error("daemon.install read as false from a file that says true")
	}
}

// TestAnUnknownKeyIsRefusedWithASuggestion: a typo'd key that was
// silently ignored produces a machine configured differently from what
// its author wrote, and nothing tells them.
func TestAnUnknownKeyIsRefusedWithASuggestion(t *testing.T) {
	for body, want := range map[string]string{
		"schema = \"cascade.init/v1\"\nprofil = \"local\"\n":           `"profile"`,
		"schema = \"cascade.init/v1\"\n[plugins]\nenabel = [\"a\"]\n":  `"plugins.enable"`,
		"schema = \"cascade.init/v1\"\n[daemon]\ninstal = true\n":      `"daemon.install"`,
		"schema = \"cascade.init/v1\"\n[telemetry]\nenabledd = true\n": `"telemetry.enabled"`,
	} {
		t.Run(want, func(t *testing.T) {
			_, err := Parse([]byte(body))
			if err == nil {
				t.Fatalf("Parse accepted a document with an unknown key:\n%s", body)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not suggest %s: %v", want, err)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
			}
		})
	}
}

// TestAWildKeyGetsNoSuggestion: one bad guess teaches an operator to
// ignore every later one, so a key nothing resembles is refused plainly.
func TestAWildKeyGetsNoSuggestion(t *testing.T) {
	_, err := Parse([]byte("schema = \"cascade.init/v1\"\nkubernetes_namespace = \"x\"\n"))
	if err == nil {
		t.Fatal("Parse accepted an unknown key")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("a key resembling nothing still drew a suggestion: %v", err)
	}
}

// TestAFutureSchemaIsRefused: reading a newer document with this parser
// means acting on whichever fields it happens to recognise, which
// configures a machine from half a document.
func TestAFutureSchemaIsRefused(t *testing.T) {
	_, err := Parse([]byte(`schema = "cascade.init/v99"`))
	if err == nil {
		t.Fatal("Parse accepted a schema this build does not read")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("kind = %v (typed %t), want KindUnsupported", kind, ok)
	}
	if !strings.Contains(err.Error(), "upgrade cascade") {
		t.Errorf("the refusal does not say what to do about it: %v", err)
	}
}

// TestAMissingSchemaIsRefused: a file with no version claim is not a
// version-1 file, it is a file whose author has not said.
func TestAMissingSchemaIsRefused(t *testing.T) {
	if _, err := Parse([]byte(`profile = "local"`)); err == nil {
		t.Fatal("Parse accepted a document with no schema field")
	}
}

// TestOAuthIsRefusedBeforeAnyStep is R-14.53: a file-driven run has
// nobody at the browser, so an OAuth directive can only hang or fail
// confusingly. The refusal names the alternative.
func TestOAuthIsRefusedBeforeAnyStep(t *testing.T) {
	_, err := Parse([]byte(`
schema = "cascade.init/v1"
[[providers]]
name = "anthropic"
auth = "oauth"
key_env = "ANTHROPIC_API_KEY"
`))
	if err == nil {
		t.Fatal("Parse accepted an oauth directive on the file-driven path")
	}
	if !strings.Contains(err.Error(), "key-env") {
		t.Errorf("the refusal does not cite the key-auth alternative: %v", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestAnIncompleteProviderIsRefused: a file-driven run reads the key from
// the environment, and a directive naming no variable is asking for a
// prompt that will not come.
func TestAnIncompleteProviderIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no key_env": "schema = \"cascade.init/v1\"\n[[providers]]\nname = \"a\"\n",
		"no name":    "schema = \"cascade.init/v1\"\n[[providers]]\nkey_env = \"K\"\n",
		"bad auth":   "schema = \"cascade.init/v1\"\n[[providers]]\nname=\"a\"\nauth=\"magic\"\nkey_env=\"K\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatalf("Parse accepted %s", name)
			}
		})
	}
}

// TestMalformedTOMLIsRefusedAsSuch keeps a syntax error from being
// reported as an unknown key, which would send its author to the wrong
// line.
func TestMalformedTOMLIsRefusedAsSuch(t *testing.T) {
	_, err := Parse([]byte("schema = \n[[["))
	if err == nil {
		t.Fatal("Parse accepted a document that is not TOML")
	}
	if strings.Contains(err.Error(), "unknown key") {
		t.Errorf("a syntax error was reported as an unknown key: %v", err)
	}
}

// FuzzInitConfigSchema drives the parser over untrusted bytes: this reads
// a file a person hands the CLI, so it must refuse rather than panic on
// anything, and must never accept the shapes the rules above forbid.
func FuzzInitConfigSchema(f *testing.F) {
	for _, seed := range []string{
		validConfig, "", "schema = \"cascade.init/v1\"", "[[[", "schema = 1",
		"schema = \"cascade.init/v1\"\n[[providers]]\nname=\"a\"\nauth=\"oauth\"\n",
		"schema = \"cascade.init/v1\"\nprofil = \"local\"\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		cfg, err := Parse([]byte(body))
		if err != nil {
			if cfg != nil {
				t.Fatalf("a refusal still returned a config for %q", body)
			}
			return
		}
		if cfg.Schema != SchemaVersion {
			t.Fatalf("Parse accepted schema %q", cfg.Schema)
		}
		for _, p := range cfg.Providers {
			if p.Auth == AuthOAuth {
				t.Fatalf("Parse accepted an oauth directive: %+v", p)
			}
			if p.Name == "" || p.KeyEnv == "" {
				t.Fatalf("Parse accepted an incomplete directive: %+v", p)
			}
		}
	})
}
