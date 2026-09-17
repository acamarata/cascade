package sync

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the `sync.*` JSON-RPC surface — what the CLI's
//
//	`sync` noun is a thin mirror of, and what the generated MCP subset
//	filters down from.
//
// FOUR METHODS, THREE OF THEM READS. status and conflicts.list answer
//
//	questions; run does work; conflicts.resolve is the only one that
//	discards data, and it is the only one gated.
//
// THE RESOLVE GATE IS NOT A PROMPT (06 §5.14). The resolution is chosen
//
//	by a parameter, never by asking. Discarding a server-primary decision
//	means overriding the authority the domain is defined by, so it routes
//	through the elevation gate — and a caller with no gate wired is
//	REFUSED rather than allowed through, because the failure mode of the
//	other choice is "the machine that could not check let it happen".
//
// AND IT IS NOT AN MCP TOOL. The generated MCP subset carries the two
//
//	read verbs. A model that could discard the server's copy of a config
//	record on its own reasoning is a surface nobody asked for.
//
// Inputs: the engine, and a peer tier for the eligibility questions.
// Outputs: typed results, or taxonomy errors.
// SPORT: internal/sync rpc surface (ADD) — P1-E17-W4-S38-T3.

// The four method names, mirroring 07 §sync's verbs.
const (
	MethodStatus           = "sync.status"
	MethodRun              = "sync.run"
	MethodConflictsList    = "sync.conflicts_list"
	MethodConflictsResolve = "sync.conflicts_resolve"
)

// ErrInvalidSyncParams is returned for params this package cannot decode.
var ErrInvalidSyncParams = cascade.New(cascade.KindInvalidInput, "sync: invalid request params")

// DomainStatus is one domain's line in the status report.
type DomainStatus struct {
	// Domain and Subkind name it.
	Domain  string `json:"domain"`
	Subkind string `json:"subkind"`
	// Class is its registered sync class.
	Class string `json:"class"`
	// Strategy is the merge it would use.
	Strategy string `json:"strategy"`
	// Eligible reports whether the peer tier in the request may sync it,
	// and Reason says why not. Both are reported because "this domain
	// does not sync here" and "this domain does not sync at all" are
	// different facts an operator acts on differently.
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
	// Position is the cursor this domain has reached, and PositionKnown
	// says whether it could be read. Zero is a real position — a domain
	// that has never synced is at zero — so the two facts are separate,
	// and a reader that collapsed them would print a number an operator
	// would believe.
	Position      uint64 `json:"position"`
	PositionKnown bool   `json:"position_known"`
}

// StatusResult is sync.status's answer.
type StatusResult struct {
	// PeerTier is the tier the eligibility column was computed for, so a
	// reader knows which question was answered.
	PeerTier string `json:"peer_tier"`
	// Domains is one line per registered domain, sorted.
	Domains []DomainStatus `json:"domains"`
	// OpenConflicts is how many journaled conflicts are unresolved.
	OpenConflicts int `json:"open_conflicts"`
}

// ConflictsResult is sync.conflicts_list's answer.
type ConflictsResult struct {
	Conflicts []Conflict `json:"conflicts"`
}

// ResolveResult reports one resolution.
type ResolveResult struct {
	// RecordID names what was resolved.
	RecordID string `json:"record_id"`
	// Keep is the side that was kept.
	Keep string `json:"keep"`
	// Elevated reports whether the resolution needed authorization. It is
	// in the result so an audit reader can see that the gate ran, not
	// only that the resolve succeeded.
	Elevated bool `json:"elevated"`
}

// The two sides a resolution may keep.
const (
	// KeepServer accepts the server-primary decision. Never elevated:
	// it is what the merge already did.
	KeepServer = "server"
	// KeepLocal DISCARDS the server's copy. Elevated, because it
	// overrides the authority a server-primary domain is defined by.
	KeepLocal = "local"
)

// Deps are the RPC surface's collaborators.
//
// Named Deps rather than SyncDeps: it is reached as sync.Deps, and the
// package name already says which subsystem's dependencies these are.
type Deps struct {
	// Engine holds the conflict journal and the cursors.
	Engine *Engine
	// PeerTier is the tier status answers eligibility for.
	PeerTier nodes.Tier
	// Gate authorizes an elevated resolution. Nil REFUSES rather than
	// allows: a machine that cannot check must not be the one that lets
	// a server-primary decision be discarded.
	Gate ElevationGate
	// Run performs one domain's sync. Nil makes sync.run report that it
	// is not wired, rather than reporting success having done nothing.
	Run func(ctx context.Context, domain storage.DomainID, subkind string) error
	// Config is the operator's `[sync]` section, which may NARROW a
	// domain's compiled-in class (never widen it). The zero value means
	// no overrides.
	//
	// It is here rather than read at the point of use because status and
	// run must answer with the SAME effective class: a report that said a
	// domain syncs while a run skipped it would be worse than either
	// answer alone.
	Config Config
}

// ElevationGate authorizes one elevated verb.
//
// Declared here rather than imported from internal/secrets so this
// package's RPC surface does not depend on the vault to answer a question
// about conflicts. The production gate satisfies it structurally.
type ElevationGate interface {
	Authorize(ctx context.Context, verb string) error
}

// ElevatedVerbResolve is the verb name the gate sees.
const ElevatedVerbResolve = "sync.conflicts.resolve.discard-server"

// RegisterHandlers binds the four methods into registry.
func RegisterHandlers(registry *rpc.Registry, deps Deps) {
	registry.Register(MethodStatus, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if _, err := decodeSyncParams[struct{}](raw); err != nil {
			return nil, err
		}
		return deps.Status(ctx)
	})
	registry.Register(MethodRun, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeSyncParams[runParams](raw)
		if err != nil {
			return nil, err
		}
		return deps.run(ctx, p)
	})
	registry.Register(MethodConflictsList, func(_ context.Context, raw json.RawMessage) (any, error) {
		if _, err := decodeSyncParams[struct{}](raw); err != nil {
			return nil, err
		}
		return deps.ConflictsList()
	})
	registry.Register(MethodConflictsResolve, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeSyncParams[resolveParams](raw)
		if err != nil {
			return nil, err
		}
		return deps.resolve(ctx, p)
	})
}

// ConflictsList answers sync.conflicts_list, and is the one place the
// journal is read, so the CLI and the RPC cannot answer differently.
func (d Deps) ConflictsList() (ConflictsResult, error) {
	if err := d.requireEngine(); err != nil {
		return ConflictsResult{}, err
	}
	return ConflictsResult{Conflicts: d.Engine.Conflicts().List()}, nil
}

// runParams selects one domain, or all of them.
type runParams struct {
	Domain  string `json:"domain,omitempty"`
	Subkind string `json:"subkind,omitempty"`
}

// resolveParams names one conflict and the side to keep.
type resolveParams struct {
	RecordID string `json:"record_id"`
	Keep     string `json:"keep"`
}

// decodeSyncParams decodes one request's params, refusing unknown fields.
//
// Unknown fields are an error rather than being ignored: a caller that
// sent `{"domian": "config"}` asked for one domain and would otherwise
// silently sync all of them.
func decodeSyncParams[T any](raw json.RawMessage) (T, error) {
	var p T
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSyncParams,
			"sync: decoding request params: %v", err)
	}
	return p, nil
}
