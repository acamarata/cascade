package memory

// Purpose: the memory.* JSON-RPC namespace: the typed params/results the
//   daemon decodes external input into, the Handler that serves
//   memory.remember and memory.forget over the T1 file store, and the
//   Register call the daemon composition root makes so the namespace is
//   reachable from a running daemon rather than merely built.
// Inputs: raw JSON params from an untrusted peer; a MemoryStore; a Clock.
// Outputs: typed result values marshalled into the JSON-RPC response, or
//   a pkg/cascade taxonomy error carrying the Kind that classifies the
//   refusal.
// Constraints: params decode into concrete structs, never interface{};
//   every refusal is a taxonomy error, never a bare string; no clock read
//   outside the injected Clock; the query half (memory.recall,
//   memory.list) lives in rpc_query.go to stay inside the 300-line cap.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The four method names of the memory.* namespace. They are constants
// because the daemon registers them and the CLI calls them by the same
// name; a literal typed twice is a namespace that half-exists.
const (
	// MethodRemember writes a record and returns its canonical address.
	MethodRemember = "memory.remember"
	// MethodRecall scans the store for records matching a query.
	MethodRecall = "memory.recall"
	// MethodForget tombstones one record by canonical address.
	MethodForget = "memory.forget"
	// MethodList pages the store in canonical-address order.
	MethodList = "memory.list"
)

// DefaultScopeRef is the scope every record written through this surface
// is filed under.
//
// MemoryEntry.ScopeRef is required and, per R-16.7, syntactic-only: the
// scope graph that would give a reference meaning does not exist yet. The
// four verbs in this namespace carry no scope input, so rather than
// inventing a per-call scope the store cannot check, every record written
// here lands in one named scope. When the scope graph arrives, records
// written by this build are identifiable by exactly this value instead of
// by a blank field nobody can attribute.
const DefaultScopeRef = "local"

// defaultConfidence is the confidence a record remembered through this
// surface is stored with. A caller that reaches for `cascade memory
// remember` is stating something directly rather than inferring it, which
// is the top of the [0,1] range; the promotion ladder is what assigns
// anything lower, and it is not this surface.
const defaultConfidence = 1.0

// nameHashPrefixLen is how many hex characters of the body's BLAKE3 digest
// become the record name when the caller supplies none. Sixteen hex
// characters is 64 bits: collision-safe far past any plausible store size,
// short enough to type, and deterministic, so remembering the same body
// twice addresses the same record instead of littering the store with
// copies.
const nameHashPrefixLen = 16

// RememberParams is memory.remember's input.
type RememberParams struct {
	// Content is the record body. Required.
	Content string `json:"content"`
	// Type is the MemoryKind spelling (user|feedback|project|reference).
	// Empty defaults to "project".
	Type string `json:"type,omitempty"`
	// Name is the record name within its kind. Empty derives the name
	// from the body hash.
	Name string `json:"name,omitempty"`
	// Provenance is the producing session's reference. May be empty.
	Provenance string `json:"provenance,omitempty"`
}

// RememberResult is memory.remember's output.
type RememberResult struct {
	// ID is the canonical "<kind>/<name>" address of the written record.
	ID string `json:"id"`
}

// Handler serves the memory.* namespace over a MemoryStore.
//
// # What the methods promise
//
//   - memory.remember validates the kind, derives the record name from the
//     body hash when none is given, and Writes through the store. It is
//     idempotent for an identical record, because the store is.
//   - memory.recall scans the files (rpc_query.go). It reads no index: the
//     indexed projection is derived state and a later ticket's query path.
//   - memory.forget tombstones exactly one record. See ForgetResult and
//     Forget's own doc comment for precisely what that removes and leaves.
//   - memory.list pages live records in canonical-address order.
//
// # Error contracts
//
// Every refusal is a pkg/cascade taxonomy error, so the RPC layer maps it
// to a wire code without this package knowing the wire at all:
// KindInvalidInput for malformed params, an unknown kind or a malformed
// address; KindNotFound for an address with no live record;
// KindUnsupported for a record this build cannot read; KindIntegrity for a
// damaged one; KindUnavailable when the file system fails.
type Handler struct {
	store MemoryStore
	clock Clock
}

