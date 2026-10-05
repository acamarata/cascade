// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// Tier has an innocent name; its const values are tier words, so the
// const-value rule fires.
type Tier string

const (
	TierRestricted Tier = "restricted"
	TierPublic     Tier = "public"
)
