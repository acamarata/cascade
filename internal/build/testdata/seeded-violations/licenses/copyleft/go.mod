// Seeded-violation fixture for P1-E01-W1-S01-T3's license allowlist gate
// (internal/build/licenses_test.go, TestLicenses_SeededViolationRed_Copyleft).
// This is a fake go.mod: it requires a fake module that the test registers
// (in an in-test-only registry, never KnownModuleLicenses) as GPL-3.0, a
// license not on LicenseAllowlist. Data only — never built or resolved.
module example.com/fixture

go 1.26.2

require example.com/gplcopyleft v1.0.0
