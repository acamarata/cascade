// Purpose: the provider health probers, shared by the one-shot CLI
//
//	(provider_health_cmd.go) and the daemon's health scheduler
//	(daemon_unix_provider_health.go): the reachability prober every
//	non-gateway provider keeps, the authenticated gateway prober, the
//	prober that routes between them, and the egress gate they run behind.
//
// Inputs: a provider record; for a gateway record the ADMITTED gateway
//
//	accounts and a gateway.Source the caller injects (this file never calls
//	os.Getenv; the daemon hands in its credential snapshot source).
//
// Outputs: a health.ProbeResult; Success only when the probe proved the
//
//	provider usable.
//
// Constraints: a gateway record is never judged by reachability, because
//
//	an unauthenticated GET of its base URL answers below 500 while the
//	gateway still fails real calls. Its recovery is one authenticated
//	max_tokens-1 request through the admitted account, and only a 2xx is
//	healthy. An unadmitted account, a base URL that differs from the
//	admitted one, a missing token or model are all "not healthy" with a
//	content-free reason. A token is never logged or placed in a result.
//
// SPORT: cmd/cascade provider_health_probe/ADD (P1-GWY-09).

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/health"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// providerDoctorTimeout bounds every doctor-driven reachability probe.
const providerDoctorTimeout = 10 * time.Second

// httpDoer is httpReachabilityProber's transport seam, deliberately typed
// without net/http in its signature: providerDeps.HealthHTTPDoer lets a
// test substitute a fake with no net/http import at all, so
// TestProviderTest_* stays outside the no-network-unit-lane gate (a
// _test.go file importing net/http is flagged even when, as here, it
// never opens a real socket -- internal/build's gate is import-based).
// realHTTPDoer (below) is the only implementation that imports net/http;
// it is never referenced from a _test.go file.
type httpDoer interface {
	// Get performs a GET against url and returns its status code, or an
	// error if the request could not be sent/completed at all.
	Get(ctx context.Context, url string) (status int, err error)
}

// realHTTPDoer is the production httpDoer: a real outbound GET through
// *http.Client. This is the one place the reachability probe touches
// net/http.
type realHTTPDoer struct{ client *http.Client }

