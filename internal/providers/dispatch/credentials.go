// Purpose: the production CredentialSource — how the daemon turns a
//
//	provider record's vault-key REFERENCE into the credential the driver
//	needs, under a standing grant (R-14.243).
//
// Inputs: a vault broker and the grant register the operator issued into.
// Outputs: Resolve(ctx, ref) (string, error).
// Constraints: this type never prompts, never defaults, and never invents
//
//	a value. With no live grant it returns the broker's own typed refusal
//	naming the key — the same fail-closed outcome a nil CredentialSource
//	produced before this existed, but at the true credential boundary and
//	per request, rather than at construction.
//
// SPORT: internal/providers/dispatch credentials/ADD — P1-E10-W4-S87-T1.
package dispatch

import (
	"context"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// GrantedCredentials resolves references through the vault under a grant.
type GrantedCredentials struct {
	broker *secrets.Broker
	grants *secrets.Grants
}

// NewGrantedCredentials builds the source.
//
// Both arguments are required. A source with no broker could only ever
// fail, and one with no grant register would have to either refuse
// everything (in which case it should not exist) or read without
// authorisation (in which case it is the exemption R-14.243 rejected).
func NewGrantedCredentials(broker *secrets.Broker, grants *secrets.Grants) (*GrantedCredentials, error) {
	switch {
	case broker == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"dispatch: a credential source needs a vault broker")
	case grants == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"dispatch: a credential source needs a grant register")
	}
	return &GrantedCredentials{broker: broker, grants: grants}, nil
}

var _ CredentialSource = (*GrantedCredentials)(nil)

// Resolve returns the value stored under ref.
//
// The returned string is the credential. It is never logged, never placed
// in an error message, and never returned alongside one: every error path
// here returns the empty string, so a caller that ignores the error still
// gets nothing usable rather than a partially-populated secret.
func (c *GrantedCredentials) Resolve(ctx context.Context, ref string) (string, error) {
	value, err := c.broker.GetGranted(ctx, ref, c.grants)
	if err != nil {
		return "", err
	}
	return string(value), nil
}
