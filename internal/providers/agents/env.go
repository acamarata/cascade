// Purpose: builds the exact environments of one driver child and of the
// untrusted tools it spawns, and lists every secret value placed in them so
// the value-based output scrub can mask each one.
// Inputs: the binding, the validated DriverAuth, the runtime profile, the job
// id, the principal token, the lease marker, the per-spawn proxy's Env()
// and the daemon's getenv.
// Outputs: BuiltEnv{Driver, Tool, Secrets}.
// Constraints: the keys are exactly provider.DriverEnvAllowlist(configDirVar,
// keyVar); a value comes only from its own named source, so the daemon
// environment is consulted for PATH, LANG and the one key variable alone;
// every proxy variable (both cases of HTTPS_PROXY, HTTP_PROXY, NO_PROXY)
// comes only from proxyEnv, empty values kept, and ALL_PROXY is never
// present; CASCADE_SOCKET is allowlisted but not set here (this builder has no
// socket input and the contract signature is fixed): the spawn shim appends it;
// a value holding a newline, carriage return or NUL is refused
// (naming the variable, never the value); the key value is read from the
// daemon environment only, never the vault (E.1).
// SPORT: internal.providers.agents/ADD (P1-AGT-15, contract:driver-credentials).

package agents

import (
	"net/url"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// BuiltEnv is one spawn's environments. Secrets holds every injected secret
// value (the key, the principal token, the proxy credential) for the scrub.
type BuiltEnv struct {
	Driver  []string
	Tool    []string
	Secrets [][]byte
}

// proxyKeys are the six variables that come from the per-spawn proxy alone.
var proxyKeys = []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "NO_PROXY", "no_proxy"}

// BuildDriverEnv builds the driver and tool environments for one spawn.
func BuildDriverEnv(b Binding, a DriverAuth, p RuntimeProfile, jobID, principalToken, leaseMarker string,
	proxyEnv []string, getenv func(string) string) (BuiltEnv, error) {
	keyVar, err := KeyVarFor(b, a.Class)
	if err != nil {
		return BuiltEnv{}, err
	}
	if err := checkChildVars(b, keyVar); err != nil {
		return BuiltEnv{}, err
	}
	if jobID == "" || principalToken == "" || leaseMarker == "" {
		return BuiltEnv{}, cascade.New(cascade.KindInvalidInput, "agents: the job id, principal token and lease marker are all required")
	}
	proxies, err := proxyValues(proxyEnv)
	if err != nil {
		return BuiltEnv{}, err
	}
	values := map[string]string{
		"PATH": getenv("PATH"), "LANG": getenv("LANG"),
		"HOME": p.Home, "TMPDIR": p.Tmp, b.ConfigDirVar: p.ConfigDir,
		"CASCADE_JOB_ID": jobID, "CASCADE_LEASE_ID": leaseMarker,
	}
	for k, v := range proxies {
		values[k] = v
	}
	var key string
	if keyVar != "" {
		if key = getenv(a.KeyEnv); key == "" {
			return BuiltEnv{}, cascade.Newf(cascade.KindUnavailable, "agents: %s: the daemon environment variable %s is not set", b.Harness, a.KeyEnv)
		}
		values[keyVar] = key
	}
	env, err := orderedEnv(provider.DriverEnvAllowlist(b.ConfigDirVar, keyVar), values)
	if err != nil {
		return BuiltEnv{}, err
	}
	if strings.ContainsAny(principalToken, "\n\r\x00") {
		return BuiltEnv{}, cascade.New(cascade.KindInvalidInput, "agents: the principal token holds a newline or NUL")
	}
	env = principal.BindEnv(env, principalToken)
	return BuiltEnv{Driver: env, Tool: BuildToolEnv(env, b, keyVar), Secrets: secretValues(key, principalToken, proxies)}, nil
}

// checkChildVars refuses a binding whose config-directory or key variable
// collides with a base allowlist name or with each other: the later value
// would silently replace the first and the key would be emitted twice.
func checkChildVars(b Binding, keyVar string) error {
	base := provider.DriverEnvAllowlist()
	for _, v := range []string{b.ConfigDirVar, keyVar} {
		if v != "" && slices.Contains(base, v) {
			return cascade.Newf(cascade.KindInvalidInput, "agents: %s: variable %s is already a base environment name", b.Harness, v)
		}
	}
	if keyVar != "" && keyVar == b.ConfigDirVar {
		return cascade.Newf(cascade.KindInvalidInput, "agents: %s: the key variable and the config directory variable are the same name", b.Harness)
	}
	return nil
}

