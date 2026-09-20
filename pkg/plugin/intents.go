package plugin

// Purpose: the public Resolver contract and its input/output types — the
//   lookup layer between an unresolved intent string (e.g. "github:push")
//   and the plugin(s) that can satisfy it — plus VerifiedIndex, the
//   witness proving a registry index really passed signature verification
//   before any candidate is drawn from it. This file declares the SDK
//   surface only; internal/plugins/resolver owns the ranking algorithm
//   (Resolve is a pure function, so pkg/ can own the shape without owning
//   behavior that would need internal/ access).
// Inputs: raw registry-index bytes plus the real Ed25519Verifier, for the
//   one VerifiedIndex constructor; nothing else at this layer.
// Outputs: a *VerifiedIndex, or the verifier's own typed *cascade.Error.
// Constraints: pkg/plugin never imports internal/ (Art.10.2, enforced by
//
//	the pkg-no-internal depguard rule and internal/build's boundary gate);
//	no bare fmt.Errorf/errors.New (boundary lint — the only error
//	constructors here are cascade.Error composite literals and
//	AmbiguousIntent.Error's fmt.Sprintf, neither of which the lint
//	flags).
//
// SPORT: pkg/plugin intents (ADD) — P1-E24-W5-S50-T3.

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// InstalledPlugin is one entry in a ManifestSet snapshot: an installed
// plugin's parsed manifest plus whether the host currently has it enabled.
// internal/plugins.PluginMetadata (the host's own installed-plugin
// bookkeeping record, R-14.100's reserved metadata slot) is the natural
// producer of this snapshot in a real caller, but this package cannot
// depend on internal/plugins directly (pkg/ never imports internal/), so
// InstalledPlugin is a small, self-contained projection carrying only what
// Resolve's installed-first step actually reads.
type InstalledPlugin struct {
	// Manifest is the plugin's parsed cascade.plugin/v2 manifest.
	Manifest Manifest
	// Enabled reports whether the host currently has this plugin active.
	// A disabled plugin is never considered for installed-first
	// resolution, regardless of what intents its manifest declares.
	Enabled bool
}

// ManifestSet is the installed-plugin snapshot Resolve's installed-first
// lookup step scans (06-FORGE-SPEC.md step 1). It is caller-supplied and
// read-only to Resolve: Resolve takes no lock, performs no I/O against it,
// and never mutates it.
type ManifestSet []InstalledPlugin

// CandidateSource identifies which resolution step produced a Candidate.
// It is the only ordering-adjacent enum this package exports: a caller
// legitimately needs to know "already installed" from "would have to be
// fetched from the registry" to render a proposal, whereas the ranking
// tier a registry candidate matched at is an implementation detail of
// internal/plugins/resolver and is deliberately not published here.
type CandidateSource string

const (
	// CandidateSourceInstalled marks a Candidate found by the
	// installed-first scan (step 1).
	CandidateSourceInstalled CandidateSource = "installed"
	// CandidateSourceRegistry marks a Candidate found by the registry
	// lookup (step 2).
	CandidateSourceRegistry CandidateSource = "registry"
)

// Candidate is one plugin Resolve considers as satisfying an intent.
// Exactly one of Manifest or RegistryEntry is meaningfully populated,
// selected by Source: an installed-first Candidate carries the plugin's
// full Manifest (it was already loaded); a registry-sourced Candidate
// carries the RegistryIndexEntry the ranking matched against, since the
// registry index never ships a full Manifest for an uninstalled plugin.
type Candidate struct {
	// PluginID is the candidate plugin's manifest/registry id.
	PluginID string
	// Name is the plugin's human display name.
	Name string
	// Source reports which resolution step produced this Candidate.
	Source CandidateSource
	// Manifest is populated when Source == CandidateSourceInstalled;
	// the zero Manifest otherwise.
	Manifest Manifest
	// RegistryEntry is populated when Source == CandidateSourceRegistry;
	// the zero RegistryIndexEntry otherwise.
	RegistryEntry RegistryIndexEntry
}

