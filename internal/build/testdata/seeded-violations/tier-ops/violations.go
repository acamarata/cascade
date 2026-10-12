package tierops

import (
	"cmp"
	"context"
	"database/sql"
	"github.com/acamarata/cascade/pkg/provider"
)

type alias = provider.SensitivityTier

func identity[T any](v T) T { return v }

func safeOperations(a, b alias, index int, table []int) {
	_ = identity[alias](a)
	_ = a == b
	_ = a != b
	_ = a.MoreRestrictiveThan(b)
	_ = provider.JoinSensitivity(a, b)
	_ = table[index]
	_ = min(index, 2)
	_ = max(index, 2)
	_ = cmp.Compare(index, 2)
	_ = cmp.Less(index, 2)
}

func violations(a, b provider.SensitivityTier, p *provider.SensitivityTier, c alias, db *sql.DB, q string) {
	_ = string(a)           // want tier violation
	_ = a < b               // want tier violation
	_ = *p >= b             // want tier violation
	_ = c > b               // want tier violation
	_ = uint8(a) < uint8(b) // want tier violation
	_, _ = db.Exec(q, a)    // want tier violation
}

func numericViolations(a, b alias, table []int) {
	_ = min(a, b)                // want tier violation
	_ = max(a, b)                // want tier violation
	_ = cmp.Compare(a, b)        // want tier violation
	_ = cmp.Less(a, b)           // want tier violation
	_ = cmp.Compare[alias](a, b) // want tier violation
	_ = cmp.Less[alias](a, b)    // want tier violation
	_ = table[a] > table[b]      // want tier violation
	_ = a + b                    // want tier violation
	_ = a - b                    // want tier violation
	_ = a * b                    // want tier violation
	_ = a / b                    // want tier violation
	_ = a % b                    // want tier violation
	_ = a & b                    // want tier violation
	_ = a | b                    // want tier violation
	_ = a ^ b                    // want tier violation
	_ = a &^ b                   // want tier violation
	_ = a << b                   // want tier violation
	_ = a >> b                   // want tier violation
	_ = -a                       // want tier violation
	_ = +a                       // want tier violation
	_ = ^a                       // want tier violation
	a++                          // want tier violation
	b--                          // want tier violation
	a += b                       // want tier violation
	a &= b                       // want tier violation
}

type querier interface {
	Exec(string, ...any) (sql.Result, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sqlViolations(a alias, p *alias, stmt *sql.Stmt, local querier, ctx context.Context, q string) {
	_, _ = stmt.Exec(a)                  // want tier violation
	_, _ = stmt.ExecContext(ctx, p)      // want tier violation
	_, _ = stmt.Query(a)                 // want tier violation
	_, _ = stmt.QueryContext(ctx, p)     // want tier violation
	_ = stmt.QueryRow(a)                 // want tier violation
	_ = stmt.QueryRowContext(ctx, p)     // want tier violation
	_, _ = local.Exec(q, a)              // want tier violation
	_, _ = local.ExecContext(ctx, q, p)  // want tier violation
	_, _ = local.Query(q, a)             // want tier violation
	_, _ = local.QueryContext(ctx, q, p) // want tier violation
	_ = local.QueryRow(q, a)             // want tier violation
	_ = local.QueryRowContext(ctx, q, p) // want tier violation
}
