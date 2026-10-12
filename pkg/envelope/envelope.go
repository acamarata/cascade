// Purpose: the one canonical scope envelope (contract:scope-envelope,
//
//	ADR-0007): the signed statement of what one unit of delegated work may
//	touch - filesystem roots, tools, commands, egress classes, credential
//	grant references, a resource budget with typed extensions, a fencing
//	token, a sensitivity tier, the caller principal and the issuer.
//
// Inputs: an Envelope built by the controller, or wire bytes for Decode.
// Outputs: the Envelope, Signed and helper value types.
// Constraints: pkg/envelope imports only pkg/provider, pkg/cascade and the
//
//	standard library (Art.10.2); there is exactly one envelope type in the
//	tree. Field tags are the wire and canonical names and are frozen.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// Version is the only envelope schema version this package issues and
// accepts.
const Version = 1

// MaxEncodedBytes bounds the wire size Decode accepts.
const MaxEncodedBytes = 64 << 10

// domainSeparator prefixes every canonical encoding, so a signature over an
// envelope can never be replayed as a signature over any other message the
// controller key signs.
const domainSeparator = "cascade.scope-envelope.v1\n"

// List limits Decode enforces, and Canonical enforces for issued envelopes.
const (
	maxRoots = 64
	maxNames = 256 // tools, commands and egress classes
	maxRefs  = 32  // grants and extensions
)

// Envelope is the scope one dispatch may act within. Every list is sorted
// and deduplicated in canonical form; Narrow decides whether a child
// envelope stays inside its parent.
type Envelope struct {
	// Version is the schema version; always Version.
	Version int `json:"v"`
	// ID names this envelope.
	ID string `json:"id"`
	// ParentDigest is Digest(parent) for a narrowed envelope, empty for a root.
	ParentDigest string `json:"parent,omitempty"`
	// Issuer is "ed25519:" + hex(sha256(controller public key)).
	Issuer string `json:"iss"`
	// Principal is the caller-principal ID (contract:caller-principal),
	// never a token.
	Principal string `json:"principal"`
	// Roots are absolute, clean, slash-separated filesystem roots.
	Roots []string `json:"roots"`
	// Tools are the allowed tool names (exact strings; "*" is refused in
	// tools, commands and egress).
	Tools []string `json:"tools"`
	// Commands are the allowed command names (exact strings).
	Commands []string `json:"cmds"`
	// Egress are the allowed egress classes.
	Egress []string `json:"egress"`
	// Grants reference short-lived credential grants by ID and verb.
	Grants []GrantRef `json:"grants"`
	// Budget bounds wall clock, tokens, cost and processes.
	Budget Budget `json:"budget"`
	// Ext carries typed extensions, one per registered kind.
	Ext []Extension `json:"ext,omitempty"`
	// Fence is the fencing token of the dispatch this envelope scopes.
	Fence Fence `json:"fence"`
	// Sensitivity is the data tier (contract:sensitivity-tier).
	Sensitivity provider.SensitivityTier `json:"sens"`
	// IssuedAt is the start of the validity window.
	IssuedAt time.Time `json:"iat"`
	// ExpiresAt is the end of the validity window (exclusive).
	ExpiresAt time.Time `json:"exp"`
}

// GrantRef names one short-lived credential grant the holder may redeem.
type GrantRef struct {
	// ID is the grant identifier.
	ID string `json:"id"`
	// Verb is the action the grant allows.
	Verb string `json:"verb"`
	// ExpiresAt is the grant's own expiry.
	ExpiresAt time.Time `json:"exp"`
}

// Budget is the resource budget. A zero component grants none of it.
type Budget struct {
	// WallClock is the wall-clock allowance.
	WallClock time.Duration `json:"wall"`
	// Tokens is the model-token allowance.
	Tokens int64 `json:"tok"`
	// CostMicros is the spend allowance in millionths of the billing unit.
	CostMicros int64 `json:"cost"`
	// Procs is the concurrent-process allowance.
	Procs int `json:"procs"`
}

// Fence is the fencing token that binds an envelope to one dispatch attempt.
type Fence struct {
	// DispatchID names the dispatch.
	DispatchID string `json:"dispatch"`
	// Attempt is the dispatch attempt number.
	Attempt uint64 `json:"attempt"`
	// LeaseEpoch is the lease epoch the attempt holds.
	LeaseEpoch int64 `json:"epoch"`
}

// Extension is one typed budget extension; Kind selects the registered
// ExtensionNarrower that compares a parent's and a child's Data.
type Extension struct {
	// Kind is the registered extension kind.
	Kind string `json:"kind"`
	// Data is the kind-specific JSON payload.
	Data json.RawMessage `json:"data"`
}

// Signed is an envelope with the controller's ed25519 signature over
// Canonical(Env).
type Signed struct {
	// Env is the signed envelope.
	Env Envelope `json:"env"`
	// Sig is the ed25519 signature.
	Sig []byte `json:"sig"`
}
