//go:build !windows

// Purpose: tests for the B-1 key variable beside the config directory: one key
//   variable per credential class, refused before any child otherwise.
// Constraints: every credential value is the synthetic fakeKeyValue.
// SPORT: providers.agents.claude tests (P1-AGT-01).

package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaudeKeyVarBesideConfigDir(t *testing.T) {
	cases := map[string]struct {
		class CredentialClass
		key   string
		bare  bool
	}{
		"oauth_token": {CredentialClassOAuthToken, KeyVarOAuthToken, false},
		"api_key":     {CredentialClassAPIKey, KeyVarAPIKey, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			envOut := filepath.Join(f.dir, "env")
			f.body = "env > " + envOut + "\n" + f.body
			prof, spec := f.profile(), f.spec()
			prof.CredentialClass, prof.KeyVar = c.class, c.key
			spec.Env.Driver[3] = c.key + "=" + fakeKeyValue
			cfg := f.config()
			cfg.Profile = fakeProfile{prof: prof}
			if _, _, err := run(t, f.driver(cfg), spec); err != nil {
				t.Fatalf("Collect: %v", err)
			}
			b, _ := os.ReadFile(envOut)
			env := "\n" + string(b)
			if !strings.Contains(env, "\n"+c.key+"="+fakeKeyValue+"\n") || !strings.Contains(env, "\n"+VendorConfigDirVar+"="+f.vendor+"\n") {
				t.Fatalf("child environ lacks %s beside %s:\n%s", c.key, VendorConfigDirVar, env)
			}
			if got := has(argvOf(t, f), "--bare"); got != c.bare {
				t.Fatalf("--bare emitted = %v, want %v for class %s", got, c.bare, c.class)
			}
		})
	}
}

func TestClaudeKeyVarRefusals(t *testing.T) {
	const second = KeyVarAPIKey + "=" + fakeKeyValue
	cases := map[string]struct {
		mutate func(s *provider.AgentJobSpec, p *Profile)
		msg    string
		env    bool // the refusal is ErrSpawnEnvRejected
	}{
		"key var outside the pair": {func(s *provider.AgentJobSpec, p *Profile) {
			p.KeyVar, s.Env.Driver[3] = "GITHUB_TOKEN", "GITHUB_TOKEN="+fakeKeyValue
		}, "is not CLAUDE_CODE_OAUTH_TOKEN", true},
		"key var of the other class": {func(_ *provider.AgentJobSpec, p *Profile) {
			p.CredentialClass = CredentialClassAPIKey
		}, "is not ANTHROPIC_API_KEY", true},
		"profile names no key var": {func(_ *provider.AgentJobSpec, p *Profile) { p.KeyVar = "" }, "is not CLAUDE_CODE_OAUTH_TOKEN", true},
		"unknown credential class": {func(_ *provider.AgentJobSpec, p *Profile) { p.CredentialClass = "subscription" },
			"is not api_key or oauth_token", false},
		"key var without the config dir": {func(s *provider.AgentJobSpec, _ *Profile) {
			s.Env.Driver = []string{s.Env.Driver[0], s.Env.Driver[1], s.Env.Driver[3]}
		}, "under the runtime home", true},
		"key var absent from the environ": {func(s *provider.AgentJobSpec, _ *Profile) { s.Env.Driver = s.Env.Driver[:3] },
			"must be set beside", true},
		"key var with an empty value": {func(s *provider.AgentJobSpec, _ *Profile) { s.Env.Driver[3] = KeyVarOAuthToken + "=" },
			"must be set beside", true},
		"two key vars": {func(s *provider.AgentJobSpec, _ *Profile) { s.Env.Driver = append(s.Env.Driver, second) },
			`key "ANTHROPIC_API_KEY" is outside the allowlist`, true},
		"key var in the tool environ": {func(s *provider.AgentJobSpec, _ *Profile) {
			s.Env.Tool = append(s.Env.Tool, KeyVarOAuthToken+"="+fakeKeyValue)
		}, `key "CLAUDE_CODE_OAUTH_TOKEN" is outside the allowlist`, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			prof, spec := f.profile(), f.spec()
			c.mutate(&spec, &prof)
			cfg := f.config()
			cfg.Profile = fakeProfile{prof: prof}
			_, err := f.driver(cfg).Spawn(context.Background(), spec)
			if err == nil || !strings.Contains(err.Error(), c.msg) || errors.Is(err, ErrSpawnEnvRejected) != c.env {
				t.Fatalf("Spawn = %v, want a refusal naming %q (env rejection %v)", err, c.msg, c.env)
			}
			if exists(f.anyRan) {
				t.Fatal("a subprocess started despite the refusal")
			}
			if strings.Contains(err.Error(), fakeKeyValue) {
				t.Fatalf("refusal %q carries the key value", err)
			}
		})
	}
}

// TestClaudeKeyValueLeakFailsClosed: the real key variable's value is
// scanned like the seeded canary, in output, stderr and written files.
func TestClaudeKeyValueLeakFailsClosed(t *testing.T) {
	const key = `"$` + KeyVarOAuthToken + `"`
	leaks := map[string]string{
		"written file, exact":  `printf 'x=%s\n' ` + key + ` > out.txt`,
		"written file, base64": `printf 'ab%s' ` + key + ` | base64 > out.b64`,
		"stderr, hex":          `printf '%s' ` + key + ` | od -An -tx1 | tr -d ' \n' >&2`,
		"artifact name":        `touch "leak-"` + key + `".txt"`,
	}
	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.body = leak + "\n" + f.body
			_, cr, err := run(t, f.driver(f.config()), f.spec())
			if !errors.Is(err, ErrCredentialCanary) || cr.Output != "" || strings.Contains(err.Error(), fakeKeyValue) {
				t.Fatalf("Collect = %v (output %q): the key value in %s must fail the run closed", err, cr.Output, name)
			}
		})
	}
	t.Run("a job that never emits the value passes", func(t *testing.T) {
		f := newFx(t)
		f.body = `[ -n ` + key + ` ] || exit 9` + "\n" + f.body
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatalf("Collect = %v: reading the variable is not a leak", err)
		}
	})
}

func TestKeyValueFormsIgnoresShortAndAbsentValues(t *testing.T) {
	env := []string{"A=1", KeyVarAPIKey + "=short", KeyVarOAuthToken + "=" + fakeKeyValue}
	if keyValueForms(env, KeyVarAPIKey) != nil || keyValueForms(env, "MISSING") != nil {
		t.Fatal("a short or absent value must yield no forms")
	}
	if got := keyValueForms(env, KeyVarOAuthToken); len(got) == 0 || string(got[0]) != fakeKeyValue {
		t.Fatalf("forms = %q, want the exact value first", got)
	}
}
