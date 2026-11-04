package learn

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ObservationResetter deletes matching capability observations idempotently.
type ObservationResetter interface {
	Reset(ctx context.Context, scope, taskClass, tier string, all bool) (int, error)
}

// SQLiteObservationResetter executes one parameterised delete through DB.
type SQLiteObservationResetter struct{ DB Execer }

// Reset removes observations matching every supplied filter. A scope or all
// is required, even when a task class or tier filter is supplied.
func (r *SQLiteObservationResetter) Reset(ctx context.Context, scope, taskClass, tier string, all bool) (int, error) {
	if err := validateReset(scope, taskClass, tier, all); err != nil {
		return 0, err
	}
	if r == nil || r.DB == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: reset requires a database")
	}
	filters := []string{}
	args := []any{}
	for _, f := range []struct{ column, value string }{{"scope_key", scope}, {"task_class", taskClass}, {"tier", tier}} {
		if f.value != "" {
			filters = append(filters, f.column+" = ?")
			args = append(args, f.value)
		}
	}
	query := `DELETE FROM ` + tableCapabilityScore
	if len(filters) > 0 {
		query += " WHERE " + strings.Join(filters, " AND ")
	}
	result, err := r.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: reset observations")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: reset row count")
	}
	return int(n), nil
}

func validateReset(scope, taskClass, tier string, all bool) error {
	if scope == "" && !all {
		return cascade.New(cascade.KindInvalidInput, "learn: reset requires scope_key or all")
	}
	if scope != "" {
		if _, err := ParseScopeKey(scope); err != nil {
			return err
		}
	}
	if tier != "" && !capacity.Tier(tier).Valid() {
		return cascade.New(cascade.KindInvalidInput, "learn: invalid tier")
	}
	if taskClass != "" {
		if _, ok := capacity.Priors[capacity.TierZero][taskClassType(taskClass)]; !ok {
			return cascade.New(cascade.KindInvalidInput, "learn: task class has no capability prior")
		}
	}
	return nil
}
