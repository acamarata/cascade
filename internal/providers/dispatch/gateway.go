// Purpose: the gateway branch of Resolver.Resolve (P1-GWY-08). A provider
//
//	record with auth type gateway is resolved against the daemon's
//	ADMITTED gateway accounts, never against providers.db alone: the
//	account must be in the admitted set and the record's base URL must
//	equal the account's, or the call is refused before any request. The
//	driver is the anthropic driver built with the account's base URL and
//	header style, and a credential source that answers `gateway:<account>`
//	through gateway.Source while every other ref keeps the standing-grant
//	vault reader.
//
// Inputs: the admitted accounts and token source (WithGatewayAccounts),
//
//	the lane and provider record Resolve already read.
//
// Outputs: the driver Resolver.build decorates (the WID-11 lane
//
//	outcome decorator), or a taxonomy error.
//
// Constraints: no vault read for a `gateway:` ref; no retry, backoff or
//
//	health write; a token is never cached, logged or placed in an error.
//
// SPORT: internal/providers/dispatch gateway/ADD (P1-GWY-08).

package dispatch

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/anthropic"
	"github.com/acamarata/cascade/providers/transport"
)

// gatewayRefPrefix is the AuthRef prefix of a gateway provider record.
const gatewayRefPrefix = "gateway:"

// ResolverOption configures an optional Resolver collaborator.
type ResolverOption func(*Resolver)

// WithGatewayAccounts gives the Resolver the daemon's admitted gateway
// accounts and the call-time source their tokens come from. The slice is
// copied; a Resolver built without this option admits no gateway account.
func WithGatewayAccounts(accts []gateway.Account, src gateway.Source) ResolverOption {
	admitted := append([]gateway.Account(nil), accts...)
	return func(r *Resolver) {
		r.gateways = admitted
		r.gatewaySrc = src
	}
}

// gatewayCredentials answers `gateway:<account>` through the account's
// call-time source and every other ref through base.
type gatewayCredentials struct {
	base  CredentialSource
	accts []gateway.Account
	src   gateway.Source
}

var _ CredentialSource = gatewayCredentials{}

// Resolve returns the credential for ref. A gateway ref never reaches base
// (the vault); an account outside the admitted set is refused. The token
// bytes the source returns are zeroed once copied into the result.
func (g gatewayCredentials) Resolve(ctx context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, gatewayRefPrefix)
	if !ok {
		if g.base == nil {
			return "", cascade.Newf(cascade.KindUnavailable,
				"dispatch: credential %q requested, no credential source is configured at this composition root", ref)
		}
		return g.base.Resolve(ctx, ref)
	}
	acct, err := gateway.Lookup(g.accts, name)
	if err != nil {
		return "", notAdmitted(name)
	}
	tok, err := g.src.Token(ctx, acct)
	if err != nil {
		clear(tok)
		return "", err
	}
	value := string(tok)
	clear(tok)
	return value, nil
}

// notAdmitted is the refusal for a gateway account outside the admitted set.
func notAdmitted(name string) error {
	return cascade.Newf(cascade.KindPermissionDenied,
		"gateway account %s is not admitted; run `cascade config accept` after reviewing [gateways.%s]", name, name)
}

// gatewayDriver builds the driver for a gateway record, refusing an
// unadmitted account or a base URL mismatch before anything is sent.
// Resolver.build (build.go) wraps it in the lane-outcome decorator.
func (r *Resolver) gatewayDriver(rec registry.ProviderRecord, model string, tr transport.Transport) (provider.ModelProvider, error) {
	acct, err := r.admittedGateway(rec)
	if err != nil {
		return nil, err
	}
	return r.newGatewayDriver(rec, acct, model, tr)
}

// admittedGateway returns the admitted account rec names. The record's
// driver must be anthropic, its ref well-formed, its account admitted and
// its base URL exactly the account's.
func (r *Resolver) admittedGateway(rec registry.ProviderRecord) (gateway.Account, error) {
	if rec.Driver != registry.DriverAnthropic {
		return gateway.Account{}, cascade.Newf(cascade.KindInvalidInput,
			"dispatch: gateway provider %q must use the anthropic driver, not %q", rec.Name, rec.Driver)
	}
	if err := registry.ValidateGatewayAuthRef(rec.AuthRef); err != nil {
		return gateway.Account{}, err
	}
	name := strings.TrimPrefix(rec.AuthRef.String(), gatewayRefPrefix)
	acct, err := gateway.Lookup(r.gateways, name)
	if err != nil {
		return gateway.Account{}, notAdmitted(name)
	}
	if rec.BaseURL != acct.BaseURL {
		return gateway.Account{}, cascade.Newf(cascade.KindConflict,
			"dispatch: provider %q base URL does not match admitted gateway account %s; re-register it with `cascade provider add --gateway %s`",
			rec.Name, name, name)
	}
	return acct, nil
}

// newGatewayDriver builds the anthropic driver for acct, sending through tr.
func (r *Resolver) newGatewayDriver(rec registry.ProviderRecord, acct gateway.Account, model string, tr transport.Transport) (provider.ModelProvider, error) {
	mode, err := gatewayAuthMode(acct)
	if err != nil {
		return nil, err
	}
	return anthropic.New(anthropic.Config{
		BaseURL: acct.BaseURL,
		Doer:    transport.AnthropicDoer{Transport: tr},
		Clock:   r.clock,
		Auth: anthropic.AuthConfig{
			Mode:     mode,
			KeyRef:   rec.AuthRef.String(),
			Resolver: gatewayCredentials{base: r.credentials, accts: r.gateways, src: r.gatewaySrc},
		},
		DefaultModel: model,
	})
}

// gatewayAuthMode maps the account's header style to the driver auth mode.
func gatewayAuthMode(acct gateway.Account) (anthropic.AuthMode, error) {
	switch acct.Header {
	case gateway.HeaderBearer:
		return anthropic.AuthModeBearerKey, nil
	case gateway.HeaderXAPIKey:
		return anthropic.AuthModeKey, nil
	default:
		return 0, cascade.Newf(cascade.KindInvalidInput,
			"dispatch: gateway account %s has unsupported header style %q", acct.Name, acct.Header)
	}
}
