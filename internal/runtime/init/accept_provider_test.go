//go:build integration

package init_test

// Purpose: scenario D of P1-E16-W4-S35-T5 and its error path — a provider
//   wired through `cascade init --config`, and a credential the endpoint
//   rejects.
// Constraints: the only stand-in here is the endpoint's RESPONSE, whose
//   provenance is stated in testdata/accept/README.md. Everything cascade
//   does is real: the binary, the child `provider add`, the credential
//   read from an environment variable, and the request that reaches the
//   server — which is asserted on, so a change to what cascade sends
//   fails these scenarios.
// SPORT: internal/runtime/init acceptance (ADD) — P1-E16-W4-S35-T5.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keyEnvVar is the variable the setup file names. The credential is read
// from it at the moment the provider is added and never enters the setup
// file, the journal or a log line.
const keyEnvVar = "CASCADE_ACCEPT_PROVIDER_KEY"

// TestAcceptInitScenarioD wires a provider through the setup file and
// requires the whole chain to hold: the key comes from the named variable,
// the endpoint sees a real verify exchange, and the provider is registered
// afterwards.
func TestAcceptInitScenarioD(t *testing.T) {
	e := newEnv(t)
	endpoint := newVerifyEndpoint(t, shapeAnthropic, "accept-key")

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("scenario A prerequisite exited %d:\n%s", code, out)
	}

	config := e.writeSetupFile(t, endpoint.URL, "")
	out, code := e.runWithKey(t, "accept-key", "init", "--config", config, "--no-daemon")
	if code != 0 {
		t.Fatalf("cascade init --config exited %d:\n%s", code, out)
	}

	if got := endpoint.paths(); !contains(got, "/v1/messages") {
		t.Errorf("the endpoint saw %v; the micro-verify never ran", got)
	}
	if key := endpoint.lastKey(); key != "accept-key" {
		t.Errorf("the endpoint saw the credential %q, want the value of %s", key, keyEnvVar)
	}

	names, code := e.providerNames(t)
	if code != 0 {
		t.Fatalf("cascade provider list exited %d", code)
	}
	if !contains(names, "accept-provider") {
		t.Errorf("provider list = %v, want the provider the setup file named", names)
	}

	// The harness must survive the second run, since this is the same
	// wizard: a `--config` run that un-wired what `--yes` wired would be
	// the convergence rule broken from the other direction.
	if row := e.harnessRow(t, "claude"); !row.Detected || !row.CascadeRegistered {
		t.Errorf("the harness regressed across the --config run: %+v", row)
	}
}

// TestAcceptInitProviderFailure: a credential the endpoint rejects fails
// the setup run, says so, and leaves no provider behind.
//
// The journal assertion is the one that matters. A failed run's journal is
// what lets the operator fix the variable and resume rather than start
// over, and a run that deleted it on the way out would make the failure
// more expensive than it is.
func TestAcceptInitProviderFailure(t *testing.T) {
	e := newEnv(t)
	endpoint := newVerifyEndpoint(t, shapeAnthropic, "accept-key")

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("scenario A prerequisite exited %d:\n%s", code, out)
	}

	config := e.writeSetupFile(t, endpoint.URL, "")
	out, code := e.runWithKey(t, "the-wrong-key", "init", "--config", config, "--no-daemon")
	if code == 0 {
		t.Fatalf("cascade init --config succeeded with a credential the endpoint rejects:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "accept-provider") {
		t.Errorf("the failure does not name the provider that failed:\n%s", out)
	}

	names, _ := e.providerNames(t)
	if contains(names, "accept-provider") {
		t.Errorf("provider list = %v; a provider whose credential was rejected was recorded anyway", names)
	}
	if !exists(e.journal()) {
		t.Errorf("a failed run deleted its journal; the operator has to start over rather than resume")
	}
}

