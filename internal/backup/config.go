// Purpose: the repo-level config document — layout version + the age
//
//	recipient the repo is encrypted to (PUBLIC material only, per
//	00-VISION principle 10: "recovery keys never live beside backups").
//	This is also the ticket's own parser (06-FORGE-SPEC §5.7): an unknown
//	layout version or malformed document fails closed, never a
//	best-effort parse, which FuzzRepoConfigDecode (config_test.go) proves
//	against arbitrary bytes.
//
// Inputs: a RepoConfig value (Encode) or a raw byte slice (Decode).
// Outputs: canonical JSON bytes, or a validated RepoConfig.
// Constraints: Decode never panics on any input, including truncated or
//
//	adversarial bytes (the fuzz target's whole point); DisallowUnknownFields
//	so a future field a decoder does not understand refuses rather than
//	being silently dropped.
//
// SPORT: internal.backup.config/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"

	"filippo.io/age"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CurrentLayoutVersion is the only layout version this build's config
// decoder accepts. A document naming any other version fails closed
// (KindUnsupported) rather than attempting a best-effort read.
const CurrentLayoutVersion = 1

// RepoConfig is the repo's config/ document: layout version, the age
// recipient the repo is encrypted to, and (P1-E19-W4-S41-T2, R-14.58) the
// backup-manifest signing key's PUBLIC half. Neither field carries private
// material: AgeRecipient decrypts nothing on its own, and
// ManifestSigningPubKey (base64-encoded Ed25519, 32 bytes) only lets a
// reader VERIFY a manifest signature — it can never produce one. It is
// optional (empty string) until a snapshot has been created: T2's
// CreateSnapshot populates it itself, on first use, from the signing key
// it resolves by vault reference (config.go carries no dependency on the
// later S-42.T6 ceremony beyond that key existing).
type RepoConfig struct {
	LayoutVersion         int    `json:"layout_version"`
	AgeRecipient          string `json:"age_recipient"`
	ManifestSigningPubKey string `json:"manifest_signing_pubkey,omitempty"`
}

// EncodeRepoConfig validates cfg and marshals it to canonical JSON.
// Validation happens here (not only on decode) so WriteRepoConfig can never
// persist a document a later ReadRepoConfig would refuse.
func EncodeRepoConfig(cfg RepoConfig) ([]byte, error) {
	if err := validateRepoConfig(cfg); err != nil {
		return nil, err
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: encode repo config")
	}
	return data, nil
}

// DecodeRepoConfig parses data as a repo config document. It never panics
// on any input (FuzzRepoConfigDecode proves this against arbitrary bytes)
// and fails closed — via a taxonomy error, never a partial or best-effort
// result — on malformed JSON, an unrecognized field, trailing data, or an
// unsupported layout version.
func DecodeRepoConfig(data []byte) (RepoConfig, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg RepoConfig
	if err := dec.Decode(&cfg); err != nil {
		return RepoConfig{}, cascade.Wrap(cascade.KindInvalidInput, err, "backup: malformed repo config")
	}
	if dec.More() {
		return RepoConfig{}, cascade.New(cascade.KindInvalidInput, "backup: repo config has trailing data")
	}
	if err := validateRepoConfig(cfg); err != nil {
		return RepoConfig{}, err
	}
	return cfg, nil
}

// validateRepoConfig is Encode and Decode's shared refusal logic: an
// unsupported layout version is a distinct, typed refusal (KindUnsupported)
// from every other malformed-input case (KindInvalidInput), so a caller can
// tell "this repo is from a future cascade" apart from "this document is
// simply broken."
func validateRepoConfig(cfg RepoConfig) error {
	if cfg.LayoutVersion != CurrentLayoutVersion {
		return cascade.Newf(cascade.KindUnsupported,
			"backup: repo config layout version %d is not supported (want %d)",
			cfg.LayoutVersion, CurrentLayoutVersion)
	}
	if cfg.AgeRecipient == "" {
		return cascade.New(cascade.KindInvalidInput, "backup: repo config missing age recipient")
	}
	if _, err := age.ParseX25519Recipient(cfg.AgeRecipient); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "backup: repo config age recipient is not a valid age recipient")
	}
	return validateManifestSigningPubKey(cfg.ManifestSigningPubKey)
}

// validateManifestSigningPubKey validates the optional pubkey field: an
// empty string is valid (not yet populated — no snapshot has run), but a
// non-empty value must decode as exactly one Ed25519 public key. This is
// the S-41.T2 extension to S-41.T1's decoder named in the ticket's HOW.
func validateManifestSigningPubKey(encoded string) error {
	if encoded == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "backup: repo config manifest signing pubkey is not valid base64")
	}
	if len(raw) != ed25519.PublicKeySize {
		return cascade.Newf(cascade.KindInvalidInput,
			"backup: repo config manifest signing pubkey is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return nil
}
