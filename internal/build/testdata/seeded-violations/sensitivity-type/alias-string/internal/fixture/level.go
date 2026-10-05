// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// Level is an alias of string, not of a canonical tier, so the gate checks
// it as a declaration and its tier-word consts fire the const-value rule.
type Level = string

const (
	LevelRestricted Level = "restricted"
	LevelPublic     Level = "public"
)
