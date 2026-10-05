// Purpose: classify signing custody by source, never its storage label.
// Inputs: selected keystore. Outputs: signer or refusal.
// Constraints: release builds require protected custody. SPORT: elevation custody.

package elevation

import (
	"errors"
	"fmt"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CustodyTier identifies the authority that produced a key.
type CustodyTier string

// CustodyPlatform and the other tiers name the registered source authority.
const (
	CustodyPlatform CustodyTier = "platform"
	CustodyPresence CustodyTier = "presence"
	CustodyFile     CustodyTier = "file"
	CustodyNone     CustodyTier = "none"
)

// SatisfiesElevation applies the shared custody policy.
func (t CustodyTier) SatisfiesElevation() bool {
	return t == CustodyPlatform || t == CustodyPresence || (t == CustodyFile && devkeysBuild)
}

// CustodySource labels the authority that supplies a keystore.
type CustodySource struct {
	Tier CustodyTier
	Name string
	Open func(dataDir string) (ElevationKeystore, bool)
}

// Custody retains source classification independently of storage metadata.
type Custody struct {
	tier    CustodyTier
	source  string
	storage StorageTier
	ks      ElevationKeystore
	reason  string
	dataDir string
}

// Tier reports the selected authority, including the zero-value none tier.
func (c Custody) Tier() CustodyTier {
	if c.tier == "" {
		return CustodyNone
	}
	return c.tier
}

// Source reports the registered source name.
func (c Custody) Source() string { return c.source }

// Storage reports informational keystore metadata.
func (c Custody) Storage() StorageTier { return c.storage }

// Reason explains a selection refusal.
func (c Custody) Reason() string { return c.reason }

// Signer returns a signing capability only for elevation-grade custody.
func (c Custody) Signer() (ElevationKeystore, error) {
	if !c.Tier().SatisfiesElevation() || c.ks == nil {
		return nil, ErrCustodyTier(c.Tier(), c.reason)
	}
	return c.ks, nil
}

// CustodyTierError names a terminal custody refusal.
type CustodyTierError struct {
	Tier   CustodyTier
	Reason string
}

func (e *CustodyTierError) Error() string {
	return fmt.Sprintf("elevation: custody tier %s cannot elevate (ADR-0005): %s", e.Tier, e.Reason)
}

// ErrCustodyTier wraps a custody refusal as unsupported.
func ErrCustodyTier(tier CustodyTier, reason string) error {
	return cascade.Wrap(cascade.KindUnsupported, &CustodyTierError{Tier: tier, Reason: reason}, "elevation custody refused")
}

// CustodyTierOf extracts the typed custody refusal.
func CustodyTierOf(err error) (CustodyTier, bool) {
	var e *CustodyTierError
	if errors.As(err, &e) {
		return e.Tier, true
	}
	return CustodyNone, false
}