// VerifiedIndex is proof that a registry index document was verified, not
// merely asserted to be verified. Its one field is unexported and its one
// constructor is NewVerifiedIndex, which returns a value only after the
// real production verifier has accepted the raw bytes: there is no
// composite literal, no zero value, and no setter with which a caller can
// mint one around the signature check. Resolve takes *VerifiedIndex for
// exactly that reason — a hand-built RegistryIndex (a forged signature
// string, an unsupported schema_version, an entry claiming a "deploy-prod"
// tag) cannot be spelled as a VerifiedIndex at all, so it can never reach
// the ranking step.
//
// What the witness proves: the index bytes carry a valid Ed25519 signature
// over their own schema_version+entries payload under the public key the
// caller supplied, and the document declares the one supported schema
// version. What it cannot prove: that the public key is the registry's.
// Key provenance belongs to the composition root, which reads it from
// [registry].public_key (08-INIT-CONFIG-SPEC §3), never from the index
// itself.
//
// A nil *VerifiedIndex is the "no verified index available" case, refused
// with ErrUnverifiedIndex, so absence and presence are distinguishable
// without a second boolean.
type VerifiedIndex struct {
	index RegistryIndex
}

// NewVerifiedIndex verifies data — the raw bytes of a registry index
// document, exactly as returned by RegistryFetcher.FetchIndex or read back
// out of a RegistryCache entry — through verifier, and returns the witness
// on success.
//
// The parameter is the concrete Ed25519Verifier rather than the
// RegistryVerifier interface on purpose: an interface parameter would let
// any caller, or any test, pass a verifier that succeeds unconditionally,
// which is precisely the structural hole this type exists to close.
//
// On failure the returned witness is nil and the error is the verifier's
// own typed *cascade.Error (KindIntegrity for a signature problem,
// KindInvalidInput for a malformed or unsupported document) — never a
// partially trusted value.
func NewVerifiedIndex(ctx context.Context, verifier Ed25519Verifier, data []byte) (*VerifiedIndex, error) {
	idx, err := verifier.VerifyIndex(ctx, data)
	if err != nil {
		return nil, err
	}
	return &VerifiedIndex{index: idx}, nil
}

// Entries returns the verified index's plugin entries. The returned slice
// is a fresh copy, so a caller ranking over it cannot mutate the witness;
// the copy is shallow, so each entry's own Tags/Versions slices are still
// shared and must be treated as read-only. A nil receiver returns nil,
// which keeps a caller that skipped its nil check from panicking on a path
// that should already have returned ErrUnverifiedIndex.
func (v *VerifiedIndex) Entries() []RegistryIndexEntry {
	if v == nil {
		return nil
	}
	out := make([]RegistryIndexEntry, len(v.index.Entries))
	copy(out, v.index.Entries)
	return out
}

// AmbiguousIntent is returned when a resolution step produces more than
// one candidate at the strongest tier it found — installed-first (more
// than one enabled plugin declares the same intent) or registry lookup
// (more than one entry ties at the strongest ranking tier). The caller
// (the conversational install flow, P1-E24-W5-S50-T4) owns disambiguation
// UX. Resolve never silently drops a candidate to break a tie.
type AmbiguousIntent struct {
	// Intent is the original intent string that produced the ambiguity.
	Intent string
	// Candidates is the full ranked list, strongest first with a
	// deterministic tie-break: every candidate found at every tier, not
	// only the tied group, so nothing is dropped on the way to the
	// caller.
	Candidates []Candidate
	// Tied is how many leading entries of Candidates share the strongest
	// tier. The tied group is exactly Candidates[:Tied], and Tied is
	// always at least 2 — a single strongest candidate is not an
	// ambiguity. Resolve reports the count rather than leaving the caller
	// to recompute it, because the tier a candidate matched at is
	// deliberately not part of this package's surface.
	Tied int
}

// Error implements the error interface. The count it reports first is
// Tied, the size of the tied top group the message names — not
// len(Candidates), which also counts the weaker alternatives carried along
// for the caller's disambiguation UX, and which is reported separately.
func (e *AmbiguousIntent) Error() string {
	return fmt.Sprintf("plugin: ambiguous intent %q: %d candidate(s) at top rank, %d in all",
		e.Intent, e.Tied, len(e.Candidates))
}

