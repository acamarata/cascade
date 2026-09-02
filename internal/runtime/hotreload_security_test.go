package runtime

import "testing"

func TestCompareSecurity_Policy(t *testing.T) {
	base := EffectiveConfig{Policy: PolicySection{AutonomyProfile: "locked"}}
	t.Run("equal", func(t *testing.T) {
		if paths := CompareSecurity(base, base); len(paths) != 0 {
			t.Fatalf("expected no paths, got %v", paths)
		}
	})
	t.Run("any change flagged", func(t *testing.T) {
		proposed := EffectiveConfig{Policy: PolicySection{AutonomyProfile: "autonomous"}}
		paths := CompareSecurity(base, proposed)
		if len(paths) != 1 || paths[0].Family != "policy" {
			t.Fatalf("got %v", paths)
		}
	})
}

func TestCompareSecurity_Secrets(t *testing.T) {
	base := EffectiveConfig{Secrets: SecretsSection{KeychainBackend: "os-keychain"}}
	proposed := EffectiveConfig{Secrets: SecretsSection{KeychainBackend: "plaintext"}}
	if paths := CompareSecurity(base, base); len(paths) != 0 {
		t.Fatalf("equal case: expected none, got %v", paths)
	}
	if paths := CompareSecurity(base, proposed); len(paths) != 1 || paths[0].Family != "secrets" {
		t.Fatalf("got %v", paths)
	}
}

func TestCompareSecurity_Sync(t *testing.T) {
	tighter := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only"}}}
	looser := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "server-primary"}}}
	equal := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "synced"}}}

	if paths := CompareSecurity(equal, equal); len(paths) != 0 {
		t.Fatalf("equal: expected none, got %v", paths)
	}
	if paths := CompareSecurity(tighter, looser); len(paths) == 0 {
		t.Fatal("local-only -> server-primary must be flagged as loosening")
	}
	if paths := CompareSecurity(looser, tighter); len(paths) != 0 {
		t.Fatalf("server-primary -> local-only is tightening, expected none, got %v", paths)
	}
}

func TestCompareSecurity_Sync_UnknownClassFailsClosed(t *testing.T) {
	base := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only"}}}
	proposed := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "mystery-tier"}}}
	if paths := CompareSecurity(base, proposed); len(paths) == 0 {
		t.Fatal("unrecognised sync class must fail closed (flagged as loosening)")
	}
}

func TestCompareSecurity_Nodes(t *testing.T) {
	base := EffectiveConfig{Nodes: NodesSection{TrustTier: "controller"}}
	proposed := EffectiveConfig{Nodes: NodesSection{TrustTier: "worker-trusted"}}
	if paths := CompareSecurity(base, base); len(paths) != 0 {
		t.Fatalf("equal: got %v", paths)
	}
	if paths := CompareSecurity(base, proposed); len(paths) != 1 || paths[0].Family != "nodes" {
		t.Fatalf("got %v", paths)
	}
}

func TestCompareSecurity_Conductor(t *testing.T) {
	tight := EffectiveConfig{Conductor: ConductorSection{ExternalRoutingEnabled: false, SpillEnabled: false}}
	loose := EffectiveConfig{Conductor: ConductorSection{ExternalRoutingEnabled: true, SpillEnabled: true}}

	if paths := CompareSecurity(tight, tight); len(paths) != 0 {
		t.Fatalf("equal: got %v", paths)
	}
	if paths := CompareSecurity(tight, loose); len(paths) != 2 {
		t.Fatalf("expected 2 loosening paths (routing+spill), got %v", paths)
	}
	if paths := CompareSecurity(loose, tight); len(paths) != 0 {
		t.Fatalf("true->false is tightening, expected none, got %v", paths)
	}
}

func TestCompareSecurity_Elevation(t *testing.T) {
	tight := EffectiveConfig{Elevation: elevationSection{AllowRemote: false, HelperPubkey: ""}}
	loose := EffectiveConfig{Elevation: elevationSection{AllowRemote: true, HelperPubkey: "key1"}}

	if paths := CompareSecurity(tight, tight); len(paths) != 0 {
		t.Fatalf("equal: got %v", paths)
	}
	paths := CompareSecurity(tight, loose)
	if len(paths) < 2 {
		t.Fatalf("expected allow_remote + helper_pubkey both flagged, got %v", paths)
	}
	// Tightening allow_remote true->false is not itself flagged by
	// compareBool, but ANY helper_pubkey change still is (any-change
	// rule) — CompareSecurity is not the sole gate for elevation; see
	// TestHotReloader_ElevationChangeAlwaysRejected for the absolute
	// hard-gate this ticket layers on top.
	tighten := CompareSecurity(loose, EffectiveConfig{Elevation: elevationSection{AllowRemote: false, HelperPubkey: "key1"}})
	if len(tighten) != 0 {
		t.Fatalf("allow_remote true->false with unchanged helper_pubkey should not itself be flagged by CompareSecurity, got %v", tighten)
	}
}

func TestCompareSecurity_AllSixFamiliesEmptyOnIdenticalConfig(t *testing.T) {
	cfg := EffectiveConfig{
		Policy:    PolicySection{AutonomyProfile: "locked"},
		Secrets:   SecretsSection{KeychainBackend: "os"},
		Sync:      SyncSection{Classes: map[string]string{"memory": "local-only"}},
		Nodes:     NodesSection{TrustTier: "controller"},
		Conductor: ConductorSection{ExternalRoutingEnabled: true, SpillEnabled: true},
		Elevation: elevationSection{AllowRemote: true, HelperPubkey: "k"},
	}
	if paths := CompareSecurity(cfg, cfg); len(paths) != 0 {
		t.Fatalf("identical config must produce zero loosening paths, got %v", paths)
	}
}
