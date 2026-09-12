// Purpose: Engine ties domains.go, cursor.go, filter.go, chunk.go/
//   transfer.go and egress.go together into the composition-root-facing
//   surface (Art.10.2): this is what cmd/cascade wires as one service.
//   Merge strategies (S-38.T2) and the CLI/RPC surface (S-38.T3) are
//   genuinely absent here — Engine exposes only what this ticket built:
//   admitting and filtering records, moving the cursor, and sending an
//   admitted batch through the sync egress class.
// Inputs: a provider.Store (cursor/exclusion persistence) and Clock at
//   construction; an egress.Engine bound to the sync class at Send time.
// Outputs: SendBatch reports the cursor position it advanced to, or a
//   typed error.
// Constraints: SendBatch NEVER writes a byte to conn before both the
//   pre-serialization filter (filter.go) and the egress substitution
//   pass (egress.go's registered class) have run, in that order.
// SPORT: internal.sync.engine/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"context"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Engine is the sync engine core's composition surface. The zero value
// is not usable; build one with NewEngine.
type Engine struct {
	cursors *CursorStore
	egress  *egress.Engine
}

// NewEngine builds an Engine over store (cursor/exclusion persistence),
// clock, and an optional egress engine (nil is valid: a caller that only
// needs cursor/filter bookkeeping, e.g. a test, need not build a full
// egress.Engine — SendBatch reports KindUnavailable if it is called
// without one).
func NewEngine(store provider.Store, clock Clock, eg *egress.Engine) *Engine {
	return &Engine{cursors: NewCursorStore(store, clock), egress: eg}
}

// Cursors exposes the underlying CursorStore for a consumer (S-38.T2's
// merge strategies) that needs direct cursor/exclusion access beyond
// SendBatch's own bookkeeping.
func (e *Engine) Cursors() *CursorStore { return e.cursors }

// SendBatch filters recs (all must share one domain+subkind), journals
// every exclusion, advances the cursor past the whole batch (applied
// records go out on conn; excluded ones are journaled — R-21.223: the
// cursor never advances over a record neither applied nor excluded), runs
// the admitted set through the sync egress class, and sends it as a
// chunked stream on conn starting at fromSeq.
func (e *Engine) SendBatch(ctx context.Context, conn io.Writer, domain storage.DomainID, subkind string, recs []Record, streamID uint64, fromSeq uint64) (Cursor, error) {
	admitted, excluded := FilterBatch(recs)
	pos := e.nextPosition(ctx, domain, subkind)
	for _, excl := range excluded {
		pos++
		if err := e.cursors.AdvanceOverExclusion(ctx, domain, subkind, excl, pos); err != nil {
			return Cursor{}, err
		}
	}
	if len(admitted) > 0 {
		if err := e.sendAdmitted(ctx, conn, admitted, streamID, fromSeq); err != nil {
			return Cursor{}, err
		}
		pos += uint64(len(admitted))
		if err := e.cursors.Advance(ctx, domain, subkind, pos); err != nil {
			return Cursor{}, err
		}
	}
	return e.cursors.Get(ctx, domain, subkind)
}

// sendAdmitted runs admitted through the sync egress class and writes it
// to conn as a chunked stream. Split out of SendBatch purely to stay
// under the 50-line function cap.
func (e *Engine) sendAdmitted(ctx context.Context, conn io.Writer, admitted []Record, streamID, fromSeq uint64) error {
	payload, err := json.Marshal(admitted)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sync: encode batch")
	}
	if e.egress == nil {
		return cascade.New(cascade.KindUnavailable, "sync: no egress engine configured")
	}
	out, err := e.egress.InterceptClass(ctx, egress.EgressClassSync, egress.TierInternal, payload)
	if err != nil {
		return err
	}
	return TransferBytes(ctx, conn, streamID, out, DefaultChunkSize, fromSeq)
}

// nextPosition returns the current cursor position for domain+subkind,
// treating any read failure as position zero rather than propagating it
// mid-batch — Get itself never errors except on corrupt persisted state,
// which SendBatch's caller will also hit on its own next Get call.
func (e *Engine) nextPosition(ctx context.Context, domain storage.DomainID, subkind string) uint64 {
	cur, err := e.cursors.Get(ctx, domain, subkind)
	if err != nil {
		return 0
	}
	return cur.Position
}