func (d realHTTPDoer) Get(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

type httpReachabilityProber struct {
	doer httpDoer
}

// Probe implements health.Prober. This is a REAL, network-capable probe
// scoped to reachability rather than a full per-vendor authenticated
// 1-token verify: it issues a GET to the provider's BaseURL and treats
// any response with status < 500 as reachable. A gateway record is never
// routed here (routedProber).
func (p httpReachabilityProber) Probe(ctx context.Context, rec registry.ProviderRecord) (health.ProbeResult, error) {
	if rec.BaseURL == "" {
		return health.ProbeResult{Success: false, TransportErr: "no base_url configured"}, nil
	}
	status, err := p.doer.Get(ctx, rec.BaseURL)
	if err != nil {
		return health.ProbeResult{Endpoint: rec.BaseURL, Success: false, TransportErr: err.Error()}, nil
	}
	return health.ProbeResult{Endpoint: rec.BaseURL, Status: status, Success: status < 500}, nil
}

// gatewayProbeConfig is what the gateway prober needs from its caller: the
// admitted accounts and the token source. The zero value admits none.
type gatewayProbeConfig struct {
	Accounts []gateway.Account
	Source   gateway.Source
}

// gatewayProber is the authenticated micro-verify for gateway records.
type gatewayProber struct {
	doer intake.Doer
	cfg  gatewayProbeConfig
}

var _ health.Prober = gatewayProber{}

// Probe sends one max_tokens-1 request to the admitted account and reports
// Success only for a 2xx. Every refusal before the request is a typed
// "not healthy" result, never an error that could be read as a transport
// fault worth retrying differently.
func (p gatewayProber) Probe(ctx context.Context, rec registry.ProviderRecord) (health.ProbeResult, error) {
	acct, err := gateway.Lookup(p.cfg.Accounts, strings.TrimPrefix(rec.AuthRef.String(), "gateway:"))
	if err != nil || acct.BaseURL != rec.BaseURL {
		return notHealthy(rec.BaseURL, "gateway account not admitted for this provider"), nil
	}
	model := firstOf(rec.KnownModels, acct.Models)
	if model == "" {
		return notHealthy(acct.BaseURL, "no model to verify with"), nil
	}
	token, terr := p.cfg.Source.Token(ctx, acct)
	if terr != nil || len(token) == 0 {
		clear(token)
		return notHealthy(acct.BaseURL, "gateway credential unavailable"), nil
	}
	defer clear(token)
	url := strings.TrimRight(acct.BaseURL, "/") + "/v1/messages"
	resp, derr := p.doer.Do(ctx, intake.HTTPRequest{Method: http.MethodPost, URL: url,
		Headers: gatewayProbeHeaders(acct, token), Body: gatewayProbeBody(model)})
	if derr != nil {
		return notHealthy(url, "gateway request failed: "+strings.ReplaceAll(derr.Error(), string(token), "[redacted]")), nil
	}
	return health.ProbeResult{Endpoint: url, Status: resp.Status, Success: resp.Status >= 200 && resp.Status < 300}, nil
}

// notHealthy is the content-free failed result.
func notHealthy(endpoint, reason string) health.ProbeResult {
	return health.ProbeResult{Endpoint: endpoint, TransportErr: reason}
}

// firstOf returns the first non-empty model of the given lists.
func firstOf(lists ...[]string) string {
	for _, l := range lists {
		for _, m := range l {
			if strings.TrimSpace(m) != "" {
				return m
			}
		}
	}
	return ""
}

// gatewayProbeHeaders is the account's header style, as intake sends it.
func gatewayProbeHeaders(acct gateway.Account, token []byte) map[string]string {
	h := map[string]string{"anthropic-version": "2023-06-01", "content-type": "application/json"}
	if acct.Header == gateway.HeaderXAPIKey {
		h["x-api-key"] = string(token)
	} else {
		h["authorization"] = "Bearer " + string(token)
	}
	return h
}

// gatewayProbeBody is the smallest valid request: one token, one word.
func gatewayProbeBody(model string) []byte {
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "ping"}}})
	return body
}

// routedProber sends gateway records to the authenticated prober and every
// other record to the reachability prober.
type routedProber struct {
	reach   httpReachabilityProber
	gateway gatewayProber
}

var _ health.Prober = routedProber{}

// Probe implements health.Prober.
func (p routedProber) Probe(ctx context.Context, rec registry.ProviderRecord) (health.ProbeResult, error) {
	if rec.Auth == registry.AuthGateway {
		return p.gateway.Probe(ctx, rec)
	}
	return p.reach.Probe(ctx, rec)
}

// newRoutedProber builds the prober from deps' test seams (HealthHTTPDoer,
// Doer) or the real clients, and cfg for gateway records.
func newRoutedProber(deps providerDeps, cfg gatewayProbeConfig) routedProber {
	get := deps.HealthHTTPDoer
	if get == nil {
		get = realHTTPDoer{client: &http.Client{Timeout: providerDoctorTimeout}}
	}
	post := deps.Doer
	if post == nil {
		post = intake.NewHTTPDoer(providerDoctorTimeout)
	}
	return routedProber{reach: httpReachabilityProber{doer: get}, gateway: gatewayProber{doer: post, cfg: cfg}}
}

// egressEngineForHealth returns the process firewall's engine, the one
// `provider add` writes through, gating the probers behind the same egress
// class. On failure it returns a gate that refuses every class with the
// firewall's cause (Kind and message, no path), never a typed nil
// *egress.Engine, so the probe fails closed and says why.
func egressEngineForHealth() health.Egress {
	engine, err := egress.ProcessEngine(egress.DefaultRegistry())
	if err != nil {
		return firewallUnavailable{cause: firewallCause(err)}
	}
	if engine == nil {
		return firewallUnavailable{cause: cascade.New(cascade.KindInternal, "egress firewall unavailable: no engine")}
	}
	return engine
}
