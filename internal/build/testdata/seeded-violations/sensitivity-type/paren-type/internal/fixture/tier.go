// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// Tier wraps its underlying type in parentheses; it is still a string kind.
type Tier (string)

const (
	TierRestricted Tier = "restricted"
	TierPublic     Tier = "public"
)
