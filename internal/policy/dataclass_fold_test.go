package policy

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestApprovalRecordSignatureStable(t *testing.T) {
	key, err := os.ReadFile("testdata/approval/pre-sec34/public-key.bin")
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewApprovalVerifier(ed25519.PublicKey(key), testkit.NewFrozenClock(fixtureIssued.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		word string
		tier provider.SensitivityTier
	}{
		{"public", provider.SensitivityPublic}, {"internal", provider.SensitivityInternal},
		{"confidential", provider.SensitivityRestricted}, {"secret", provider.SensitivityLocalOnly},
	} {
		signed, err := os.ReadFile("testdata/approval/pre-sec34/" + tc.word + ".bin")
		if err != nil {
			t.Fatal(err)
		}
		rec, err := v.Verify(signed)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Sensitivity != tc.word {
			t.Fatalf("sensitivity = %v", rec.Sensitivity)
		}
		tier, err := provider.ParseDataClassName(rec.Sensitivity)
		if err != nil || tier != tc.tier {
			t.Fatalf("tier = %v: %v", tier, err)
		}
		if !bytes.Equal(CanonicalEncode(*rec), signed[:len(signed)-ed25519.SignatureSize]) {
			t.Fatal("signed payload changed")
		}
	}
}

func TestDataClassCeilingRefusesAboveInternal(t *testing.T) {
	for _, tier := range []provider.SensitivityTier{
		provider.SensitivityLocalOnly, provider.SensitivityRestricted,
		provider.SensitivityInternal, provider.SensitivityPublic,
	} {
		c := evalRun{req: EvalRequest{DataClass: new(tier), LaneMaxDataClass: new(provider.SensitivityInternal)}}
		layer, err := c.layerDataClass()
		deny := tier == provider.SensitivityLocalOnly || tier == provider.SensitivityRestricted
		if deny && (err != ErrDataClassDenied || err.Error() != ErrDataClassDenied.Error()) {
			t.Fatalf("denial = %v", err)
		}
		if !deny && err != nil {
			t.Fatal(err)
		}
		if tier == provider.SensitivityLocalOnly && layer.Rule != "secret material may not travel to a destination capped at internal" {
			t.Fatal(layer.Rule)
		}
		if tier == provider.SensitivityRestricted && layer.Rule != "confidential material may not travel to a destination capped at internal" {
			t.Fatal(layer.Rule)
		}
	}
}

func TestUnsetDataClassFailsClosed(t *testing.T) {
	for _, ceiling := range []provider.SensitivityTier{provider.SensitivityPublic, provider.SensitivityInternal, provider.SensitivityRestricted, 9} {
		c := evalRun{req: EvalRequest{LaneMaxDataClass: new(ceiling)}}
		layer, err := c.layerDataClass()
		if err != ErrDataClassDenied || err.Error() != ErrDataClassDenied.Error() || !bytes.HasPrefix([]byte(layer.Rule), []byte("secret material")) {
			t.Fatalf("%+v: %v", layer, err)
		}
	}
	c := evalRun{}
	if layer, err := c.layerDataClass(); err != nil || layer.Rule != "no external destination is declared, so no data class applies" {
		t.Fatalf("%+v: %v", layer, err)
	}
}