// NewHandler returns a Handler serving store, taking its timestamps from
// clk. Both are required; a nil store would turn every call into a panic
// at the far end of an RPC, which is a crash reported as a hang.
func NewHandler(store MemoryStore, clk Clock) *Handler {
	return &Handler{store: store, clock: clk}
}

// Register binds all four memory.* methods on r. This is the whole of the
// composition-root wiring: without this call the handler is built, tested
// and unreachable from a running daemon, which is the failure mode this
// repository's test-only gate exists to make visible.
func (h *Handler) Register(r *rpc.Registry) {
	r.Register(MethodRemember, h.Remember)
	r.Register(MethodRecall, h.Recall)
	r.Register(MethodForget, h.Forget)
	r.Register(MethodList, h.List)
}

// Compile-time proof that every method still satisfies the router's
// handler signature, so a drifting signature fails the build here rather
// than at the composition root.
var (
	_ rpc.HandlerFunc = (*Handler)(nil).Remember
	_ rpc.HandlerFunc = (*Handler)(nil).Recall
	_ rpc.HandlerFunc = (*Handler)(nil).Forget
	_ rpc.HandlerFunc = (*Handler)(nil).List
)

// Remember serves memory.remember.
func (h *Handler) Remember(ctx context.Context, params json.RawMessage) (any, error) {
	var p RememberParams
	if err := decodeParams(MethodRemember, params, &p); err != nil {
		return nil, err
	}
	entry, err := h.entryFrom(p)
	if err != nil {
		return nil, err
	}
	if err := h.store.Write(ctx, entry); err != nil {
		return nil, err
	}
	return RememberResult{ID: recordID(entry.Kind, entry.Name)}, nil
}

// entryFrom turns validated params into the record to write. Split from
// Remember so both stay inside the 50-line function cap.
func (h *Handler) entryFrom(p RememberParams) (MemoryEntry, error) {
	if strings.TrimSpace(p.Content) == "" {
		return MemoryEntry{}, cascade.New(cascade.KindInvalidInput,
			"memory.remember: content is empty")
	}
	kind := KindProject
	if p.Type != "" {
		parsed, err := ParseKind(p.Type)
		if err != nil {
			return MemoryEntry{}, err
		}
		kind = parsed
	}
	name := p.Name
	if name == "" {
		name = HashBody(p.Content)[:nameHashPrefixLen]
	}
	if err := ValidateName(name); err != nil {
		return MemoryEntry{}, err
	}
	return MemoryEntry{
		Name: name,
		Kind: kind,
		Body: p.Content,
		Provenance: Provenance{
			Origin:    OriginSession,
			SessionID: p.Provenance,
		},
		ScopeRef:   DefaultScopeRef,
		Confidence: defaultConfidence,
	}, nil
}

// Address returns the canonical "<kind>/<name>" address of a record. It is
// the one identity this surface, the projection and the CLI all use, so it
// delegates to the projection's own recordID rather than re-deriving the
// same concatenation a second time.
func Address(kind MemoryKind, name string) string { return recordID(kind, name) }

// ParseAddress splits a canonical "<kind>/<name>" address, refusing
// anything that is not one. It fails closed: an address with no separator,
// an unknown kind, or a name that is not a legal path segment is refused
// rather than repaired into some nearby address the caller did not ask
// for.
func ParseAddress(id string) (MemoryKind, string, error) {
	kindPart, namePart, ok := strings.Cut(id, "/")
	if !ok {
		return "", "", cascade.Newf(cascade.KindInvalidInput,
			"memory address %q is not a <kind>/<name> pair", id)
	}
	kind, err := ParseKind(kindPart)
	if err != nil {
		return "", "", err
	}
	if err := ValidateName(namePart); err != nil {
		return "", "", err
	}
	return kind, namePart, nil
}

// decodeParams unmarshals raw params into dst. Absent params are decoded
// as an empty object rather than refused outright, so a method whose
// fields are all optional stays callable with no params member at all;
// every method still validates its own required fields afterwards.
//
// This is the external-input decoder for the whole namespace — every byte
// it sees comes from a peer — which is why FuzzMemoryRPCParams drives it
// rather than driving json.Unmarshal directly.
func decodeParams(method string, params json.RawMessage, dst any) error {
	trimmed := strings.TrimSpace(string(params))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err,
			"%s: malformed params", method)
	}
	return nil
}
