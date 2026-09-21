// Purpose: AllowedEnv tests -- what reaches a step, what cannot, and what
// the operator can add.
// SPORT: internal.ci.AllowedEnv/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"strings"
	"testing"
)

// ambient is a realistic hostile environment: the variables a build needs,
// mixed with credentials and cascade's own settings that must not leak into
// an operator-configured command.
func ambient() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/dev",
		"TMPDIR=/tmp",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"GOCACHE=/home/dev/.cache/go-build",
		"GOMODCACHE=/home/dev/go/pkg/mod",
		"GOFLAGS=-mod=readonly",
		"GOPATH=/home/dev/go",
		"GITHUB_TOKEN=ghp-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"CASCADE_HOME=/home/dev/.cascade",
		"NPM_TOKEN=npm-secret",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
	}
}

func envMap(t *testing.T, kvs []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("AllowedEnv produced %q, which is not a K=V pair", kv)
		}
		out[k] = v
	}
	return out
}

func TestAllowedEnv_PassesWhatABuildNeeds(t *testing.T) {
	got := envMap(t, AllowedEnv(ambient(), nil))
	for _, want := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOPATH"} {
		if _, ok := got[want]; !ok {
			t.Errorf("AllowedEnv dropped %s, which a build step needs", want)
		}
	}
	if got["CI"] != "true" {
		t.Errorf("CI = %q, want \"true\"", got["CI"])
	}
}

func TestAllowedEnv_BlocksCredentialsAndCascadeSettings(t *testing.T) {
	got := envMap(t, AllowedEnv(ambient(), nil))
	for _, blocked := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "CASCADE_HOME", "NPM_TOKEN", "SSH_AUTH_SOCK"} {
		if v, ok := got[blocked]; ok {
			t.Errorf("AllowedEnv passed %s=%q through; the step environment is built, never inherited", blocked, v)
		}
	}
}

// TestAllowedEnv_OperatorNamedKeysPassThrough proves [ci.local] env widens
// the list -- and that it widens it by NAME, taking the value from the
// ambient environment rather than from config.
func TestAllowedEnv_OperatorNamedKeysPassThrough(t *testing.T) {
	got := envMap(t, AllowedEnv(ambient(), []string{"NPM_TOKEN", "NOT_SET_ANYWHERE"}))
	if got["NPM_TOKEN"] != "npm-secret" {
		t.Errorf("NPM_TOKEN = %q, want the ambient value once the operator named it", got["NPM_TOKEN"])
	}
	if _, ok := got["NOT_SET_ANYWHERE"]; ok {
		t.Error("a named key that is unset in the environment must be absent, never invented")
	}
	if _, ok := got["GITHUB_TOKEN"]; ok {
		t.Error("naming one key must not widen the list to every credential")
	}
}

// TestAllowedEnv_AmbientCICannotDisableTheMarker proves CI=true wins: a
// step must not be able to see CI=0 because the calling shell had it.
func TestAllowedEnv_AmbientCICannotDisableTheMarker(t *testing.T) {
	got := envMap(t, AllowedEnv([]string{"PATH=/bin", "CI=0"}, []string{"CI"}))
	if got["CI"] != "true" {
		t.Errorf("CI = %q, want \"true\" even when the ambient environment says otherwise", got["CI"])
	}
	if strings.Count(strings.Join(AllowedEnv([]string{"CI=0"}, nil), " "), "CI=") != 1 {
		t.Error("CI must appear exactly once in a step's environment")
	}
}

// TestAllowedEnv_EmptyAmbientStillCarriesCI proves the floor: even with
// nothing to filter, a step gets the CI marker and nothing else.
func TestAllowedEnv_EmptyAmbientStillCarriesCI(t *testing.T) {
	got := AllowedEnv(nil, nil)
	if len(got) != 1 || got[0] != ciEnvMarker {
		t.Errorf("AllowedEnv(nil, nil) = %v, want exactly [%s]", got, ciEnvMarker)
	}
}

// TestAllowedEnv_IgnoresMalformedEntries proves a value with no "=" (which
// os.Environ can carry on some platforms) is skipped rather than passed on
// as a nameless entry.
func TestAllowedEnv_IgnoresMalformedEntries(t *testing.T) {
	got := AllowedEnv([]string{"PATH=/bin", "MALFORMED"}, []string{"MALFORMED"})
	for _, kv := range got {
		if kv == "MALFORMED" {
			t.Error("a malformed environment entry reached a step")
		}
	}
}
