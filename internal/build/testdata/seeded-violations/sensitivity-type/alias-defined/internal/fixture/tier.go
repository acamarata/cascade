// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// Tier is a defined tier type with an innocent name and no consts of its
// own. Mode aliases it; consts typed by Mode count against Tier.
type Tier string

// Mode is an alias of a defined, non-canonical tier type.
type Mode = Tier

const (
	ModeRestricted Mode = "restricted"
	ModePublic     Mode = "public"
)
