//go:build darwin && integration

// Purpose: Art.2 real-counterpart integration test for the darwin custody
//
//	backend: exercises the REAL /usr/bin/security tool against the user's
//	login keychain, under a dedicated service name no other entry uses.
//
// Constraints: it mutates the real keychain, so it lives behind the
//
//	integration build tag and cleans up after itself.

package secrets

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestKeychainRealSecurityToolRoundTrip runs the full Set/Get/Rotate/Delete
// cycle against the REAL /usr/bin/security and the user's login keychain,
// under a service name no other entry uses. Integration-tagged: it mutates
// the real keychain, so it is not part of the default unit lane.
func TestKeychainRealSecurityToolRoundTrip(t *testing.T) {
	custody, err := platformCustody(Config{Service: "cascade-vault-integration-test"})
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
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := custody.Set(ctx, "ROUNDTRIP", []byte("second")); err != nil {
		t.Fatalf("rotate via Set: %v", err)
	}
	got, err = custody.Get(ctx, "ROUNDTRIP")
	if err != nil || string(got) != "second" {
		t.Fatalf("Get after rotate = %q, %v", got, err)
	}
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

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
