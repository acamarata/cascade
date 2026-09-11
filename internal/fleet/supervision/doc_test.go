package supervision

import "testing"

// TestPackageDefaultMaxItemsPositive is a minimal sanity assertion that
// the package's exported defaults are sane (doc.go's own package-level
// contract: a usable queue with no explicit maxItems configured).
func TestPackageDefaultMaxItemsPositive(t *testing.T) {
	if DefaultMaxItems <= 0 {
		t.Fatalf("DefaultMaxItems = %d, want a positive default", DefaultMaxItems)
	}
}

func TestPackageChangedKindIsNamespacedUnderFleetAttention(t *testing.T) {
	if ChangedKind != "fleet.attention.changed" {
		t.Errorf("ChangedKind = %q, want %q", ChangedKind, "fleet.attention.changed")
	}
}
