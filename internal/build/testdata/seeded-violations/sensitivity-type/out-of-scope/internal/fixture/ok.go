// Negative fixture for internal/build/arch_sensitivity_test.go (P1-SEC-19):
// every declaration here is out of the gate's scope and must stay silent.
// It lives under testdata/ so the toolchain never compiles it.
package fixture

import "github.com/acamarata/cascade/pkg/provider"

// SensitivityGate is an interface: out of scope by construction.
type SensitivityGate interface{ Check() error }

// FailClosedSensitivity is a struct: out of scope by construction.
type FailClosedSensitivity struct{}

// ProvenanceError is an error type: out of scope by construction.
type ProvenanceError error

// DataClass aliases a canonical type, and TrustTag reaches one through the
// alias Trust: all three are allowed.
type (
	DataClass = provider.SensitivityTier
	Trust     = provider.Provenance
	TrustTag  = Trust
)

// Consts typed by an alias that reaches a canonical type count against the
// canonical type, which the gate allows.
const (
	tagTrusted   TrustTag = "trusted"
	tagUntrusted TrustTag = "untrusted-source"
)

// Untyped tier strings declare no type, so neither rule applies.
const (
	tierRestricted = "restricted"
	tierPublic     = "public"
)
