// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

// Base aliases string and Mode aliases Base. Consts typed by Mode count
// against Base, the last declared type in the chain, which then fires.
type Base = string

// Mode is the alias of a non-canonical alias.
type Mode = Base

const (
	ModeRestricted Mode = "restricted"
	ModePublic     Mode = "public"
)
