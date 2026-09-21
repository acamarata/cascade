// Purpose: the local CI gate's environment ALLOWLIST (P1-E25-W5-S51-T5).
// A step is an operator-configured command running with the cascade
// process's privileges; handing it os.Environ() wholesale would hand it
// every credential the ambient shell happens to carry -- GITHUB_TOKEN,
// AWS_*, every CASCADE_* setting including ones that redirect cascade's
// own storage. So the environment a step receives is BUILT, never
// inherited: a fixed list of variables a build genuinely needs, plus the
// names the operator themselves listed under [ci.local] env.
//
// Inputs: the ambient environment as "K=V" strings (injected -- never read
// from os.Environ inside a code path a test exercises) and the extra
// variable NAMES [ci.local] env declares.
// Outputs: the filtered "K=V" slice a step runs with, always carrying
// CI=true.
// Constraints: the allowlist is a closed set plus operator-named keys.
// Anything not on it does not reach a step, and there is no "pass
// everything" switch to set.
// SPORT: internal.ci.AllowedEnv/ADDED (P1-E25-W5-S51-T5).

package ci

import "strings"

// envAllowlist is the fixed set of variables every step receives when the
// ambient environment defines them.
//
// Each earns its place: PATH locates the toolchain at all; HOME and TMPDIR
// are where build tools put caches and scratch files (a Go build fails
// outright without a writable HOME); LANG/LC_* keep a compiler's
// diagnostics and any collation deterministic; the GO* four are the Go
// toolchain's own cache and module locations, whose absence turns every
// step into a cold rebuild.
var envAllowlist = map[string]bool{
	"PATH":       true,
	"HOME":       true,
	"TMPDIR":     true,
	"LANG":       true,
	"GOPATH":     true,
	"GOFLAGS":    true,
	"GOCACHE":    true,
	"GOMODCACHE": true,
}

// envAllowedPrefixes are variable-name prefixes admitted wholesale.
// "LC_" is the locale family (LC_ALL, LC_CTYPE, ...), which behaves as one
// setting and is meaningless split up.
var envAllowedPrefixes = []string{"LC_"}

// ciEnvMarker is set on every step: build tools branch on it to disable
// interactive prompts and colour, which is what 06-FORGE-SPEC §5.8's
// "fully non-interactive" requirement needs from the step's side.
const ciEnvMarker = "CI=true"

// AllowedEnv builds the environment one step runs with: every allowlisted
// variable present in environ, every variable whose NAME the operator
// listed in extraKeys, and CI=true.
//
// An operator-named key that is not set in environ is simply absent -- the
// key names a pass-through, not a value this function invents. CI is
// appended last so an ambient CI=0 cannot turn it off.
func AllowedEnv(environ []string, extraKeys []string) []string {
	extra := make(map[string]bool, len(extraKeys))
	for _, k := range extraKeys {
		if name := strings.TrimSpace(k); name != "" {
			extra[name] = true
		}
	}
	out := make([]string, 0, len(envAllowlist)+len(extra)+1)
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "CI" {
			continue
		}
		if envAllowed(name) || extra[name] {
			out = append(out, kv)
		}
	}
	return append(out, ciEnvMarker)
}

// envAllowed reports whether name is on the fixed allowlist, by exact name
// or by an admitted prefix.
func envAllowed(name string) bool {
	if envAllowlist[name] {
		return true
	}
	for _, prefix := range envAllowedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
