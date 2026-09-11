// Purpose: RepoConfig encode/decode round trip, every malformed-input
//
//	refusal path, and FuzzRepoConfigDecode (06-FORGE-SPEC §5.7: this
//	ticket's own parser ships a fuzz target with a package-local seed
//	corpus, R-21.266).
//
// SPORT: internal.backup.config/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEncodeDecodeRepoConfigRoundTrip(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	want := RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient}

	data, err := EncodeRepoConfig(want)
	if err != nil {
		t.Fatalf("EncodeRepoConfig: %v", err)
	}
	got, err := DecodeRepoConfig(data)
	if err != nil {
		t.Fatalf("DecodeRepoConfig: %v", err)
	}
	if got != want {
		t.Fatalf("DecodeRepoConfig = %+v, want %+v", got, want)
	}
}

func TestDecodeRepoConfigUnknownLayoutVersion(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	// A layout_version Encode would never produce, simulating a config
	// document written by a future cascade version.
	bumped := []byte(`{"layout_version":99,"age_recipient":"` + recipient + `"}`)
	_, err := DecodeRepoConfig(bumped)
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("DecodeRepoConfig(future layout version) error kind = %v, want KindUnsupported", err)
	}
}

func TestDecodeRepoConfigMalformedJSON(t *testing.T) {
	_, err := DecodeRepoConfig([]byte(`{not json`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRepoConfig(malformed) error kind = %v, want KindInvalidInput", err)
	}
}

func TestDecodeRepoConfigUnknownField(t *testing.T) {
	_, err := DecodeRepoConfig([]byte(`{"layout_version":1,"age_recipient":"x","extra":true}`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRepoConfig(unknown field) error kind = %v, want KindInvalidInput", err)
	}
}

func TestDecodeRepoConfigTrailingData(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	_, err := DecodeRepoConfig([]byte(`{"layout_version":1,"age_recipient":"` + recipient + `"}{}`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRepoConfig(trailing data) error kind = %v, want KindInvalidInput", err)
	}
}

func TestDecodeRepoConfigMissingRecipient(t *testing.T) {
	_, err := DecodeRepoConfig([]byte(`{"layout_version":1,"age_recipient":""}`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRepoConfig(missing recipient) error kind = %v, want KindInvalidInput", err)
	}
}

func TestDecodeRepoConfigInvalidRecipientShape(t *testing.T) {
	_, err := DecodeRepoConfig([]byte(`{"layout_version":1,"age_recipient":"not-an-age-recipient"}`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRepoConfig(invalid recipient) error kind = %v, want KindInvalidInput", err)
	}
}

func TestEncodeRepoConfigRefusesUnsupportedVersion(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	_, err := EncodeRepoConfig(RepoConfig{LayoutVersion: 2, AgeRecipient: recipient})
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("EncodeRepoConfig(unsupported version) error kind = %v, want KindUnsupported", err)
	}
}

// FuzzRepoConfigDecode is this ticket's own-parser fuzz target
// (06-FORGE-SPEC §5.7, R-21.266): DecodeRepoConfig must never panic on any
// input, malformed or adversarial. Every input is guaranteed to return an
// error or a value satisfying validateRepoConfig — never a partial or
// best-effort parse.
func FuzzRepoConfigDecode(f *testing.F) {
	_, recipient := newTestAgeKeypair(f)
	f.Add([]byte(`{"layout_version":1,"age_recipient":"` + recipient + `"}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"layout_version":0,"age_recipient":""}`))
	f.Add([]byte(`{"layout_version":1,"age_recipient":"age1notreal"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := DecodeRepoConfig(data)
		if err != nil {
			return
		}
		if cfg.LayoutVersion != CurrentLayoutVersion || cfg.AgeRecipient == "" {
			t.Fatalf("DecodeRepoConfig accepted an invalid config without error: %+v", cfg)
		}
	})
}

func TestEncodeRepoConfigRefusesInvalidRecipientShape(t *testing.T) {
	_, err := EncodeRepoConfig(RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: "not-an-age-recipient"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("EncodeRepoConfig(invalid recipient) error kind = %v, want KindInvalidInput", err)
	}
}

// TestEncodeRepoConfig_EmptyManifestSigningPubKeyIsValid: the P1-E19-W4-
// S41-T2 field is optional (no snapshot has run yet) - existing callers
// that never set it (S-41.T1's ensureRepoConfig, out of this ticket's
// files_scope) must keep working unchanged.
func TestEncodeRepoConfig_EmptyManifestSigningPubKeyIsValid(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	if _, err := EncodeRepoConfig(RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient}); err != nil {
		t.Fatalf("EncodeRepoConfig(empty pubkey) = %v, want nil", err)
	}
}

func TestEncodeRepoConfig_ManifestSigningPubKeyRoundTrip(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	pub := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	data, err := EncodeRepoConfig(RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient, ManifestSigningPubKey: pub})
	if err != nil {
		t.Fatalf("EncodeRepoConfig: %v", err)
	}
	got, err := DecodeRepoConfig(data)
	if err != nil {
		t.Fatalf("DecodeRepoConfig: %v", err)
	}
	if got.ManifestSigningPubKey != pub {
		t.Fatalf("ManifestSigningPubKey round trip = %q, want %q", got.ManifestSigningPubKey, pub)
	}
}

func TestEncodeRepoConfig_ManifestSigningPubKeyWrongLengthRefuses(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	pub := base64.StdEncoding.EncodeToString([]byte("too-short"))
	_, err := EncodeRepoConfig(RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient, ManifestSigningPubKey: pub})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("EncodeRepoConfig(wrong-length pubkey) error kind = %v, want KindInvalidInput", err)
	}
}

func TestEncodeRepoConfig_ManifestSigningPubKeyMalformedBase64Refuses(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	_, err := EncodeRepoConfig(RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient, ManifestSigningPubKey: "not-valid-base64!!!"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("EncodeRepoConfig(malformed pubkey base64) error kind = %v, want KindInvalidInput", err)
	}
}
