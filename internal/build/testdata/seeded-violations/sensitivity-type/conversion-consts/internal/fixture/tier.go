// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// Tier gets its tier words through conversion-form consts, which name no
// type on the left; the conversion counts against Tier.
type Tier string

const (
	TierRestricted = Tier("restricted")
	TierPublic     = Tier("public")
)
