package memory

// Purpose: the memory.soul.* JSON-RPC namespace — the typed params and
//   results the daemon decodes untrusted input into, the handler that
//   serves show/edit/export over a SoulStore, and the Register call the
//   composition root makes so the namespace is reachable from a running
//   daemon rather than merely built.
// Inputs: raw JSON params from an untrusted peer; a SoulStore.
// Outputs: typed results marshalled into the JSON-RPC response, or a
//   pkg/cascade taxonomy error carrying the Kind that classifies it.
// Constraints: params decode into concrete structs, never interface{};
//   every refusal is a taxonomy error; the export result is the
//   SoulExport envelope and nothing wrapped around it, so what crosses
//   the socket is exactly what the export contract says it is.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
)

// The three method names of the memory.soul.* namespace. They are
// constants because the daemon registers them and the CLI calls them by
// the same name; a literal typed twice is a namespace that half-exists.
const (
	// MethodSoulShow returns the current document and its version.
	MethodSoulShow = "memory.soul.show"
	// MethodSoulEdit applies a new document through route (a).
	MethodSoulEdit = "memory.soul.edit"
	// MethodSoulExport returns the document plus the whole audit log.
	MethodSoulExport = "memory.soul.export"
)

// SoulShowParams is memory.soul.show's input. It has no fields: the SOUL
// is a single document and there is nothing to select. It exists as a
// named type anyway so the method's shape is declared rather than implied,
// and so a future field does not change the method's signature.
type SoulShowParams struct{}

// SoulShowResult is memory.soul.show's output.
type SoulShowResult struct {
	// Body and Schema are the document.
	Body   string `json:"body"`
	Schema string `json:"schema"`
	// Version is the current version.
	Version int `json:"version"`
	// Diverged reports an unresolved conflict between the file and the
	// store. It is reported rather than hidden: a caller reading a
	// possibly-stale identity document must be told so.
	Diverged bool `json:"diverged"`
}

// SoulEditParams is memory.soul.edit's input.
type SoulEditParams struct {
	// Body is the new document text. Required.
	Body string `json:"body"`
	// Schema names the document's shape. Empty takes the default.
	Schema string `json:"schema,omitempty"`
}

// SoulEditResult is memory.soul.edit's output: the version the write
// produced, which is what proves to the caller that the edit was recorded
// rather than merely accepted.
type SoulEditResult struct {
	// Version is the version after the write.
	Version int `json:"version"`
}

// SoulExportParams is memory.soul.export's input, empty for the same
// reason SoulShowParams is.
type SoulExportParams struct{}

// SoulHandler serves the memory.soul.* namespace over a SoulStore.
//
// It is a separate handler from the record-store Handler beside it because
// the two guard different things: that one moves memory records, this one
// moves the system's model of the user. Keeping them apart means a change
// to the record surface cannot widen this one by accident.
type SoulHandler struct {
	store SoulStore
}

// NewSoulHandler returns a handler serving store. store is required; a nil
// one would turn every call into a panic at the far end of an RPC, which
// is a crash reported to the user as a hang.
func NewSoulHandler(store SoulStore) *SoulHandler {
	return &SoulHandler{store: store}
}

// Register binds all three memory.soul.* methods on r. This is the whole
// of the composition-root wiring: without this call the handler is built,
// tested and unreachable from a running daemon.
func (h *SoulHandler) Register(r *rpc.Registry) {
	r.Register(MethodSoulShow, h.Show)
	r.Register(MethodSoulEdit, h.Edit)
	r.Register(MethodSoulExport, h.Export)
}

// Compile-time proof that every method still satisfies the router's
// handler signature, so a drifting signature fails the build here rather
// than at the composition root.
var (
	_ rpc.HandlerFunc = (*SoulHandler)(nil).Show
	_ rpc.HandlerFunc = (*SoulHandler)(nil).Edit
	_ rpc.HandlerFunc = (*SoulHandler)(nil).Export
)

// Show serves memory.soul.show. Reading runs the route-(b) reconcile as a
// side effect, so simply looking at the SOUL is what adopts an edit the
// user made in their own editor.
func (h *SoulHandler) Show(ctx context.Context, params json.RawMessage) (any, error) {
	var p SoulShowParams
	if err := decodeParams(MethodSoulShow, params, &p); err != nil {
		return nil, err
	}
	view, err := h.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	return SoulShowResult{
		Body:     view.Document.Body,
		Schema:   view.Document.Schema,
		Version:  view.Version,
		Diverged: view.Diverged,
	}, nil
}

// Edit serves memory.soul.edit through route (a).
func (h *SoulHandler) Edit(ctx context.Context, params json.RawMessage) (any, error) {
	var p SoulEditParams
	if err := decodeParams(MethodSoulEdit, params, &p); err != nil {
		return nil, err
	}
	// The conversion, rather than a field-by-field literal, is deliberate:
	// it makes the wire type and the document type provably the same
	// shape, so a field added to SoulDocument fails to compile here until
	// someone decides whether a peer may set it.
	view, err := h.store.Edit(ctx, SoulDocument(p))
	if err != nil {
		return nil, err
	}
	return SoulEditResult{Version: view.Version}, nil
}

// Export serves memory.soul.export.
//
// The result is the SoulExport envelope itself, with nothing wrapped
// around it and nothing added to it, so the bytes a caller writes to a
// file are the bytes the export contract describes.
func (h *SoulHandler) Export(ctx context.Context, params json.RawMessage) (any, error) {
	var p SoulExportParams
	if err := decodeParams(MethodSoulExport, params, &p); err != nil {
		return nil, err
	}
	return h.store.Export(ctx)
}
