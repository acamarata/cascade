// Package plugins (process_env.go): Purpose: the closed environment a
// trusted process plugin is started with, and the net scopes its consent
// warning names.
// Inputs: the plugin's stored record and the launch environment's reader.
// Outputs: KEY=VALUE entries for process.Manifest.Env; host names.
// Constraints: an allowlist, never a denylist: PATH, HOME, TMPDIR, LANG,
// LC_ALL and TZ when set, plus the three GitHub token names for
// cascade-github only when EffectiveGrants holds "oauth:repo". Nothing else
// from the host environment (CASCADE_PRINCIPAL_TOKEN, the owner-CLI marker,
// provider keys) ever reaches a child.
// SPORT: internal/plugins process child environment (P1-PLG-03).
package plugins

import (
	"slices"
	"strings"
)

// childEnvBase are the names every trusted child may inherit.
var childEnvBase = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ"}

// githubTokenEnv are the token names cascade-github reads, passed only when
// its record's effective grants hold oauth:repo.
var githubTokenEnv = []string{"CASCADE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"}

// oauthRepoGrant is the grant that admits the GitHub token names.
const oauthRepoGrant = "oauth:repo"

// childEnv returns rec's closed child environment, read through getenv. A
// name getenv answers empty is left out.
func childEnv(rec PluginMetadata, getenv func(string) string) []string {
	names := append([]string(nil), childEnvBase...)
	if rec.Name == cascadeGitHubPluginID && slices.Contains(EffectiveGrants(rec), oauthRepoGrant) {
		names = append(names, githubTokenEnv...)
	}
	env := make([]string, 0, len(names))
	for _, name := range names {
		if v := getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// netScopes returns the hosts rec's effective "net.http:<host>" grants name,
// for the consent warning Launch prints.
func netScopes(rec PluginMetadata) []string {
	var scopes []string
	for _, g := range EffectiveGrants(rec) {
		if host, ok := strings.CutPrefix(g, "net.http:"); ok && host != "" {
			scopes = append(scopes, host)
		}
	}
	return scopes
}
