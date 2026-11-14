package topologycheck

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// TopologyReader is the read-only topology surface the checks need. A
// *topology.Store satisfies it.
type TopologyReader interface {
	// ListQuotaDomains returns every quota-domain row.
	ListQuotaDomains(ctx context.Context) ([]topology.QuotaDomain, error)
	// ListCredentials returns every credential row.
	ListCredentials(ctx context.Context) ([]topology.Credential, error)
}

// ReservationRow is the subset of a reservation-ledger row the
// sensitivity checks read: its id, the quota domain it sits on, its state
// in wire form and the sensitivity recorded when it was reserved.
type ReservationRow struct {
	ID          string
	DomainID    string
	State       string
	Sensitivity string
}

// ReservationReader is the read-only reservation-ledger surface the
// sensitivity checks need.
type ReservationReader interface {
	// ListReservations returns every reservation row, live or terminal.
	ListReservations(ctx context.Context) ([]ReservationRow, error)
}
