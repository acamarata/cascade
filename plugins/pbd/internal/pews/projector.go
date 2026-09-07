// Package pews (projector.go): Purpose: project the canonical PEWS
//
//	phase/ticket file tree into database-backed PBD state
//	(pkg/provider.Store), and notify subscribers so plugins/pbd/pbd.go
//	can bridge the change onto the repository's existing HTTP/1.1 SSE
//	transport.
//
// Inputs: a tree root and phase (read via this package's Store, store.go),
//
//	a pkg/provider.Store to project into, and an optional EventPublisher
//	notified after a successful write.
//
// Outputs: a ProjectionResult, or a *cascade.Error. Files on disk are the
//
//	source of truth: Project is idempotent (an unchanged tree writes
//	nothing) and convergent (a row for every ticket the tree currently
//	has, none for one it no longer has).
//
// Constraints: plugins/** may import pkg/** only, never internal/**
//
//	(Art.10.2, plugins-providers-boundary) — Clock and EventPublisher are
//	declared locally, duck-typed, exactly as internal/storage/domains.go
//	declares its own Clock. No bare time.Now.
//
// SPORT: plugins.pbd.internal.pews.Projector/ADD (P1-E14-W3-S29-T1).
package pews

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DefaultProjectionNamespace is the pkg/provider.Store namespace this
// projector writes into: a plugin-owned namespace, not one of
// internal/storage/domains.go's eleven cascade.db domains. R-14.100
// already reserves the "plugin.<name>" namespace shape for plugin-owned
// storage, so no DomainID amendment is needed here.
const DefaultProjectionNamespace = "plugin.pbd"

// rowKeyPrefix namespaces this projector's keys within
// DefaultProjectionNamespace, leaving room for other cascade-pbd state
// (lifecycle, dispatch, drafts — N/S-29.T2-T3, N/S-30) in the same
// namespace without key collision.
const rowKeyPrefix = "pews/ticket/"

// Clock abstracts time.Now (forbidigo bans a bare read). Declared locally
// for the same reason EventPublisher is: this package may never import
// internal/runtime.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// EventPublisher is the local, duck-typed seam onto the repository's
// existing HTTP/1.1 SSE boundary (internal/rpc.SSEHandler, D/S-06.T4):
// Publish delivers one projection-update notification for phase. A nil
// EventPublisher disables notification: a documented degrade, not a
// stub — Project's storage write is unaffected. plugins/pbd.Bus (pbd.go)
// is the shipped implementation.
type EventPublisher interface {
	// Publish delivers payload for phase. A non-nil error aborts the
	// Project call that produced it.
	Publish(ctx context.Context, phase string, payload []byte) error
}

