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
	// "Providers", capitalised, is what the envelope actually carries:
	// the result type has no JSON tag on that field, so Go's own name
	// reaches the document. Decoding the snake_case spelling the rest of
	// the contract uses finds nothing. Written as it IS, with the defect
	// recorded in P1-E16-W4-S35-T10 rather than hidden behind a decoder
	// that quietly accepts both.
	var envelope struct {
		Data struct {
			Providers []struct {
				Name string `json:"name"`
			} `json:"Providers"`
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

// TestAcceptInitTheSetupFileKindIsIgnored is a RECORDED HAZARD, not a
// contract: it pins behaviour that is wrong, so the day it changes is
// visible.
//
// `kind` is a documented field of cascade.init/v1 — "pins the provider
// shape, when the author would rather not let the probe decide" — and
// nothing reads it. `providerAddArgs` never passes it, `cascade provider
// add` has no flag to receive it, and `intake.AddRequest` has no field to
// hold it. An author who pins a shape gets whatever the probe picks.
//
// Proved here rather than asserted from the source: the endpoint below
// serves ONLY the openai-compat shape and the file pins exactly that, so
// a build that honoured `kind` would verify successfully. This build
// instead probes its way to another driver, asks for a path the fixture
// does not serve, and the run fails.
//
// When P1-E16-W4-S35-T12 lands, this test fails. Replace it with the
// positive assertion — the setup file pins the shape and the endpoint
// sees that shape's request — rather than deleting it.
func TestAcceptInitTheSetupFileKindIsIgnored(t *testing.T) {
	e := newEnv(t)
	endpoint := newVerifyEndpoint(t, shapeOpenAICompat, "accept-key")

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("scenario A prerequisite exited %d:\n%s", code, out)
	}

	config := e.writeSetupFile(t, endpoint.URL, "openai-compat")
	out, code := e.runWithKey(t, "accept-key", "init", "--config", config, "--no-daemon")
	if code == 0 {
		t.Fatalf("RECORDED HAZARD FIXED: the setup file's kind was honoured and the run succeeded. "+
			"P1-E16-W4-S35-T12 has landed; replace this test with the positive assertion.\n%s", out)
	}
	if got := endpoint.paths(); contains(got, "/v1/chat/completions") {
		t.Fatalf("RECORDED HAZARD FIXED: the endpoint saw the openai-compat request the file pinned (%v). "+
			"Replace this test with the positive assertion.", got)
	}
	t.Logf("RECORDED HAZARD: the setup file pinned kind=openai-compat and the run reached %v instead; "+
		"`kind` is accepted by the schema and read by nothing (P1-E16-W4-S35-T12)", endpoint.paths())
}
