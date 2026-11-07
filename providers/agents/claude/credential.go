// Purpose: the credential classes a claude Profile carries and the one key
//   variable each class puts beside the config-directory variable.
// Inputs: a Profile (CredentialClass, KeyVar).
// Outputs: the key variable name a class permits, or a typed refusal.
// Constraints: exactly one key variable is ever permitted beside
//   the config-directory variable: CLAUDE_CODE_OAUTH_TOKEN for class oauth_token,
//   ANTHROPIC_API_KEY for class api_key, ANTHROPIC_AUTH_TOKEN for class
//   gateway (plus its base-URL and host-managed variables). A key variable outside that pair,
//   or one that does not match the class, refuses before any child exists.
//   SpawnEnv.Tool never carries it, but the harness's own tool subprocesses
//   inherit it (DEF-P2-driver-tool-isolation), so its value is scanned like
//   the canary (keyValueForms) and a straggler holding it fails the run.
// SPORT: providers.agents.claude/ADD (P1-AGT-01, named deviation B-1).

package claude

import (
	"net/netip"
	"net/url"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CredentialClass distinguishes an api_key lane (USD meter, --bare
// allowed, key in ANTHROPIC_API_KEY) from an oauth_token lane (no --bare,
// token in CLAUDE_CODE_OAUTH_TOKEN), per the J/S-20 registry.
type CredentialClass string

// The CredentialClass members. A gateway lane routes the CLI to one
// admitted gateway account: bearer token, base URL and host-managed flag.
const (
	CredentialClassAPIKey     CredentialClass = "api_key"
	CredentialClassOAuthToken CredentialClass = "oauth_token"
	CredentialClassGateway    CredentialClass = "gateway"
)

// The key variables, one per class. Every use in this repository goes
// through these constants.
const (
	KeyVarAPIKey     = "ANTHROPIC_API_KEY"
	KeyVarOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
	// KeyVarGatewayToken is the bearer credential of a gateway lane.
	KeyVarGatewayToken = "ANTHROPIC_AUTH_TOKEN"
)

// The gateway lane's two extra variables and the managed flag's one value.
// They are admitted only for class gateway.
const (
	GatewayBaseURLVar   = "ANTHROPIC_BASE_URL"
	GatewayManagedVar   = "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"
	GatewayManagedValue = "1"
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
	case CredentialClassGateway:
		want = KeyVarGatewayToken
	default:
		return "", cascade.Newf(cascade.KindInvalidInput,
			"claude: profile credential class %q is not api_key or oauth_token, nor gateway", prof.CredentialClass)
	}
	if prof.KeyVar != want {
		return "", envRejected("profile key variable %q is not %s, the only key variable class %s permits",
			prof.KeyVar, want, prof.CredentialClass)
	}
	return want, nil
}

// driverAllowlist is the Driver environment allowlist for the profile: the
// shared base, the config-directory variable and the key variable, plus the
// two gateway variables for class gateway only.
func driverAllowlist(prof Profile, keyVar string) []string {
	out := provider.DriverEnvAllowlist(prof.VendorAuthVar, keyVar)
	if prof.CredentialClass == CredentialClassGateway {
		out = append(out, GatewayBaseURLVar, GatewayManagedVar)
	}
	return out
}

// checkGatewayVars refuses a gateway lane whose base URL is empty, not
// https (http only on a loopback host) or whose managed flag is not
// GatewayManagedValue. Other classes pass: parsePairs already refused the
// gateway variables for them.
func checkGatewayVars(prof Profile, values map[string]string) error {
	if prof.CredentialClass != CredentialClassGateway {
		return nil
	}
	if !gatewayURLAllowed(values[GatewayBaseURLVar]) {
		return envRejected("gateway lane: %s must be an https URL (http only on a loopback host)", GatewayBaseURLVar)
	}
	if values[GatewayManagedVar] != GatewayManagedValue {
		return envRejected("gateway lane: %s must be %q", GatewayManagedVar, GatewayManagedValue)
	}
	return nil
}

// gatewayURLAllowed reports whether raw is an https URL with a host, or an
// http URL whose host is localhost or a loopback IP.
func gatewayURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		// netip is a pure parser (no socket surface), unlike net.ParseIP's
		// package. A zoned address is refused, as net.ParseIP refused it.
		ip, err := netip.ParseAddr(host)
		return err == nil && ip.Zone() == "" && ip.Unmap().IsLoopback()
	}
	return false
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
