// Purpose: the credential classes a claude Profile carries and the one key
//   variable each class puts beside the config-directory variable.
// Inputs: a Profile (CredentialClass, KeyVar).
// Outputs: the key variable name a class permits, or a typed refusal.
// Constraints: exactly one key variable is ever permitted beside
//   the config-directory variable: CLAUDE_CODE_OAUTH_TOKEN for class oauth_token,
//   ANTHROPIC_API_KEY for class api_key. A key variable outside that pair,
//   or one that does not match the class, refuses before any child exists.
//   SpawnEnv.Tool never carries it, but the harness's own tool subprocesses
//   inherit it (DEF-P2-driver-tool-isolation), so its value is scanned like
//   the canary (keyValueForms) and a straggler holding it fails the run.
// SPORT: providers.agents.claude/ADD (P1-AGT-01, named deviation B-1).

package claude

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CredentialClass distinguishes an api_key lane (USD meter, --bare
// allowed, key in ANTHROPIC_API_KEY) from an oauth_token lane (no --bare,
// token in CLAUDE_CODE_OAUTH_TOKEN), per the J/S-20 registry.
type CredentialClass string

// The two CredentialClass members.
const (
	CredentialClassAPIKey     CredentialClass = "api_key"
	CredentialClassOAuthToken CredentialClass = "oauth_token"
)

// The key variables, one per class. Every use in this repository goes
// through these constants.
const (
	KeyVarAPIKey     = "ANTHROPIC_API_KEY"
	KeyVarOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
)

// keyVarFor returns the key variable the profile's class permits and
// refuses a Profile.KeyVar that is not exactly that variable.
func keyVarFor(prof Profile) (string, error) {
	var want string
	switch prof.CredentialClass {
	case CredentialClassAPIKey:
		want = KeyVarAPIKey
	case CredentialClassOAuthToken:
		want = KeyVarOAuthToken
	default:
		return "", cascade.Newf(cascade.KindInvalidInput,
			"claude: profile credential class %q is not api_key or oauth_token", prof.CredentialClass)
	}
	if prof.KeyVar != want {
		return "", envRejected("profile key variable %q is not %s, the only key variable class %s permits",
			prof.KeyVar, want, prof.CredentialClass)
	}
	return want, nil
}

// minKeyValueLen is the shortest key value worth scanning for: a shorter one
// would match ordinary text and is not a credible token.
const minKeyValueLen = 8

// keyValueForms returns the key variable's value in every canary form
// (exact, hex, base64), so the real secret, like the seeded canary, fails
// the run closed if it reaches output, frames, the journal, an artifact name
// or a file the job wrote. Nil when the environ holds no usable value.
func keyValueForms(env []string, keyVar string) [][]byte {
	for _, kv := range env {
		if name, val, ok := strings.Cut(kv, "="); ok && name == keyVar && len(val) >= minKeyValueLen {
			return canaryForms(val)
		}
	}
	return nil
}
