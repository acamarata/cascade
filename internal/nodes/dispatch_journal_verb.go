package nodes

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the journal-stream verb, mounted separately from the
//
//	claim/report pair because its dependency is different in kind — it needs
//	the controller's journal store, which the composition root opens, while
//	the other two need only the rendezvous. Split from dispatch_handler.go
//	under the 300-line cap; the cut is at the dependency boundary rather
//	than at an arbitrary line.
//
// SPORT: internal/nodes:dispatch-journal-verb (ADD) — P1-E17-W4-S37-T3 split.

// DispatchJournalMethod is how a node streams journal records back.
const DispatchJournalMethod = "node.dispatch.journal"

// RegisterDispatchJournalHandler mounts the journal-stream verb over deps.
//
// It is separate from RegisterDispatchNodeHandlers because its dependency
// is different in kind: claim and report need only the rendezvous, while
// this needs the controller's journal store, which the composition root
// opens. Mounting it separately means a daemon without a journal store
// still serves the other two rather than failing to register any of them.
func RegisterDispatchJournalHandler(registry *rpc.Registry, deps JournalStreamDeps) {
	registry.Register(DispatchJournalMethod, func(ctx context.Context, params json.RawMessage) (any, error) {
		var rec JournalRecord
		if err := decodeParams(params, &rec, DispatchJournalMethod); err != nil {
			return nil, err
		}
		if err := StreamJournalRecord(ctx, deps, rec); err != nil {
			return nil, err
		}
		return map[string]bool{"appended": true}, nil
	})
}

// decodeParams decodes one call's params, naming the verb in any failure.
//
// JSON null counts as ABSENT, not as a value. A `"params": null` frame is
// four bytes, so a length check alone lets it through, and unmarshalling
// null into a struct leaves the zero value untouched — the verb then runs
// against a claim from node "" or a report for dispatch "". Each verb's own
// validation happens to refuse those today, which is precisely why this
// guard is worth having: it refuses at the boundary, by name, instead of
// relying on every downstream rule to keep noticing.
func decodeParams(params json.RawMessage, into any, method string) error {
	if len(params) == 0 || string(bytes.TrimSpace(params)) == "null" {
		return cascade.Newf(cascade.KindInvalidInput, "nodes: %s requires params", method)
	}
	if err := json.Unmarshal(params, into); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "nodes: %s: decode request", method)
	}
	return nil
}
