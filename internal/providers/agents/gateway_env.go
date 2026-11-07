// Purpose: the gateway credential class at the spawn: resolves an admitted
// gateway account to its base URL, token and proxy destination, places the
// three gateway-row values in the environment BuildDriverEnv is building, and
// widens one spawn's egress allowlist by that account's host.
// Inputs: the admitted accounts and the gateway.Source (GatewayDeps), the
// account name from DriverAuth, the binding's gateway row.
// Outputs: a GatewayEnv, the injected key value, the spawn's allowlist.
// Constraints: the variable names come from the Binding row alone; the token
// comes from gateway.Source alone, never from a child variable of the daemon
// environment; the token is held in memory until the spawn step ends and is
// then zeroed, never written to a file, the journal or a log; refusals name the
// account, never the URL or the token; the shared allowlist is never modified.
// SPORT: internal.providers.agents/ADD (P1-GWY-05, contract:gateway-agent-credential).

package agents

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gatewayManagedValue is the fixed value of the row's host-managed flag.
const gatewayManagedValue = "1"

// GatewayDeps is the gateway side of the shim: the admitted accounts
// (gateway.AccountsFrom) and the source that resolves their tokens.
type GatewayDeps struct {
	Accounts []gateway.Account
	Source   gateway.Source
}

// GatewayEnv is one spawn's resolved gateway account. Base URL and destination
// come from the same account, so the child can only be sent where the proxy
// admits. The caller zeroes Token after building the environment.
type GatewayEnv struct {
	BaseURL     string
	Token       []byte
	Destination egressproxy.Destination
}

// ResolveGatewayCredential resolves the named account, which must be in
// d.Accounts: anything else is the KindNotFound of gateway.Lookup.
func ResolveGatewayCredential(ctx context.Context, d GatewayDeps, name string) (GatewayEnv, error) {
	acct, err := gateway.Lookup(d.Accounts, name)
	if err != nil {
		return GatewayEnv{}, err
	}
	dest, err := gatewayDestination(acct.BaseURL)
	if err != nil {
		return GatewayEnv{}, cascade.Newf(cascade.KindInvalidInput, "agents: gateway account %q has no usable proxy destination", name)
	}
	token, err := d.Source.Token(ctx, acct)
	if err != nil {
		return GatewayEnv{}, err
	}
	return GatewayEnv{BaseURL: acct.BaseURL, Token: token, Destination: dest}, nil
}

// gatewayDestination is the host:port a base URL connects to.
func gatewayDestination(baseURL string) (egressproxy.Destination, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", cascade.New(cascade.KindInvalidInput, "agents: the gateway base URL does not parse")
	}
	port := u.Port()
	switch {
	case port != "":
	case u.Scheme == "https":
		port = "443"
	case u.Scheme == "http":
		port = "80"
	default:
		return "", cascade.New(cascade.KindInvalidInput, "agents: the gateway base URL has no https or http scheme")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return egressproxy.ParseDestination(host + ":" + port)
}

// credentialValues places the class's values in values, from their own named
// sources, and returns the key value for the scrub. Gateway values come from
// gw alone; every other class refuses a gw and reads the daemon environment.
func credentialValues(b Binding, a DriverAuth, spec CredentialSpec, getenv func(string) string,
	gw []GatewayEnv, values map[string]string) (string, error) {
	if a.Class == CredentialGateway {
		if len(gw) != 1 {
			return "", cascade.Newf(cascade.KindInvalidInput, "agents: %s: class gateway needs exactly one resolved account", b.Harness)
		}
		return gatewayValues(b, spec, gw[0], values)
	}
	if len(gw) != 0 {
		return "", cascade.Newf(cascade.KindInvalidInput, "agents: %s: class %s takes no gateway account", b.Harness, a.Class)
	}
	if spec.KeyVar == "" {
		return "", nil
	}
	key := getenv(a.KeyEnv)
	if key == "" {
		return "", cascade.Newf(cascade.KindUnavailable, "agents: %s: the daemon environment variable %s is not set", b.Harness, a.KeyEnv)
	}
	values[spec.KeyVar] = key
	return key, nil
}

// gatewayValues checks one resolved account and places the row's three values.
func gatewayValues(b Binding, spec CredentialSpec, gw GatewayEnv, values map[string]string) (string, error) {
	if len(gw.Token) == 0 {
		return "", cascade.Newf(cascade.KindUnavailable, "agents: %s: the gateway account resolved an empty value", b.Harness)
	}
	if dest, err := gatewayDestination(gw.BaseURL); err != nil || dest != gw.Destination {
		return "", cascade.Newf(cascade.KindInvalidInput, "agents: %s: the gateway base URL does not match the proxy destination", b.Harness)
	}
	key := string(gw.Token)
	values[spec.KeyVar] = key
	values[spec.BaseURLVar] = gw.BaseURL
	values[spec.ManagedVar] = gatewayManagedValue
	return key, nil
}

// resolveGateway resolves a gateway class's account for this spawn.
func (r *spawnRun) resolveGateway(ctx context.Context) error {
	if r.auth.Class != CredentialGateway {
		return nil
	}
	deps := r.s.d.Gateway
	if deps.Source.Getenv == nil {
		deps.Source.Getenv = r.s.d.Getenv
	}
	gw, err := ResolveGatewayCredential(ctx, deps, r.auth.Gateway)
	if err != nil {
		return err
	}
	r.gw = []GatewayEnv{gw}
	return nil
}

// forgetGatewayToken zeroes the resolved token once the spawn is past it.
func (r *spawnRun) forgetGatewayToken() {
	for _, gw := range r.gw {
		clear(gw.Token)
	}
}

// startProxy starts the per-spawn egress proxy for this driver. A gateway
// spawn's list is a new slice: the driver's list plus the account's host, for
// this spawn alone.
func (r *spawnRun) startProxy(ctx context.Context) error {
	id := egressproxy.DriverID(r.b.Harness)
	allow := r.s.d.EgressAllow[id]
	for _, gw := range r.gw {
		allow = slices.Concat(allow, egressproxy.DestinationAllowlist{gw.Destination})
	}
	var p *egressproxy.Proxy
	var err error
	if r.s.dial == nil {
		p, err = StartSpawnProxy(ctx, id, string(r.job.JobID), allow, r.s.d.ProxyJournal)
	} else {
		p, err = startSpawnProxy(ctx, id, string(r.job.JobID), allow, r.s.d.ProxyJournal, r.s.dial)
	}
	if err != nil {
		return err
	}
	r.proxy = p
	return nil
}
