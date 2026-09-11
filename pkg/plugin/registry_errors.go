package plugin

// Purpose: the typed error sentinels X/S-50.T1's registry client returns,
//   each wrapping exactly one frozen pkg/cascade Kind per errors.go's own
//   convention ("Domain-specific sentinel error values... belong in their
//   OWNING package, each wrapping exactly one frozen Kind"). Callers use
//   errors.Is(err, plugin.ErrSignatureInvalid) etc. regardless of which
//   constructor produced the concrete error, because *cascade.Error.Is
//   compares only on Kind.
// Inputs: none — these are error values, not functions.
// Outputs: none.
// Constraints: every verification failure below is fail-closed by
//   construction: none of these Kinds is optional or bypassable by
//   configuration (12-QUALITY-CONSTITUTION.md Art.1, Art.3).
// SPORT: pkg/plugin registry-client (ADD) — P1-E24-W5-S50-T1.

import "github.com/acamarata/cascade/pkg/cascade"

var (
	// ErrIndexMalformed is returned when a fetched or cached index
	// document is not valid JSON, is missing schema_version, or carries a
	// schema_version this client does not support.
	ErrIndexMalformed = &cascade.Error{Kind: cascade.KindInvalidInput, Msg: "registry: index malformed"}

	// ErrSignatureInvalid is returned when an index or artifact signature
	// is missing, malformed, or does not verify against the configured
	// public key. No configuration ever suppresses this check.
	ErrSignatureInvalid = &cascade.Error{Kind: cascade.KindIntegrity, Msg: "registry: signature invalid"}

	// ErrChecksumMismatch is returned when a fetched artifact's SHA-256
	// digest does not match the checksum published in its
	// RegistryVersionEntry, or the entry carries no checksum at all.
	ErrChecksumMismatch = &cascade.Error{Kind: cascade.KindIntegrity, Msg: "registry: checksum mismatch"}

	// ErrRegistryHTTP is returned when the registry fetch transport
	// (internal/plugins/registryfetch) receives a non-200 response or a
	// transport-level failure while fetching the index or an artifact.
	ErrRegistryHTTP = &cascade.Error{Kind: cascade.KindUnavailable, Msg: "registry: http fetch failed"}
)
