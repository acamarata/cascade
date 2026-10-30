// Purpose: register configured gateways without credential persistence.
// Inputs: account, call-time source and injected dependencies.
// Outputs: catalog metadata and a verified provider record.
// Constraints: credentials stay in memory and requests use the intake gate.
// SPORT: provider.intake.gateway/ADD.

package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// GatewayAddRequest selects a configured gateway and optional pool.
type GatewayAddRequest struct {
	// Name is the provider registry name.
	Name string
	// Account is the configured gateway account.
	Account gateway.Account
	// Source resolves the token at call time.
	Source gateway.Source
	// Pool optionally joins a provider pool.
	Pool string
	// NoVerify records that micro-verification was skipped.
	NoVerify bool
}

// AddGateway obtains a catalog, verifies, and upserts without a vault.
func AddGateway(ctx context.Context, deps Deps, req GatewayAddRequest) (AddResult, error) {
	if err := validateGatewayDeps(deps, req.Account); err != nil {
		return AddResult{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return AddResult{}, errEmptyProviderName()
	}
	prior, found, err := priorRecord(ctx, deps.Registry, req.Name)
	if err != nil {
		return AddResult{}, err
	}
	pool, index, err := membershipFor(ctx, deps.Registry, req.Name, req.Pool, prior, found)
	if err != nil {
		return AddResult{}, err
	}
	models, warnings, err := gatewayCatalogAndVerify(ctx, deps, req)
	if err != nil {
		return AddResult{}, err
	}
	now := deps.Clock.Now()
	status, created := "converged", now
	if found {
		status, created = "updated", prior.CreatedAt
	}
	rec := ProviderRecord{
		Name: req.Name, Driver: DriverAnthropic, BaseURL: req.Account.BaseURL,
		Auth: AuthGateway, AuthRef: VaultKeyRef("gateway:" + req.Account.Name),
		KnownModels: models, CapabilitiesProbedAt: now,
		Pool: pool, PoolIndex: index, VerifySkipped: req.NoVerify,
		CreatedAt: created, UpdatedAt: now,
	}
	if err := deps.Registry.UpsertProvider(ctx, rec); err != nil {
		return AddResult{}, err
	}
	return AddResult{Status: status, Warnings: warnings, Record: rec}, nil
}

func validateGatewayDeps(deps Deps, acct gateway.Account) error {
	if deps.Doer == nil || deps.Clock == nil || deps.Registry == nil || deps.Egress == nil {
		return cascade.New(cascade.KindInvalidInput, "intake: gateway dependencies are incomplete")
	}
	if err := registry.ValidateGatewayAuthRef(registry.VaultKeyRef("gateway:" + acct.Name)); err != nil {
		return err
	}
	if acct.Header != gateway.HeaderBearer && acct.Header != gateway.HeaderXAPIKey {
		return cascade.New(cascade.KindInvalidInput, "intake: invalid gateway header style")
	}
	return nil
}

func gatewayCatalogAndVerify(ctx context.Context, deps Deps, req GatewayAddRequest) ([]string, []string, error) {
	if _, err := acquireIntakeGate(deps.Egress, egress.TierInternal); err != nil {
		return nil, nil, err
	}
	token, err := req.Source.Token(ctx, req.Account)
	if err != nil {
		return nil, nil, err
	}
	defer zeroBytes(token)
	models := append([]string(nil), req.Account.Models...)
	warnings := []string{"catalog: " + string(req.Account.Catalog())}
	if req.Account.Catalog() == gateway.CatalogDiscovered {
		models, err = discoverCatalog(ctx, deps.Doer, req.Account, token)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(models) == 0 {
		return nil, nil, gatewayNoCatalog()
	}
	if req.NoVerify {
		warnings = append(warnings, "micro-verify skipped: --no-verify set")
	} else if err := microVerifyGateway(ctx, deps.Doer, req.Account, token, models[0]); err != nil {
		return nil, nil, err
	}
	return models, warnings, nil
}

func gatewayNoCatalog() error {
	return cascade.New(cascade.KindUnavailable,
		"intake: gateway catalog unavailable; declare models in config or use a gateway with /v1/models")
}

func discoverCatalog(ctx context.Context, doer Doer, acct gateway.Account, token []byte) ([]string, error) {
	req := HTTPRequest{Method: "GET", URL: strings.TrimRight(acct.BaseURL, "/") + "/v1/models", Headers: gatewayHeaders(acct, token)}
	resp, err := doer.Do(ctx, req)
	if err != nil {
		return nil, gatewayTransportError(err, token)
	}
	if resp.Status == 404 || resp.Status == 405 {
		return nil, gatewayNoCatalog()
	}
	if resp.Status != 200 {
		return nil, classifyGatewayStatus(resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(resp.Body, &body) != nil {
		return nil, cascade.New(cascade.KindIntegrity, "intake: invalid gateway models response")
	}
	ids := make([]string, 0, len(body.Data))
	seen := map[string]bool{}
	for _, m := range body.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}

func microVerifyGateway(ctx context.Context, doer Doer, acct gateway.Account, token []byte, model string) error {
	req := HTTPRequest{
		Method: "POST", URL: strings.TrimRight(acct.BaseURL, "/") + "/v1/messages",
		Headers: gatewayHeaders(acct, token), Body: microVerifyBody(DriverAnthropic, model),
	}
	req.Headers["content-type"] = "application/json"
	resp, err := doer.Do(ctx, req)
	if err != nil {
		return gatewayTransportError(err, token)
	}
	if resp.Status == 200 {
		return nil
	}
	return classifyGatewayStatus(resp.Status)
}

func gatewayHeaders(acct gateway.Account, token []byte) map[string]string {
	h := map[string]string{"anthropic-version": "2023-06-01"}
	if acct.Header == gateway.HeaderXAPIKey {
		h["x-api-key"] = string(token)
	} else {
		h["authorization"] = "Bearer " + string(token)
	}
	return h
}

func gatewayTransportError(err error, token []byte) error {
	return cascade.Wrap(cascade.KindUnavailable, redactCredential(err, string(token)), "intake: gateway request failed")
}

func classifyGatewayStatus(code int) error {
	kind := cascade.KindUnavailable
	if code == 401 || code == 403 {
		kind = cascade.KindPermissionDenied
	}
	if code == 429 {
		kind = cascade.KindQuotaExhausted
	}
	return cascade.New(kind, fmt.Sprintf("intake: gateway returned %d", code))
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