var (
	// ErrUnverifiedIndex is returned when Resolve's fail-closed guard
	// (06-FORGE-SPEC.md §5.20) has no verified index to draw candidates
	// from: the *VerifiedIndex argument is nil. There is no second
	// "verified, but not really" case left to guard, because a
	// VerifiedIndex cannot exist without a successful verification — a nil
	// witness is the whole of the condition. It is returned only once the
	// installed-first step has found no unique hit; an installed-first
	// Candidate is returned without the index being consulted at all.
	//
	// COMPARE BY IDENTITY, not with errors.Is. (*cascade.Error).Is
	// compares Kind ONLY, and this sentinel shares cascade.KindIntegrity
	// with ErrSignatureInvalid and ErrChecksumMismatch
	// (registry_errors.go), so errors.Is(err, ErrUnverifiedIndex) also
	// reports true for a checksum mismatch. A caller that needs to know
	// it was THIS condition writes err == plugin.ErrUnverifiedIndex.
	ErrUnverifiedIndex = &cascade.Error{Kind: cascade.KindIntegrity, Msg: "plugin: no verified registry index"}

	// ErrIntentNotFound is returned when neither the installed-first scan
	// nor the registry lookup produces any candidate for the intent. It
	// shares cascade.KindNotFound with every other not-found error in the
	// taxonomy, so it is compared by identity for the same reason
	// ErrUnverifiedIndex documents.
	ErrIntentNotFound = &cascade.Error{Kind: cascade.KindNotFound, Msg: "plugin: no candidate satisfies intent"}

	// ErrIntentEmpty is returned when the intent string is empty or all
	// whitespace. That is a malformed request rather than a lookup miss,
	// so it carries cascade.KindInvalidInput and stays distinct from
	// ErrIntentNotFound (a well-formed intent that nothing provides).
	// Compared by identity, as above.
	ErrIntentEmpty = &cascade.Error{Kind: cascade.KindInvalidInput, Msg: "plugin: intent is empty"}
)

// Resolver maps an intent string to the plugin(s) able to satisfy it. A
// Resolver is a pure function of Resolve's arguments: no network I/O, no
// embedded clock, no side effects — every input is caller-supplied per
// call (Art.7's clock-injection principle applied to snapshot data: a
// Resolver never reads a live registry connection or a wall clock itself).
type Resolver interface {
	// Resolve maps intent to candidate plugins in three ordered steps
	// (06-FORGE-SPEC.md; P1-E24-W5-S50-T3). ctx carries cancellation
	// only: it is checked before any work begins and is not used for I/O,
	// of which Resolve performs none.
	//
	//  1. Installed-first: an exact, case-insensitive match against an
	//     enabled entry in installed whose Manifest.Provides.Intents
	//     names intent wins immediately, with index never consulted.
	//     More than one enabled installed match is an *AmbiguousIntent
	//     over just those installed candidates.
	//  2. Registry lookup: otherwise index's entries are ranked, and the
	//     ranked list is returned strongest-first. The tiers are
	//     internal/plugins/resolver's (exact intent tag, then tag
	//     prefix, then a name/description keyword hit); the tier itself
	//     is not exported.
	//  3. Ambiguity: more than one candidate tied at the strongest tier
	//     returns *AmbiguousIntent wrapping the full ranked list.
	//
	// On success the result is never empty and result[0] is the resolved
	// winner; any later element is a strictly weaker alternative offered
	// for display, never a second winner.
	//
	// Errors: ErrIntentEmpty for a blank or whitespace-only intent;
	// ErrUnverifiedIndex when the installed-first step found no unique
	// hit and index is nil (fail-closed, 06-FORGE-SPEC.md §5.20 — never a
	// partial or unverified result); ErrIntentNotFound when nothing in
	// installed or index matches. Every error path returns a nil
	// candidate slice.
	Resolve(ctx context.Context, installed ManifestSet, index *VerifiedIndex, intent string) ([]Candidate, error)
}