// writeSetupFile writes a cascade.init/v1 file naming the provider, the
// variable holding its key, and the fixture endpoint.
func (e env) writeSetupFile(t *testing.T, baseURL, kind string) string {
	t.Helper()
	path := filepath.Join(e.project, "cascade-init.toml")
	kindLine := ""
	if kind != "" {
		kindLine = "kind = \"" + kind + "\"\n"
	}
	mustWrite(t, path, `schema = "cascade.init/v1"

[[providers]]
name = "accept-provider"
`+kindLine+`auth = "key-env"
key_env = "`+keyEnvVar+`"
base_url = "`+baseURL+`"
verify = true
`)
	return path
}

// runWithKey runs the binary with the credential variable set.
func (e env) runWithKey(t *testing.T, key string, args ...string) (string, int) {
	t.Helper()
	t.Setenv(keyEnvVar, key)
	defer os.Unsetenv(keyEnvVar)
	return e.runEnv(t, append(e.environ(), keyEnvVar+"="+key), args...)
}

// providerNames lists the registered providers by name.
func (e env) providerNames(t *testing.T) ([]string, int) {
	t.Helper()
	out, code := e.run(t, "provider", "list", "--json")
	if code != 0 {
		return nil, code
	}
	var envelope struct {
		Data struct {
			Providers []struct {
				Name string `json:"name"`
			} `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonTail(out)), &envelope); err != nil {
		t.Fatalf("decoding provider list: %v\n%s", err, out)
	}
	names := make([]string, 0, len(envelope.Data.Providers))
	for _, p := range envelope.Data.Providers {
		names = append(names, p.Name)
	}
	return names, 0
}

// TestAcceptInitTheSetupFileKindPinsTheShape replaces the recorded hazard
// P1-E16-W4-S35-T5 left here, now that P1-E16-W4-S35-T12 has landed.
//
// The hazard asserted the opposite — that `kind` was accepted by the
// schema and read by nobody — and it was written to fail the day that
// changed, naming the ticket and saying to replace it with this rather
// than delete it. This is that replacement, and it is the same setup: the
// endpoint serves ONLY the openai-compat shape, and the file pins exactly
// that. Before the pin was honoured the probe reached the anthropic driver
// and asked for a path the fixture does not serve.
func TestAcceptInitTheSetupFileKindPinsTheShape(t *testing.T) {
	e := newEnv(t)
	endpoint := newVerifyEndpoint(t, shapeOpenAICompat, "accept-key")

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("scenario A prerequisite exited %d:\n%s", code, out)
	}

	config := e.writeSetupFile(t, endpoint.URL, "openai-compat")
	out, code := e.runWithKey(t, "accept-key", "init", "--config", config, "--no-daemon")
	if code != 0 {
		t.Fatalf("a run pinning the shape the endpoint speaks exited %d:\n%s", code, out)
	}
	if got := endpoint.paths(); !contains(got, "/v1/chat/completions") {
		t.Errorf("the endpoint saw %v; the pinned openai-compat verify never ran", got)
	}
	if names, _ := e.providerNames(t); !contains(names, "accept-provider") {
		t.Errorf("provider list = %v, want the provider the setup file named", names)
	}
}

// TestAcceptInitAWrongPinSaysSo: pinning a shape the endpoint does not
// speak must name the PIN. "None of anthropic-compat, openai-compat or
// gemini matched this credential" sends an author to check their key,
// when what they need to know is that the shape they named is wrong.
func TestAcceptInitAWrongPinSaysSo(t *testing.T) {
	e := newEnv(t)
	endpoint := newVerifyEndpoint(t, shapeAnthropic, "accept-key")

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("scenario A prerequisite exited %d:\n%s", code, out)
	}

	config := e.writeSetupFile(t, endpoint.URL, "openai-compat")
	out, code := e.runWithKey(t, "accept-key", "init", "--config", config, "--no-daemon")
	if code == 0 {
		t.Fatalf("a run pinning a shape the endpoint does not speak succeeded:\n%s", out)
	}
	if !strings.Contains(out, "openai-compat") {
		t.Errorf("the failure does not name the pin:\n%s", out)
	}
	if strings.Contains(out, "none of anthropic-compat") {
		t.Errorf("a wrong pin reported as an unmatched credential:\n%s", out)
	}
}
