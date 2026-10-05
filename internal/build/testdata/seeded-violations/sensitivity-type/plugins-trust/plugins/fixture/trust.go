// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// TrustLevel under plugins/: the name rule fires there too.
type TrustLevel string
