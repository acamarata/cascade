package plugins

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): cover the two storage-boundary adapters the plugin
//   composition root installs — the grant checker that decides whether a
//   plugin may reach another domain, and the scanner that decides whether
//   content it writes carries a credential. Both were built and wired and
//   neither had a single test, which for a permission decision is the worst
//   place in the package to have none.
// SPORT: internal/plugins dispatch-adapter tests (ADD).

// TestMetadataGrantChecker_GrantsAndRefusals drives the real adapter over a
// real store, asserting on the KIND as well as the outcome: a caller that
// distinguishes "denied" from "broken" can only do so if the Kind is right.
func TestMetadataGrantChecker_GrantsAndRefusals(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	checker := metadataGrantChecker{store: store}

	if err := SaveMetadata(ctx, store, PluginMetadata{
		Name:    "installed-plugin",
		Enabled: true,
		Grants:  []string{"storage.cross-domain", "net.http"},
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	t.Run("granted capability passes", func(t *testing.T) {
		if err := checker.CheckGrant(ctx, "installed-plugin", "storage.cross-domain"); err != nil {
			t.Fatalf("CheckGrant on a granted capability: %v", err)
		}
	})

	t.Run("ungranted capability is denied", func(t *testing.T) {
		err := checker.CheckGrant(ctx, "installed-plugin", "storage.not-granted")
		if err == nil {
			t.Fatal("CheckGrant allowed a capability the plugin was never granted")
		}
		if got, ok := cascade.KindOf(err); !ok || got != cascade.KindPermissionDenied {
			t.Fatalf("error kind = %v (typed=%v), want %v", got, ok, cascade.KindPermissionDenied)
		}
	})

	t.Run("uninstalled plugin is denied, not allowed by default", func(t *testing.T) {
		err := checker.CheckGrant(ctx, "never-installed", "storage.cross-domain")
		if err == nil {
			t.Fatal("CheckGrant allowed a plugin that is not installed: an absent record must fail closed")
		}
		if got, ok := cascade.KindOf(err); !ok || got != cascade.KindPermissionDenied {
			t.Fatalf("error kind = %v (typed=%v), want %v", got, ok, cascade.KindPermissionDenied)
		}
	})
}

// TestMetadataGrantCheckerIsNotFooledByASimilarName guards the loop's
// equality check: a prefix or substring match would hand a plugin a
// capability adjacent to one it actually holds.
func TestMetadataGrantCheckerIsNotFooledByASimilarName(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{
		Name: "p", Enabled: true, Grants: []string{"storage.read"},
	}); err != nil {
		t.Fatal(err)
	}
	checker := metadataGrantChecker{store: store}
	for _, capability := range []string{"storage", "storage.read.write", "torage.read", "STORAGE.READ"} {
		if err := checker.CheckGrant(ctx, "p", capability); err == nil {
			t.Errorf("CheckGrant allowed %q against a grant of only %q", capability, "storage.read")
		}
	}
}

// TestDetectorSecretScanner_RealDetector proves the adapter reports a real
// credential-shaped span through the REAL detector the composition root
// builds, not a stub that always answers one way. Both answers are
// asserted: a scanner stuck on true would block every legitimate write,
// and one stuck on false would pass every credential through.
func TestDetectorSecretScanner_RealDetector(t *testing.T) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("build the real detector: %v", err)
	}
	scanner := detectorSecretScanner{d: detector}

	if scanner.HasSecret([]byte("the quick brown fox jumps over the lazy dog")) {
		t.Error("HasSecret reported a secret in ordinary prose")
	}

	// A credential-shaped span the default registry recognises with
	// certainty. If this stops being detected the adapter is not what
	// changed, but the test must fail either way: a storage boundary that
	// silently stops recognising secrets is exactly the regression here.
	withSecret := []byte("export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	if !scanner.HasSecret(withSecret) {
		t.Errorf("HasSecret found no secret in %q", withSecret)
	}
}
