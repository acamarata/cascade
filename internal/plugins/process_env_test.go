//go:build !windows

// Purpose: childEnv is a closed allowlist: the base names getenv answers,
// plus the GitHub token names only for a cascade-github record whose
// effective grants (from a witnessed grant) hold oauth:repo; never the
// principal token, a provider key or anything else in the host environment.
// SPORT: internal/plugins process child environment tests (P1-PLG-03).
package plugins

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// fakeHostEnv is a host environment with every kind of name in it.
var fakeHostEnv = map[string]string{
	"PATH": "/usr/bin", "HOME": "/home/fake", "TMPDIR": "/tmp/fake", "LANG": "C",
	"CASCADE_GITHUB_TOKEN": "fake-a", "GITHUB_TOKEN": "fake-b", "GH_TOKEN": "fake-c",
	"CASCADE_PRINCIPAL_TOKEN": "fake-principal", "ANTHROPIC_API_KEY": "fake-provider", "CASCADE_OWNER_CLI": "1", "OTHER": "x",
}

func envNamesOf(env []string) string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestChildEnvAllowlist(t *testing.T) {
	t.Setenv("CASCADE_PRINCIPAL_TOKEN", "fake-principal") // so os.Environ() passed through is caught
	f := newLaunchFix(t)
	f.installMarker(t, cascadeGitHubPluginID)
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	rec, _, _ := LoadMetadata(context.Background(), f.store, cascadeGitHubPluginID)
	getenv := func(k string) string { return fakeHostEnv[k] }
	want := "CASCADE_GITHUB_TOKEN,GH_TOKEN,GITHUB_TOKEN,HOME,LANG,PATH,TMPDIR"
	if got := envNamesOf(childEnv(rec, getenv)); got != want {
		t.Fatalf("childEnv(cascade-github with oauth:repo) names = %s, want %s", got, want)
	}
	for _, kv := range childEnv(rec, getenv) {
		if name, v, _ := strings.Cut(kv, "="); fakeHostEnv[name] != v {
			t.Fatalf("childEnv entry %q does not carry the host value", kv)
		}
	}
	g := newLaunchFix(t)
	g.installMarker(t, cascadeGitHubPluginID)
	g.grant(t, cascadeGitHubPluginID, strings.Replace(githubManifest(t), "\n  \"oauth:repo\",", "", 1))
	noRepo, _, _ := LoadMetadata(context.Background(), g.store, cascadeGitHubPluginID)
	if got := envNamesOf(childEnv(noRepo, getenv)); got != "HOME,LANG,PATH,TMPDIR" {
		t.Fatalf("childEnv(cascade-github without oauth:repo) names = %s, want the base names only", got)
	}
	other := rec
	other.Name = "trustdemo"
	if got := envNamesOf(childEnv(other, getenv)); got != "HOME,LANG,PATH,TMPDIR" {
		t.Fatalf("childEnv(another plugin holding oauth:repo) names = %s, want the base names only", got)
	}
}