// orderedEnv emits KEY=VALUE for each allowlisted key that has a value, in
// allowlist order. A key outside the allowlist is never emitted. Only the six
// proxy variables may be empty; any other empty value is left out.
func orderedEnv(allow []string, values map[string]string) ([]string, error) {
	var env []string
	for _, k := range allow {
		v, ok := values[k]
		if !ok || (v == "" && !slices.Contains(proxyKeys, k)) {
			continue
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return nil, cascade.Newf(cascade.KindInvalidInput, "agents: the value for %s holds a newline or NUL", k)
		}
		env = append(env, k+"="+v)
	}
	return env, nil
}

// BuildToolEnv is the driver environment minus the key variable and the
// vendor config-directory variable: the tools a harness spawns are untrusted
// and never see the credential or where it lives. The input is not modified.
func BuildToolEnv(driver []string, b Binding, keyVar string) []string {
	out := make([]string, 0, len(driver))
	for _, kv := range driver {
		name, _, _ := strings.Cut(kv, "=")
		if name == b.ConfigDirVar || (keyVar != "" && name == keyVar) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// proxyValues parses the per-spawn proxy's Env(): exactly the six proxy
// variables, each once. Anything else (ALL_PROXY included) or a missing one
// is refused, so a child can never be left without the per-spawn proxy.
func proxyValues(proxyEnv []string) (map[string]string, error) {
	out := make(map[string]string, len(proxyKeys))
	for _, kv := range proxyEnv {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !slices.Contains(proxyKeys, name) {
			return nil, cascade.New(cascade.KindInvalidInput, "agents: the proxy environment holds an entry that is not one of the six proxy variables")
		}
		if _, dup := out[name]; dup {
			return nil, cascade.Newf(cascade.KindInvalidInput, "agents: the proxy environment sets %s twice", name)
		}
		if err := checkProxyURL(name, val); err != nil {
			return nil, err
		}
		out[name] = val
	}
	for _, name := range proxyKeys {
		if _, ok := out[name]; !ok {
			return nil, cascade.Newf(cascade.KindInvalidInput, "agents: the proxy environment lacks %s", name)
		}
	}
	return out, nil
}

// checkProxyURL refuses a non-empty URL variable whose value is not a URL with
// a scheme and host: the scrub reads the credential out of the parsed URL, so
// an unreadable shape would reach the child unmasked. NO_PROXY is a list, not a URL.
func checkProxyURL(name, val string) error {
	if val == "" || strings.EqualFold(name, "NO_PROXY") {
		return nil
	}
	if u, err := url.Parse(val); err != nil || u.Scheme == "" || u.Host == "" {
		return cascade.Newf(cascade.KindInvalidInput, "agents: the proxy environment value for %s is not a URL with a scheme and host", name)
	}
	return nil
}

// secretValues lists the key, the principal token and the proxy URL's
// credential (decoded and as written), each once, empties dropped.
func secretValues(key, token string, proxies map[string]string) [][]byte {
	var out [][]byte
	add := func(s string) {
		if s != "" && !slices.ContainsFunc(out, func(o []byte) bool { return string(o) == s }) {
			out = append(out, []byte(s))
		}
	}
	add(key)
	add(token)
	for _, name := range proxyKeys { // every entry, so a differing credential in any of them is masked too
		u, err := url.Parse(proxies[name])
		if err != nil || u.User == nil {
			continue
		}
		pw, has := u.User.Password()
		add(pw)
		if !has {
			add(u.User.Username()) // a username-only URL carries a token
		}
		if _, enc, ok := strings.Cut(u.User.String(), ":"); ok {
			add(enc)
		}
		add(spelledCredential(proxies[name]))
	}
	return out
}

// spelledCredential is the secret part of a proxy URL's userinfo exactly as
// written, before any percent-decoding: the password, or the whole userinfo
// when there is no colon (a token). An echo of the URL as given is masked too.
func spelledCredential(raw string) string {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return ""
	}
	if _, pw, found := strings.Cut(rest[:at], ":"); found {
		return pw
	}
	return rest[:at]
}
