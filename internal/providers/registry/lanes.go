// Purpose: the provider_lanes CRUD surface -- UpsertLane and ListLanes.
//
//	Pool-specific operations (ListPool, AdvancePoolIndex) live in pool.go.
//
// Inputs: an open *sql.DB already migrated via ApplyMigrationSchema.
// Outputs: LaneRecord values, or a pkg/cascade taxonomy error.
// Constraints: every row Validate()s before it reaches SQL. ListLanes
//
//	orders by lane_name for a deterministic, golden-testable result.
//
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2).

package registry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Reader adapts a *Registry to pkg/provider.ProviderRegistryReader: the
// read-only view a pkg/-layer consumer (the K/S-22 conductor) is allowed
// to see. The write-capable *Registry itself is never exported to pkg/ --
// Reader converts every record on each call rather than exposing the
// internal type. Construct with NewReader.
type Reader struct {
	reg *Registry
}

// NewReader wraps reg as a pkg/provider.ProviderRegistryReader.
func NewReader(reg *Registry) *Reader { return &Reader{reg: reg} }

// GetProvider implements provider.ProviderRegistryReader.
func (r *Reader) GetProvider(ctx context.Context, name string) (provider.ProviderInfo, error) {
	rec, err := r.reg.GetProvider(ctx, name)
	if err != nil {
		return provider.ProviderInfo{}, err
	}
	return providerToInfo(rec), nil
}

// ListProviders implements provider.ProviderRegistryReader.
func (r *Reader) ListProviders(ctx context.Context) ([]provider.ProviderInfo, error) {
	recs, err := r.reg.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ProviderInfo, len(recs))
	for i, rec := range recs {
		out[i] = providerToInfo(rec)
	}
	return out, nil
}

// GetByModel implements provider.ProviderRegistryReader.
func (r *Reader) GetByModel(ctx context.Context, model string) ([]provider.ProviderInfo, error) {
	recs, err := r.reg.GetByModel(ctx, model)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ProviderInfo, len(recs))
	for i, rec := range recs {
		out[i] = providerToInfo(rec)
	}
	return out, nil
}

// ListLanes implements provider.ProviderRegistryReader.
func (r *Reader) ListLanes(ctx context.Context) ([]provider.LaneInfo, error) {
	recs, err := r.reg.ListLanes(ctx)
	if err != nil {
		return nil, err
	}
	return laneRecordsToInfo(recs), nil
}

// ListPool implements provider.ProviderRegistryReader.
func (r *Reader) ListPool(ctx context.Context, pool string) ([]provider.LaneInfo, error) {
	recs, err := r.reg.ListPool(ctx, pool)
	if err != nil {
		return nil, err
	}
	return laneRecordsToInfo(recs), nil
}

// providerToInfo converts an internal ProviderRecord to the pkg/-level
// read view.
func providerToInfo(rec ProviderRecord) provider.ProviderInfo {
	return provider.ProviderInfo{
		Name:         rec.Name,
		Driver:       string(rec.Driver),
		BaseURL:      rec.BaseURL,
		KnownModels:  rec.KnownModels,
		AccountKind:  string(rec.AccountKind),
		Tier:         string(rec.Tier),
		Capabilities: rec.Capabilities,
		HealthStatus: string(rec.HealthStatus),
	}
}

// laneRecordsToInfo converts internal LaneRecords to the pkg/-level read
// view, preserving order.
func laneRecordsToInfo(recs []LaneRecord) []provider.LaneInfo {
	out := make([]provider.LaneInfo, len(recs))
	for i, rec := range recs {
		out[i] = provider.LaneInfo{
			LaneName:       rec.LaneName,
			ProviderName:   rec.ProviderName,
			ModelFilter:    rec.ModelFilter,
			Weight:         rec.Weight,
			PoolMembership: rec.PoolMembership,
			Capacity:       string(rec.Capacity),
			State:          string(rec.State),
		}
	}
	return out
}

// laneSelectColumns is the shared SELECT column list for every
// provider_lanes read, kept in exactly the order scanLaneRow expects.
const laneSelectColumns = `SELECT lane_name, provider_name, model_filter, weight, pool_membership,
	pool_index, capacity, state, reset_estimate`

// Validate checks rec's closed-vocabulary fields and required identifiers.
// Called by UpsertLane before any DB write.
func (rec LaneRecord) Validate() error {
	if rec.LaneName == "" {
		return cascade.New(cascade.KindInvalidInput, "registry: lane_name is required")
	}
	if rec.ProviderName == "" {
		return cascade.New(cascade.KindInvalidInput, "registry: provider_name is required")
	}
	if !rec.Capacity.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "registry: invalid capacity bucket %q", rec.Capacity)
	}
	if !rec.State.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"registry: invalid lane state %q (empty is not valid -- use LaneStateUnknown)", rec.State)
	}
	return nil
}

// UpsertLane inserts or updates rec by rec.LaneName.
func (r *Registry) UpsertLane(ctx context.Context, rec LaneRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	modelFilter, err := json.Marshal(nonNilStrings(rec.ModelFilter))
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "registry: encode model_filter")
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO `+tableProviderLanes+`
			(lane_name, provider_name, model_filter, weight, pool_membership, pool_index, capacity, state, reset_estimate)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(lane_name) DO UPDATE SET
			provider_name=excluded.provider_name, model_filter=excluded.model_filter, weight=excluded.weight,
			pool_membership=excluded.pool_membership, pool_index=excluded.pool_index,
			capacity=excluded.capacity, state=excluded.state, reset_estimate=excluded.reset_estimate`,
		rec.LaneName, rec.ProviderName, string(modelFilter), rec.Weight, rec.PoolMembership, rec.PoolIndex,
		string(rec.Capacity), string(rec.State), millisPtr(rec.ResetEstimate))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry: upsert lane %q", rec.LaneName)
	}
	return nil
}

// ListLanes returns every lane record, in stable lexicographic order by
// lane_name.
func (r *Registry) ListLanes(ctx context.Context) ([]LaneRecord, error) {
	rows, err := r.db.QueryContext(ctx, laneSelectColumns+` FROM `+tableProviderLanes+` ORDER BY lane_name`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "registry: list lanes")
	}
	defer func() { _ = rows.Close() }()
	out := make([]LaneRecord, 0)
	for rows.Next() {
		rec, serr := scanLaneRow(rows)
		if serr != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, serr, "registry: scan lane row")
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "registry: iterate lanes")
	}
	return out, nil
}

// scanLaneRow scans one laneSelectColumns row into a LaneRecord.
func scanLaneRow(row rowScanner) (LaneRecord, error) {
	var (
		rec           LaneRecord
		modelFilter   string
		capacity      string
		state         string
		resetEstimate sql.NullInt64
	)
	err := row.Scan(&rec.LaneName, &rec.ProviderName, &modelFilter, &rec.Weight, &rec.PoolMembership,
		&rec.PoolIndex, &capacity, &state, &resetEstimate)
	if err != nil {
		return LaneRecord{}, err
	}
	rec.Capacity = CapacityBucket(capacity)
	rec.State = LaneState(state)
	rec.ResetEstimate = nullMillisToTime(resetEstimate)
	if err := json.Unmarshal([]byte(modelFilter), &rec.ModelFilter); err != nil {
		return LaneRecord{}, cascade.Wrap(cascade.KindIntegrity, err, "registry: decode model_filter")
	}
	return rec, nil
}
