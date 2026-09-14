package plugin

// Purpose: RemoteSpec, the [remote] manifest table a RuntimeRemote
// manifest declares its connection endpoint through. Split into its own
// file rather than folded into manifest.go, which already sat at 293 of
// the repo's 300-line cap before this ticket.
//
// CONTRACT GAP THIS FILE CLOSES (P1-E15-W4-S33-T4, recorded per
// LANE-RULES §1): the ticket's full_desc reads "given a manifest entry
// declaring a remote runtime host", and RuntimeRemote has been a valid
// RuntimeMode value since P1-E03-W1-S05-T6 — but grepping this whole
// package before this ticket found no field anywhere in Manifest naming
// a host, port, or endpoint for it. A remote-runtime plugin's manifest
// had a runtime tag and nothing to dial. This file, and the one field it
// adds to Manifest, close that gap; it is a minimal, additive schema
// change (existing manifests decode an empty RemoteSpec exactly as they
// did before this field existed), not a redesign of the v2 schema.
//
// Inputs: none (pure type declaration).
// Outputs: RemoteSpec, embedded in Manifest as the Remote field.
// Constraints: pkg/plugin never imports internal/ (Art.10.2).
// SPORT: pkg/plugin remote-spec (ADD) — P1-E15-W4-S33-T4.

// RemoteSpec is a RuntimeRemote manifest's connection endpoint: the host
// and port internal/plugins/remote.Dispatch dials to perform the
// handshake this ticket implements. Every other runtime leaves this at
// its zero value, which Validate never inspects for them.
type RemoteSpec struct {
	// Host is the remote runtime's hostname or IP literal. Required when
	// Runtime == RuntimeRemote (validateRuntime in validate.go).
	Host string `toml:"host"`
	// Port is the remote runtime's TCP port. Required (non-zero) when
	// Runtime == RuntimeRemote.
	Port int `toml:"port"`
}
