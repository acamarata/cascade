package plugin

// Purpose: registry-client types — the fetch/verify/cache shape X/S-50.T1's
//
//	"registry protocol + client (static signed JSON index; fetch/verify/
//	cache)" implements against. This ticket declares the seam's data
//	shapes and interfaces only; X/S-50.T1 owns the live HTTP client, the
//	signature scheme, and the on-disk cache — none of that is built here.
//
// Inputs: none at this layer — interface and data-shape declarations only.
// Outputs: none.
// Constraints: pkg/plugin never imports internal/ (Art.10.2); no bare
//
//	fmt.Errorf/errors.New (boundary lint — vacuous, no constructors here
//	mint an error); self-contained — does not import pkg/provider, mirroring
//	register.go's BuiltinHandlers precedent of declaring its own seam
//	rather than reaching for a generic cross-package type.
//
// SPORT: pkg/plugin registry-client-types (ADD) — P1-E15-W4-S33-T1.

import "context"

// RegistryVersionEntry is one published version of one plugin in the
// static signed JSON registry index: the artifact location plus the
// checksum and signature X/S-50.T1's client verifies before trusting it.
type RegistryVersionEntry struct {
	// Version is the plugin's own semver version string.
	Version string `json:"version"`
	// HostVersion is the semver range of host versions this version
	// expects, mirroring Manifest.HostVersion's format.
	HostVersion string `json:"host_version"`
	// DownloadURL is where the artifact bytes are fetched from.
	DownloadURL string `json:"download_url"`
	// Checksum is the hex-encoded content hash of the artifact at
	// DownloadURL, checked before the artifact is used.
	Checksum string `json:"checksum"`
	// Signature is the detached signature over the artifact bytes,
	// verified against the registry's published public key before the
	// checksum itself is trusted.
	Signature string `json:"signature"`
}

// RegistryIndexEntry is one plugin's entry in the static signed JSON
// registry index: its identity plus every published RegistryVersionEntry.
type RegistryIndexEntry struct {
	// ID matches the plugin's Manifest.ID.
	ID string `json:"id"`
	// Name is the plugin's human display name.
	Name string `json:"name"`
	// Description is the plugin's human summary, searched by
	// RegistryClient.Search alongside Name and Tags (X/S-50.T1).
	Description string `json:"description"`
	// Tags are free-text labels searched by RegistryClient.Search
	// alongside Name and Description (X/S-50.T1).
	Tags []string `json:"tags"`
	// LatestVersion is the Version string of the entry in Versions the
	// registry currently recommends installing.
	LatestVersion string `json:"latest_version"`
	// Versions lists every published version, oldest first.
	Versions []RegistryVersionEntry `json:"versions"`
}

// RegistryIndex is the top-level static signed JSON document X/S-50.T1's
// client fetches: every published plugin entry, plus the whole document's
// own signature.
type RegistryIndex struct {
	// SchemaVersion identifies the index document's own schema, separate
	// from any individual plugin's Manifest.Schema.
	SchemaVersion string `json:"schema_version"`
	// Entries lists every plugin the registry publishes.
	Entries []RegistryIndexEntry `json:"entries"`
	// Signature is the detached signature over Entries plus SchemaVersion
	// (in the canonical encoding X/S-50.T1 defines), verified before any
	// entry is trusted.
	Signature string `json:"signature"`
}

// RegistryFetcher fetches the raw bytes a RegistryVerifier then checks:
// the index document itself, and any individual artifact named by a
// RegistryVersionEntry.
type RegistryFetcher interface {
	// FetchIndex returns the raw bytes of the registry's current index
	// document, unverified.
	FetchIndex(ctx context.Context) ([]byte, error)
	// FetchArtifact returns the raw bytes of the artifact entry names,
	// unverified.
	FetchArtifact(ctx context.Context, entry RegistryVersionEntry) ([]byte, error)
}

// RegistryVerifier checks that fetched bytes are what they claim to be
// before anything downstream trusts them. A verification failure is a
// tamper finding, not an ordinary input error — X/S-50.T1's implementation
// returns a pkg/cascade error of KindIntegrity for both methods.
type RegistryVerifier interface {
	// VerifyIndex checks data against the index document's own embedded
	// or out-of-band signature and returns the parsed RegistryIndex only
	// if verification succeeds.
	VerifyIndex(ctx context.Context, data []byte) (RegistryIndex, error)
	// VerifyArtifact checks data against entry's Checksum and Signature.
	VerifyArtifact(ctx context.Context, data []byte, entry RegistryVersionEntry) error
}

// RegistryCache stores verified bytes locally, keyed by the caller's own
// choice of key (X/S-50.T1 keys by "<plugin id>@<version>" in practice, but
// that convention is the client's to define, not this interface's).
type RegistryCache interface {
	// Get returns the cached bytes for key, or ok=false if nothing is
	// cached under key.
	Get(ctx context.Context, key string) (data []byte, ok bool, err error)
	// Put stores data under key, replacing any existing entry.
	Put(ctx context.Context, key string, data []byte) error
}
