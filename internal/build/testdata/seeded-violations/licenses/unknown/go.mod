// Seeded-violation fixture for P1-E01-W1-S01-T3's license allowlist gate
// (internal/build/licenses_test.go, TestLicenses_SeededViolationRed_Unknown).
// This is a fake go.mod: it requires a module with no entry in any
// registry the test supplies, so the gate must fail closed as "unknown
// license". Data only — never built or resolved.
module example.com/fixture

go 1.26.2

require (
	example.com/nowhereregistered v0.1.0
)
