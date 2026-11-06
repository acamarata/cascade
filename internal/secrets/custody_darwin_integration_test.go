//go:build darwin && integration

// Purpose: Art.2 real-counterpart integration test for the darwin custody
//
//	backend: exercises the REAL /usr/bin/security tool against a throwaway
//	keychain, under a dedicated service name no other entry uses.
//
// Constraints: it mutates the real keychain, so it lives behind the
//
//	integration build tag and cleans up after itself.

package secrets

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestKeychainRealSecurityToolRoundTrip runs the full Set/Get/Rotate/Delete
// cycle against the REAL /usr/bin/security and a throwaway keychain supplied
// by CASCADE_TEST_KEYCHAIN_PATH. It is excluded from the default unit lane.
func TestKeychainRealSecurityToolRoundTrip(t *testing.T) {
	path := os.Getenv("CASCADE_TEST_KEYCHAIN_PATH")
	if path == "" {
		t.Skip("CASCADE_TEST_KEYCHAIN_PATH must name a throwaway keychain")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
	custody, err := platformCustody(Config{Service: "cascade-vault-integration-test", KeychainPath: path})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	ctx := context.Background()
	if !custody.Available() {
		t.Skip("/usr/bin/security is not usable on this host")
	}
	t.Cleanup(func() {
		_ = custody.Delete(ctx, "ROUNDTRIP")
	})
	if err := custody.Set(ctx, "ROUNDTRIP", []byte("first")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := custody.Get(ctx, "ROUNDTRIP")
	if err != nil || string(got) != "first" {
		t.Fatal("Get did not preserve bytes")
	}
	if err := custody.Set(ctx, "ROUNDTRIP", []byte("second")); err != nil {
		t.Fatalf("rotate via Set: %v", err)
	}
	got, err = custody.Get(ctx, "ROUNDTRIP")
	if err != nil || string(got) != "second" {
		t.Fatal("Get after rotate did not preserve bytes")
	}
	checkRealKeychainFormatValues(t, custody)
	names, err := custody.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slicesContains(names, "ROUNDTRIP") {
		t.Fatalf("List = %v, want it to contain ROUNDTRIP", names)
	}
	if err := custody.Delete(ctx, "ROUNDTRIP"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := custody.Get(ctx, "ROUNDTRIP"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
}

// checkRealKeychainFormatValues exercises the cv1 fixtures on the supplied keychain.
func checkRealKeychainFormatValues(t *testing.T, custody Custody) {
	t.Helper()
	ctx := context.Background()
	for i, value := range keychainFormatValues() {
		if err := custody.Set(ctx, "ROUNDTRIP", value); err != nil {
			t.Fatalf("fixture %d: Set failed", i)
		}
		got, err := custody.Get(ctx, "ROUNDTRIP")
		if err != nil || !bytes.Equal(got, value) {
			t.Fatalf("fixture %d: real tool round trip did not preserve bytes", i)
		}
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
