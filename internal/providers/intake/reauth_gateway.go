// Purpose: re-verify gateway credentials through their configured source.
// Inputs: an existing record, account and injected dependencies.
// Outputs: a verified record, written only after success.
// Constraints: no vault operations or intake lane state writes.
// SPORT: provider.intake.gateway.reauth/ADD.

package intake

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ReauthGateway re-runs micro-verify without accepting a credential override.
func ReauthGateway(ctx context.Context, deps Deps, rec ProviderRecord, acct gateway.Account, src gateway.Source) (AddResult, error) {
	if err := validateGatewayDeps(deps, acct); err != nil {
		return AddResult{}, err
	}
	if rec.Auth != AuthGateway || rec.Driver != DriverAnthropic || rec.AuthRef != VaultKeyRef("gateway:"+acct.Name) {
		return AddResult{}, cascade.New(cascade.KindInvalidInput, "intake: gateway record does not match configured account")
	}
	if len(rec.KnownModels) == 0 {
		return AddResult{}, gatewayNoCatalog()
	}
	if rec.BaseURL != acct.BaseURL {
		return AddResult{}, cascade.New(cascade.KindInvalidInput, "intake: gateway endpoint changed; run provider add --gateway again")
	}
	if _, err := acquireIntakeGate(deps.Egress, egress.TierInternal); err != nil {
		return AddResult{}, err
	}
	token, err := src.Token(ctx, acct)
	if err != nil {
		return AddResult{}, err
	}
	defer zeroBytes(token)
	if err := microVerifyGateway(ctx, deps.Doer, acct, token, rec.KnownModels[0]); err != nil {
		return AddResult{}, err
	}
	rec.VerifySkipped = false
	rec.UpdatedAt = deps.Clock.Now()
	if err := deps.Registry.UpsertProvider(ctx, rec); err != nil {
		return AddResult{}, err
	}
	return AddResult{Status: reauthStatus, Record: rec}, nil
}
