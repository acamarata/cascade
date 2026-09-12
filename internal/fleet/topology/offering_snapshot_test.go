package topology

import "testing"

func TestModelIdentitySameModelEmptyCanonicalIsSameModel(t *testing.T) {
	a := ModelIdentity{CanonicalID: "", Family: "anthropic"}
	b := ModelIdentity{CanonicalID: "claude-opus-4", Family: "anthropic"}
	if !a.SameModel(b) {
		t.Error("an empty CanonicalID should be treated as the SAME model (R-21.72 fail-closed)")
	}
	if !b.SameModel(a) {
		t.Error("SameModel should be symmetric for the empty-canonical case")
	}
}

// TestModelIdentityEmptyCanonicalIDIsSameModel is the R-21.72 named
// acceptance test.
func TestModelIdentityEmptyCanonicalIDIsSameModel(t *testing.T) {
	TestModelIdentitySameModelEmptyCanonicalIsSameModel(t)
}

func TestModelIdentitySameModelDistinctWhenBothSet(t *testing.T) {
	a := ModelIdentity{CanonicalID: "model-a"}
	b := ModelIdentity{CanonicalID: "model-b"}
	if a.SameModel(b) {
		t.Error("two distinct non-empty canonical ids should not be the same model")
	}
}

func TestModelIdentitySameFamilyEmptyIsSameFamily(t *testing.T) {
	a := ModelIdentity{Family: ""}
	b := ModelIdentity{Family: "openai"}
	if !a.SameFamily(b) {
		t.Error("an empty Family should be treated as the SAME family (R-21.72 fail-closed)")
	}
}

func TestModelIdentitySameFamilyDistinct(t *testing.T) {
	a := ModelIdentity{Family: "anthropic"}
	b := ModelIdentity{Family: "openai"}
	if a.SameFamily(b) {
		t.Error("two distinct non-empty families should not match")
	}
}

func TestLaneSnapshotReturnsCopy(t *testing.T) {
	lane := Lane{OfferingSnapshot: OfferingSnapshot{Modalities: []string{"text"}, Version: 1}}
	got := lane.Snapshot()
	got.Modalities[0] = "mutated"
	if lane.OfferingSnapshot.Modalities[0] == "mutated" {
		t.Error("Snapshot() should return a copy; mutating it must not affect the Lane's stored snapshot")
	}
}

// TestOfferingSnapshotVersionedAtomically proves replaceSnapshot bumps the
// version and replaces the whole snapshot atomically, never mutating a
// field of the prior snapshot in place.
func TestOfferingSnapshotVersionedAtomically(t *testing.T) {
	lane := Lane{OfferingSnapshot: OfferingSnapshot{Modalities: []string{"text"}, Version: 3}}
	prior := lane.OfferingSnapshot
	lane.replaceSnapshot(OfferingSnapshot{Modalities: []string{"text", "vision"}, ContextTokens: 128000})
	if lane.OfferingSnapshot.Version != 4 {
		t.Errorf("version = %d, want prior+1 = 4", lane.OfferingSnapshot.Version)
	}
	if len(lane.OfferingSnapshot.Modalities) != 2 {
		t.Errorf("replaced snapshot should carry the new field set, got %v", lane.OfferingSnapshot.Modalities)
	}
	if prior.Version != 3 || len(prior.Modalities) != 1 {
		t.Error("the prior snapshot value captured before replaceSnapshot must be unmutated")
	}
}

func TestOfferingSnapshotCloneDeepCopies(t *testing.T) {
	s := OfferingSnapshot{Modalities: []string{"text"}, ProviderPolicy: ProviderPolicy{Flags: map[string]string{"k": "v"}}}
	c := s.clone()
	c.Modalities[0] = "mutated"
	c.ProviderPolicy.Flags["k"] = "mutated"
	if s.Modalities[0] == "mutated" {
		t.Error("clone should deep-copy Modalities")
	}
	if s.ProviderPolicy.Flags["k"] == "mutated" {
		t.Error("clone should deep-copy ProviderPolicy.Flags")
	}
}

func TestEncodeDecodeOfferingSnapshotRoundTrip(t *testing.T) {
	s := OfferingSnapshot{
		Modalities: []string{"text", "vision"}, ContextTokens: 200000, MaxOutputTokens: 8192,
		Tools: []string{"bash"}, Efforts: []Effort{EffortLow, EffortHigh},
		InteractionClasses: []InteractionClass{InteractionInteractive},
		ProviderPolicy:     ProviderPolicy{Flags: map[string]string{"batch_allowed": "true"}},
	}
	encoded, err := encodeOfferingSnapshot(s)
	if err != nil {
		t.Fatalf("encodeOfferingSnapshot: %v", err)
	}
	var decoded OfferingSnapshot
	if err := decodeOfferingSnapshot(encoded, &decoded); err != nil {
		t.Fatalf("decodeOfferingSnapshot: %v", err)
	}
	if decoded.ContextTokens != s.ContextTokens || decoded.MaxOutputTokens != s.MaxOutputTokens {
		t.Errorf("scalar fields mismatch: got %+v, want %+v", decoded, s)
	}
	if len(decoded.Modalities) != 2 || len(decoded.Tools) != 1 || len(decoded.Efforts) != 2 {
		t.Errorf("slice fields mismatch after round trip: %+v", decoded)
	}
	if decoded.ProviderPolicy.Flags["batch_allowed"] != "true" {
		t.Errorf("ProviderPolicy.Flags mismatch after round trip: %+v", decoded.ProviderPolicy)
	}
}

func TestDecodeOfferingSnapshotMalformedFails(t *testing.T) {
	var out OfferingSnapshot
	if err := decodeOfferingSnapshot("not json", &out); err == nil {
		t.Fatal("decodeOfferingSnapshot(malformed) should have failed")
	}
}

func TestNonNilJSON(t *testing.T) {
	if got := nonNilJSON(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNilJSON(nil) = %v, want a non-nil empty slice", got)
	}
}
