//go:build integration && security

package agents

// Purpose: a driver with no [agents.egress.<driver-id>] table gets a proxy
// that denies every destination: the CONNECT is refused 403, one deny event is
// journaled and nothing is dialed (legacy AC14, absent-table half).
//
// Constraints: only 127.0.0.1 sockets; stored audit state is asserted.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

func TestSpawnProxyAbsentTableDeniesEveryDestination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	log, el := netAudit(t, storetest.NewMemStore())
	rd := &routeDial{routes: map[string]string{netAllowedDest: echo(t)}}
	p, err := startSpawnProxy(ctx, egressproxy.DriverCodex, netJob, nil, ProxyJournal(log, el), rd.dial)
	if err != nil {
		t.Fatalf("startSpawnProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	addr, auth := proxyAddrAuth(t, p)
	c, _, code := connect(t, addr, netAllowedDest, auth)
	_ = c.Close()
	if code != http.StatusForbidden || rd.count() != 0 {
		t.Fatalf("absent table: status %d, dials %d; want 403 and no dial", code, rd.count())
	}
	evs := decideEvents(t, log)
	if len(evs) != 1 || evs[0].Verdict != "deny" || evs[0].Outcome != string(egressproxy.ReasonDestinationNotAllowed) {
		t.Fatalf("deny journal = %+v, want one deny with reason destination-not-allowed", evs)
	}
}
