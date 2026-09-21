package plugins

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Purpose (this file): the durable half of the bridge composition root — the
//   cascade.db connection the bridge schema is applied on, and the adapter
//   that maps internal/bridge's row type onto the plugin-facing
//   cascadepa.BridgeState.
//
// Inputs: a data directory.
// Outputs: a BridgeState whose Save is a COMPARE-AND-SWAP.
//
// Constraints:
//   - THE TWO SHAPES STAY SEPARATE TYPES. internal/bridge must not import
//     plugins/** (Art.10.2 allows only THIS package to bridge the two), so the
//     conversion is explicit here rather than a shared struct neither side
//     could own. Version crosses as an opaque token: the plugin never reads
//     it, the store never trusts a caller to compute it.
//   - THE CONFLICT IS TRANSLATED, NOT SWALLOWED. internal/bridge refuses a
//     stale write with its own typed ErrVersionConflict; the plugin's retry
//     wrapper recognises cascadepa.ErrStateConflict. This adapter is the one
//     place those two vocabularies meet, and it maps rather than collapses:
//     losing the distinction would turn a refused write into a lost update,
//     which is the whole defect the compare-and-swap exists to prevent.
//
// SPORT: internal/plugins:cascadepa-bridge-state (ADD) — P1-E23-W5-S48-T1.

// openBridgeState opens cascade.db, applies the bridge schema, and adapts the
// row store onto the plugin-facing BridgeState seam.
func openBridgeState(ctx context.Context, dataDir string) (cascadepa.BridgeState, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa bridge: create the data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa bridge: open cascade.db")
	}
	if err := bridge.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(),
		dbPath, filepath.Join(dataDir, "backups")); err != nil {
		_ = db.Close()
		return nil, err
	}
	return bridgeStateAdapter{store: bridge.NewStore(db), db: db}, nil
}

// bridgeStateAdapter maps internal/bridge's row type onto the plugin-facing
// cascadepa.BridgeState. It also implements io.Closer over the *sql.DB
// openBridgeState opened: cascadepa.BridgeState itself carries no Close (the
// plugin-facing seam must not know it is SQLite), so the production owner
// closes it through this narrower, adapter-local contract instead — see
// enabledBridge in cascadepa_bridge_wiring.go, which type-asserts for it.
type bridgeStateAdapter struct {
	store *bridge.Store
	db    *sql.DB
}

// Close releases the database handle. Windows refuses to remove a temp
// directory that still holds an open cascade.db, so every caller that opens
// state — production, through the bridge's drain, and every test — must
// eventually reach this.
func (a bridgeStateAdapter) Close() error {
	if a.db == nil {
		return nil
	}
	return a.db.Close()
}

func (a bridgeStateAdapter) Load(ctx context.Context, subject string) (cascadepa.SubjectState, bool, error) {
	row, ok, err := a.store.Load(ctx, subject)
	if err != nil || !ok {
		return cascadepa.SubjectState{}, ok, err
	}
	return cascadepa.SubjectState{
		Subject:       row.Subject,
		TrustTier:     row.TrustTier,
		PairedAt:      row.PairedAt,
		AllowedFrom:   row.AllowedFrom,
		CodeDigest:    row.CodeDigest,
		CodeExpiresAt: row.CodeExpiresAt,
		WrongAttempts: row.WrongAttempts,
		Offset:        row.Offset,
		SeenUpdateIDs: row.SeenUpdateIDs,
		Version:       row.Version,
	}, true, nil
}

func (a bridgeStateAdapter) Save(ctx context.Context, st cascadepa.SubjectState) error {
	err := a.store.Save(ctx, bridge.SubjectRow{
		Subject:       st.Subject,
		TrustTier:     st.TrustTier,
		PairedAt:      st.PairedAt,
		AllowedFrom:   st.AllowedFrom,
		CodeDigest:    st.CodeDigest,
		CodeExpiresAt: st.CodeExpiresAt,
		WrongAttempts: st.WrongAttempts,
		Offset:        st.Offset,
		SeenUpdateIDs: st.SeenUpdateIDs,
		Version:       st.Version,
	})
	if bridge.IsVersionConflict(err) {
		// Translated, with the store's own refusal kept as the cause: the
		// plugin retries its read-modify-write on THIS error and on nothing
		// else, so the two vocabularies have to meet somewhere explicit.
		return cascade.Wrap(cascade.KindConflict, err, cascadepa.ErrStateConflict.Error())
	}
	return err
}

// runtime.SystemClock is what every bridge store reads time from; this
// assertion keeps the plugin-facing PairClock and the host clock pinned
// together, so a signature change on either side fails here rather than at the
// call sites.
var _ cascadepa.PairClock = runtime.SystemClock{}
