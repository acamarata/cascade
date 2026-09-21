// Purpose (this file): resolve this plugin process's own GitHub API token
//
//	when no interactive OAuth flow has set one — the fallback main.go's
//	client() reaches for (D2, confirming review finding 2).
//
// Inputs: an injectable getenv (os.Getenv in production; client() never
//
//	receives a per-call getenv override — this plugin has no
//	internal/runtime.Getenv abstraction to inject, Art.10.2 forbidding
//	plugins/** from importing internal/**, so a raw os.Getenv here matches
//	this SAME process's own existing precedent: auth_flow.go's
//	auth.RequireInteractive(os.Getenv) and plugins/github/wiki/confirm.go's
//	defaultGetenv both already call os.Getenv directly for the identical
//	reason).
//
// Outputs: the first non-empty value among githubTokenEnvNames, or "".
//
// Constraints: this is the SAME variable list, in the SAME precedence
//
//	order, internal/ci/waitmerge_deps.go's waitTokenEnvNames already
//	established for wait-on-green's own GitHub token resolution
//	(P1-E25-W5-S51-T3) — reusing rather than inventing a second convention
//	is what makes internal/repo/templates/wiki-sync-workflow.yml's
//	`CASCADE_GITHUB_TOKEN` env var actually reach this process's client()
//	(CR finding 2, P1-E25-W5-S51-T6): a CI-launched process never calls
//	cascade.auth.complete, so p.token is always empty there, and without
//	this fallback every real auto-sync run's clone/push would carry an
//	unauthenticated URL and fail.
//
// SPORT: plugins/github:token-env (ADD) — P1-E25-W5-S51-T6.
package main

// githubTokenEnvNames are the environment variables this process falls
// back to for its own API token, in precedence order.
var githubTokenEnvNames = []string{"CASCADE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"}

// tokenFromEnv resolves the first non-empty value among
// githubTokenEnvNames, or "" if none is set.
func tokenFromEnv(getenv func(string) string) string {
	for _, name := range githubTokenEnvNames {
		if v := getenv(name); v != "" {
			return v
		}
	}
	return ""
}
