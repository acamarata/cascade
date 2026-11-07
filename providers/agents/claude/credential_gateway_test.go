//go:build !windows

package claude

// Purpose: the gateway credential class at the driver: a gateway spawn
//   carries the token, base URL and host-managed flag and emits no --bare;
//   a malformed gateway lane, or the gateway variables under another class,
//   refuses before any child exists.
// SPORT: providers.agents.claude/ADD (P1-GWY).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// fakeGatewayURL is a synthetic gateway base URL on a reserved TLD.
const fakeGatewayURL = "https://gateway.invalid"

// gatewayPairs are the base-URL and managed-flag pairs of a valid gateway lane.
func gatewayPairs() []string {
	return []string{GatewayBaseURLVar + "=" + fakeGatewayURL, GatewayManagedVar + "=" + GatewayManagedValue}
}

// gatewaySpec turns the fixture's oauth spec and profile into a gateway lane.
func gatewaySpec(f *fx) (provider.AgentJobSpec, Profile) {
	prof, spec := f.profile(), f.spec()
	prof.CredentialClass, prof.KeyVar = CredentialClassGateway, KeyVarGatewayToken
	spec.Env.Driver[3] = KeyVarGatewayToken + "=" + fakeKeyValue
	spec.Env.Driver = append(spec.Env.Driver, gatewayPairs()...)
	return spec, prof
}

func TestClaudeGatewayClassSpawn(t *testing.T) {
	f := newFx(t)
	envOut := filepath.Join(f.dir, "env")
	f.body = "env > " + envOut + "\n" + f.body
	spec, prof := gatewaySpec(f)
	cfg := f.config()
	cfg.Profile = fakeProfile{prof: prof}
	if _, _, err := run(t, f.driver(cfg), spec); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	b, _ := os.ReadFile(envOut)
	env := "\n" + string(b)
	for _, want := range append([]string{KeyVarGatewayToken + "=" + fakeKeyValue}, gatewayPairs()...) {
		if !strings.Contains(env, "\n"+want+"\n") {
			t.Fatalf("child environ lacks %q:\n%s", want, env)
		}
	}
	argv := argvOf(t, f)
	if has(argv, "--bare") || has(argv, "--max-budget-usd") {
		t.Fatalf("a gateway lane emitted an api_key-only flag: %q", argv)
	}
}

func TestClaudeGatewayClassRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(f *fx) (provider.AgentJobSpec, Profile)
		msg    string
		env    bool // the refusal is ErrSpawnEnvRejected
	}{
		"gateway with the api key variable": {func(f *fx) (provider.AgentJobSpec, Profile) {
			spec, prof := gatewaySpec(f)
			prof.KeyVar, spec.Env.Driver[3] = KeyVarAPIKey, KeyVarAPIKey+"="+fakeKeyValue
			return spec, prof
		}, "is not " + KeyVarGatewayToken, true},
		"gateway without the base url": {func(f *fx) (provider.AgentJobSpec, Profile) {
			spec, prof := gatewaySpec(f)
			spec.Env.Driver = spec.Env.Driver[:len(spec.Env.Driver)-2]
			spec.Env.Driver = append(spec.Env.Driver, GatewayManagedVar+"="+GatewayManagedValue)
			return spec, prof
		}, GatewayBaseURLVar + " must be an https URL", true},
		"gateway with managed value zero": {func(f *fx) (provider.AgentJobSpec, Profile) {
			spec, prof := gatewaySpec(f)
			spec.Env.Driver[len(spec.Env.Driver)-1] = GatewayManagedVar + "=0"
			return spec, prof
		}, GatewayManagedVar + " must be", true},
		"oauth carrying the base url": {func(f *fx) (provider.AgentJobSpec, Profile) {
			prof, spec := f.profile(), f.spec()
			spec.Env.Driver = append(spec.Env.Driver, GatewayBaseURLVar+"="+fakeGatewayURL)
			return spec, prof
		}, `key "` + GatewayBaseURLVar + `" is outside the allowlist`, true},
		"unknown class": {func(f *fx) (provider.AgentJobSpec, Profile) {
			prof, spec := f.profile(), f.spec()
			prof.CredentialClass = "subscription"
			return spec, prof
		}, "is not api_key or oauth_token", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			spec, prof := c.mutate(f)
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
