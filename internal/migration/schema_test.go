package migration

import (
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
)

// TestLedgerRowValidate proves the closed domain/status sets fail closed:
// a row naming a domain or status this package does not know about is
// refused, never silently accepted.
func TestLedgerRowValidate(t *testing.T) {
	good := LedgerRow{Domain: migrationv1.DomainConfig, Status: StatusDone}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed row was refused: %v", err)
	}

	badDomain := LedgerRow{Domain: "not-a-real-domain", Status: StatusDone}
	if err := badDomain.Validate(); err == nil {
		t.Fatal("an unknown domain was accepted")
	}

	badStatus := LedgerRow{Domain: migrationv1.DomainConfig, Status: "not-a-real-status"}
	if err := badStatus.Validate(); err == nil {
		t.Fatal("an unknown status was accepted")
	}
}

// TestLedgerKeyIsStablePerDomain proves the key layout is deterministic
// and namespaced per domain, so two domains never collide on one row.
func TestLedgerKeyIsStablePerDomain(t *testing.T) {
	a := ledgerKey(migrationv1.DomainConfig)
	b := ledgerKey(migrationv1.DomainConfig)
	if a != b {
		t.Fatalf("ledgerKey is not stable: %q != %q", a, b)
	}
	if a == ledgerKey("vault") {
		t.Fatal("two different domains produced the same key")
	}
}

// TestDecodeLedgerRowRefusesMalformedBytes proves a stored row that
// cannot be decoded is reported as a typed integrity failure, never as a
// silently-empty row (schema.go's doc comment: "treated as tampering, not
// as no row").
func TestDecodeLedgerRowRefusesMalformedBytes(t *testing.T) {
	if _, err := decodeLedgerRow([]byte("not json")); err == nil {
		t.Fatal("malformed bytes decoded without error")
	}
}