// Row is one ticket's projected record: fields copied out of a decoded
// Ticket (schema.go) for query-time access without reparsing YAML. Field
// order is fixed so JSON encoding is byte-for-byte stable across runs —
// required for Project's unchanged-row skip.
type Row struct {
	CanonicalID string `json:"canonical_id"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	Weight      string `json:"weight"`
	ModelClass  string `json:"model_class"`
	Phase       string `json:"phase"`
}

// ProjectionResult summarizes one Project call.
type ProjectionResult struct {
	Phase string
	// Upserted is how many rows this call wrote (new or changed).
	Upserted int
	// Deleted is how many rows this call removed because their ticket
	// file is no longer on disk.
	Deleted int
	// Converged is true when the tree already matched stored state:
	// nothing was written, nothing was deleted, nothing was published.
	Converged bool
}

// ProjectorOptions configures NewProjector.
type ProjectorOptions struct {
	// Store is the backing pkg/provider.Store. Required.
	Store provider.Store
	// Clock is required so construction is deterministic-by-injection,
	// matching this package's other constructors; reserved for a future
	// lifecycle column.
	Clock Clock
	// Publisher, if non-nil, is notified after a successful write.
	Publisher EventPublisher
	// Namespace overrides DefaultProjectionNamespace. Empty means default.
	Namespace string
}

// Projector projects a PEWS tree (store.go's Store) into a
// pkg/provider.Store and notifies an optional EventPublisher of the
// result.
type Projector struct {
	store     provider.Store
	clock     Clock
	publisher EventPublisher
	namespace string
}

// NewProjector validates opts and constructs a Projector.
func NewProjector(opts ProjectorOptions) (*Projector, error) {
	if opts.Store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "pews: projector: Store is required")
	}
	if opts.Clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "pews: projector: Clock is required")
	}
	ns := opts.Namespace
	if ns == "" {
		ns = DefaultProjectionNamespace
	}
	return &Projector{store: opts.Store, clock: opts.Clock, publisher: opts.Publisher, namespace: ns}, nil
}

// rowWrite is one pending Put this Project call decided to make.
type rowWrite struct {
	key   string
	value []byte
	id    string
}

// Project reads the PEWS tree at root/phase, diffs it against the
// projector's current stored rows, and atomically writes the difference:
// every changed or new ticket is upserted, and every stored row whose
// ticket file is no longer present is deleted. Running Project twice
// over an unchanged tree performs zero writes and reports Converged;
// running it again after an external file change reaches exactly the
// state a from-scratch Project over the changed tree would.
func (p *Projector) Project(ctx context.Context, root, phase string) (ProjectionResult, error) {
	tree, err := NewStore(root, phase).Load()
	if err != nil {
		return ProjectionResult{}, err
	}

	existing, err := p.scanKeys(ctx)
	if err != nil {
		return ProjectionResult{}, err
	}

	upserts, wantKeys, err := p.planUpserts(ctx, tree)
	if err != nil {
		return ProjectionResult{}, err
	}
	orphans := planOrphans(existing, wantKeys)

	if len(upserts) == 0 && len(orphans) == 0 {
		return ProjectionResult{Phase: phase, Converged: true}, nil
	}

	if err := p.commit(ctx, upserts, orphans); err != nil {
		return ProjectionResult{}, err
	}
	if p.publisher != nil {
		if perr := p.notify(ctx, phase, upserts, orphans); perr != nil {
			return ProjectionResult{}, perr
		}
	}
	return ProjectionResult{Phase: phase, Upserted: len(upserts), Deleted: len(orphans)}, nil
}

// scanKeys returns every key currently stored under this projector's
// namespace and key prefix.
func (p *Projector) scanKeys(ctx context.Context) (map[string]struct{}, error) {
	it, err := p.store.Scan(ctx, p.namespace, rowKeyPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "pews: projector: scan existing rows")
	}
	defer func() { _ = it.Close() }()
	out := make(map[string]struct{})
	for it.Next(ctx) {
		out[it.Key()] = struct{}{}
	}
	if err := it.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "pews: projector: scan existing rows")
	}
	return out, nil
}

// planUpserts builds the ordered, deterministic set of rows to write:
// every ticket whose serialized Row differs from (or is absent from) the
// currently stored value. wantKeys is every key the tree implies, used by
// planOrphans to find what the tree no longer implies.
func (p *Projector) planUpserts(ctx context.Context, tree *Tree) ([]rowWrite, map[string]struct{}, error) {
	wantKeys := make(map[string]struct{}, len(tree.Tickets))
	var upserts []rowWrite
	for _, tr := range tree.Tickets {
		key := rowKeyPrefix + tr.CanonicalID
		wantKeys[key] = struct{}{}
		value, err := json.Marshal(rowFromRecord(tree.Phase, tr))
		if err != nil {
			return nil, nil, cascade.Wrap(cascade.KindInternal, err, "pews: projector: encode row")
		}
		cur, gerr := p.store.Get(ctx, p.namespace, key)
		if gerr != nil && !cascade.HasKind(gerr, cascade.KindNotFound) {
			return nil, nil, cascade.Wrap(cascade.KindUnavailable, gerr, "pews: projector: read existing row")
		}
		if gerr == nil && bytes.Equal(cur, value) {
			continue
		}
		upserts = append(upserts, rowWrite{key: key, value: value, id: tr.CanonicalID})
	}
	sort.Slice(upserts, func(i, j int) bool { return upserts[i].key < upserts[j].key })
	return upserts, wantKeys, nil
}

// planOrphans returns, sorted, every stored key absent from wantKeys.
func planOrphans(existing, wantKeys map[string]struct{}) []string {
	var orphans []string
	for k := range existing {
		if _, ok := wantKeys[k]; !ok {
			orphans = append(orphans, k)
		}
	}
	sort.Strings(orphans)
	return orphans
}

// commit writes every upsert and deletes every orphan inside one
// transaction, so a partial failure never leaves storage between the old
// and new tree states.
func (p *Projector) commit(ctx context.Context, upserts []rowWrite, orphans []string) error {
	err := p.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		for _, u := range upserts {
			if err := tx.Put(ctx, p.namespace, u.key, u.value); err != nil {
				return err
			}
		}
		for _, key := range orphans {
			if err := tx.Delete(ctx, p.namespace, key); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "pews: projector: commit")
	}
	return nil
}

// notifyPayload is the JSON envelope Publish delivers.
type notifyPayload struct {
	Phase    string   `json:"phase"`
	Upserted []string `json:"upserted"`
	Deleted  []string `json:"deleted"`
}

// notify serializes and publishes one projection-update event.
func (p *Projector) notify(ctx context.Context, phase string, upserts []rowWrite, orphans []string) error {
	ids := make([]string, 0, len(upserts))
	for _, u := range upserts {
		ids = append(ids, u.id)
	}
	deletedIDs := make([]string, len(orphans))
	for i, key := range orphans {
		deletedIDs[i] = key[len(rowKeyPrefix):]
	}
	payload, err := json.Marshal(notifyPayload{Phase: phase, Upserted: ids, Deleted: deletedIDs})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "pews: projector: encode notification")
	}
	if err := p.publisher.Publish(ctx, phase, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "pews: projector: publish")
	}
	return nil
}

// rowFromRecord copies tr's projected fields into a Row.
func rowFromRecord(phase string, tr TicketRecord) Row {
	return Row{
		CanonicalID: tr.CanonicalID,
		ID:          tr.Ticket.ID,
		Title:       tr.Ticket.Title,
		Weight:      string(tr.Ticket.Weight),
		ModelClass:  string(tr.Ticket.ModelClass),
		Phase:       phase,
	}
}
