package economics

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// topoReserver builds a fixture whose Buckets and ReserveFraction seams
// read the real topology database tf.
func topoReserver(t *testing.T, tf *topoFixture) (*reserverFixture, *Reserver) {
	t.Helper()
	f := newReserverFixture(t, 0)
	f.seams.Buckets = tf.bucketsSeam()
	f.seams.ReserveFraction = tf.reserveFractionSeam()
	return f, f.reserver(t)
}

// seedAPIDomain seeds an api_project domain with rpm/tpm/rpd buckets on
// the account scope.
func seedAPIDomain(t *testing.T, tf *topoFixture, acct topology.AccountID, dom topology.DomainID, rpm, tpm, rpd int64) topology.LimitScopeID {
	t.Helper()
	tf.seedDomain(t, acct, topology.AccountRoleWorkforce, dom, topology.QuotaDomainAPIProject)
	scope := topology.AccountScope(acct)
	tf.bucket(t, dom, topology.DimensionRPM, rpm, rpm, scope)
	tf.bucket(t, dom, topology.DimensionTPM, tpm, tpm, scope)
	tf.bucket(t, dom, topology.DimensionRPD, rpd, rpd, scope)
	return scope
}

// TestReserveAdmitsAgainstTopologyDimensions: the estimate maps onto the
// domain kind's real dimensions; a request that fits tpm but not rpm
// refuses naming rpm; a second reservation is refused only because a
// first held row sits on the same scope. The combined capacity (1+10^6+
// 10^3) would admit every request here, so a combined-weight check fails.
func TestReserveAdmitsAgainstTopologyDimensions(t *testing.T) {
	tf := newTopoFixture(t)
	seedAPIDomain(t, tf, "acct-1", "domain-1", 1, 1000000, 1000)
	_, rv := topoReserver(t, tf)

	tooMany := baseReserveRequest()
	tooMany.Estimate = Estimate{TokensIn: 10, TokensOut: 10, Requests: 2}
	_, err := rv.Reserve(t.Context(), tooMany)
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpm"`)

	first := baseReserveRequest()
	first.Estimate = Estimate{TokensIn: 10, TokensOut: 10, Requests: 1}
	r1, err := rv.Reserve(t.Context(), first)
	if err != nil || r1.State != ReservationHeld {
		t.Fatalf("first Reserve = %+v, %v, want held", r1, err)
	}
	second := baseReserveRequest()
	second.Estimate = first.Estimate
	_, err = rv.Reserve(t.Context(), second)
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpm"`, "available 0")

	if _, err := rv.Release(t.Context(), r1.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	third := baseReserveRequest()
	third.Estimate = first.Estimate
	if r3, err := rv.Reserve(t.Context(), third); err != nil || r3.State != ReservationHeld {
		t.Fatalf("Reserve after the first row released = %+v, %v, want held", r3, err)
	}
}

// TestReserveProjectShareExceeded uses real inputs: two projects started
// through ActiveProjectCount, the barrier bucket (rpd for api_project)
// and the workforce reserve 0.15 from the seeded rows. Share per project
// = (1.0 - 0.15) / 2 x 100 = 42.5 rpd units.
func TestReserveProjectShareExceeded(t *testing.T) {
	tf := newTopoFixture(t)
	seedAPIDomain(t, tf, "acct-1", "domain-1", 100000, 100000000, 100)
	f, rv := topoReserver(t, tf)
	f.seams.Projects.ProjectStarted()
	f.seams.Projects.ProjectStarted()

	reqFor := func(project string, requests int64) ReserveRequest {
		r := baseReserveRequest()
		r.ProjectID, r.Estimate = project, Estimate{TokensIn: 1, Requests: requests}
		return r
	}
	if _, err := rv.Reserve(t.Context(), reqFor("project-a", 40)); err != nil {
		t.Fatalf("project-a 40 units: %v, want admitted (40 <= 42.5)", err)
	}
	_, err := rv.Reserve(t.Context(), reqFor("project-a", 5))
	requireSentinel(t, err, ErrProjectShareExceeded, "project-a")
	if _, err := rv.Reserve(t.Context(), reqFor("project-b", 40)); err != nil {
		t.Fatalf("project-b 40 units: %v, want admitted (its own share)", err)
	}
	f.seams.Projects.ProjectStopped()
	if _, err := rv.Reserve(t.Context(), reqFor("project-a", 5)); err != nil {
		t.Fatalf("project-a after ProjectStopped: %v, want admitted (share 85)", err)
	}
}

// TestReserveSharedAccountScopeAcrossDomains: two domains of one account
// share the account scope; outstanding estimates are summed by scope, so
// the second domain refuses when the sum exceeds the scope capacity; a
// bucket carrying a different LimitScopeID refuses; a caller ScopeID
// that differs from the derived scope refuses KindInvalidInput.
func TestReserveSharedAccountScopeAcrossDomains(t *testing.T) {
	tf := newTopoFixture(t)
	scope := seedAPIDomain(t, tf, "acct-1", "domain-a", 1000, 10000, 1000)
	seedAPIDomain(t, tf, "acct-1", "domain-b", 1000, 10000, 1000)
	_, rv := topoReserver(t, tf)

	onA := baseReserveRequest()
	onA.DomainID, onA.Estimate = "domain-a", Estimate{TokensIn: 6000, Requests: 1}
	if r, err := rv.Reserve(t.Context(), onA); err != nil || r.ScopeID != string(scope) {
		t.Fatalf("Reserve on domain-a = %+v, %v, want held on %s", r, err, scope)
	}
	onB := baseReserveRequest()
	onB.DomainID, onB.Estimate = "domain-b", Estimate{TokensIn: 6000, Requests: 1}
	_, err := rv.Reserve(t.Context(), onB)
	requireSentinel(t, err, ErrQuotaUnavailable, `"tpm"`)

	wrongScope := baseReserveRequest()
	wrongScope.DomainID, wrongScope.ScopeID, wrongScope.Estimate = "domain-a", "scope:other", Estimate{TokensIn: 1, Requests: 1}
	if _, err := rv.Reserve(t.Context(), wrongScope); !isKindInvalidInput(err) {
		t.Errorf("Reserve with a differing caller ScopeID = %v, want KindInvalidInput", err)
	}

	tf.bucket(t, "domain-b", topology.DimensionRPD, 1000, 1000, "scope:elsewhere")
	small := baseReserveRequest()
	small.DomainID, small.Estimate = "domain-b", Estimate{TokensIn: 1, Requests: 1}
	_, err = rv.Reserve(t.Context(), small)
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpd"`, "scope:elsewhere")
}

// TestReserveFailClosedEdges: Limit 0, an unknown stored domain kind, a
// deleted ReservationUnits row and a domain with no buckets each refuse
// ErrQuotaUnavailable naming the cause; only an undiscovered bucket
// (DiscoverLimit and UnobservedCapacity) skips the capacity gate.
func TestReserveFailClosedEdges(t *testing.T) {
	tf := newTopoFixture(t)
	scope := seedAPIDomain(t, tf, "acct-1", "domain-1", 100, 100000, 100)
	tf.seedDomain(t, "acct-2", topology.AccountRoleWorkforce, "domain-empty", topology.QuotaDomainAPIProject)
	_, rv := topoReserver(t, tf)
	small := func(dom string, requests int64) ReserveRequest {
		r := baseReserveRequest()
		r.DomainID, r.Estimate = dom, Estimate{TokensIn: 1, Requests: requests}
		return r
	}
	_, err := rv.Reserve(t.Context(), small("domain-empty", 1))
	requireSentinel(t, err, ErrQuotaUnavailable, "domain-empty", "no buckets")

	delete(ReservationUnits[topology.QuotaDomainAPIProject], topology.DimensionRPM)
	_, err = rv.Reserve(t.Context(), small("domain-1", 1))
	ReservationUnits[topology.QuotaDomainAPIProject][topology.DimensionRPM] = unitsRequests
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpm"`, "no reservation unit")

	tf.bucket(t, "domain-1", topology.DimensionRPD, topology.DiscoverLimit, topology.UnobservedCapacity, scope)
	if _, err := rv.Reserve(t.Context(), small("domain-1", 90)); err != nil {
		t.Fatalf("undiscovered rpd bucket: %v, want the capacity gate skipped", err)
	}
	tf.bucket(t, "domain-1", topology.DimensionRPD, topology.DiscoverLimit, 0, scope)
	_, err = rv.Reserve(t.Context(), small("domain-1", 1))
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpd"`)
	tf.bucket(t, "domain-1", topology.DimensionRPD, 5, topology.UnobservedCapacity, scope)
	_, err = rv.Reserve(t.Context(), small("domain-1", 6))
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpd"`)
	tf.bucket(t, "domain-1", topology.DimensionRPD, 0, 100, scope)
	_, err = rv.Reserve(t.Context(), small("domain-1", 1))
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpd"`, "limit 0")

	if _, err := tf.db.ExecContext(t.Context(), `UPDATE config_quota_domain SET kind = 'bogus_kind' WHERE id = 'domain-1'`); err != nil {
		t.Fatalf("store an unknown kind: %v", err)
	}
	_, err = rv.Reserve(t.Context(), small("domain-1", 1))
	requireSentinel(t, err, ErrQuotaUnavailable, `unknown domain kind "bogus_kind"`)
}

// TestReserveRefusesMissingDimensionBucket is the fail-closed probe: an
// api_project domain with only a tpm bucket, or with no rpm bucket, must
// not admit a request no bucket of that dimension was consulted for, and
// a domain with no barrier bucket must not skip the project share. Each
// refuses ErrQuotaUnavailable naming the missing dimension, holding no row.
func TestReserveRefusesMissingDimensionBucket(t *testing.T) {
	tf := newTopoFixture(t)
	tf.seedDomain(t, "acct-1", topology.AccountRoleWorkforce, "domain-tpm", topology.QuotaDomainAPIProject)
	tf.bucket(t, "domain-tpm", topology.DimensionTPM, 1<<40, 1<<40, topology.AccountScope("acct-1"))
	tf.seedDomain(t, "acct-3", topology.AccountRoleWorkforce, "domain-norpm", topology.QuotaDomainAPIProject)
	tf.bucket(t, "domain-norpm", topology.DimensionTPM, 1<<40, 1<<40, topology.AccountScope("acct-3"))
	tf.bucket(t, "domain-norpm", topology.DimensionRPD, 1<<40, 1<<40, topology.AccountScope("acct-3"))
	tf.seedDomain(t, "acct-2", topology.AccountRoleWorkforce, "domain-noshare", topology.QuotaDomainAPIProject)
	tf.bucket(t, "domain-noshare", topology.DimensionRPM, 1<<40, 1<<40, topology.AccountScope("acct-2"))
	tf.bucket(t, "domain-noshare", topology.DimensionTPM, 1<<40, 1<<40, topology.AccountScope("acct-2"))
	f, rv := topoReserver(t, tf)
	requireNoHeldRow := func(execID string) {
		t.Helper()
		if r, ok, _ := f.store.GetByExecution(t.Context(), execID); ok && r.State != ReservationRolledBack {
			t.Fatalf("refused request left row %+v, want rolled_back", r)
		}
		if held, err := f.store.ListByState(t.Context(), ReservationHeld); err != nil || len(held) != 0 {
			t.Fatalf("held rows = %d (%v), want 0", len(held), err)
		}
	}

	probe := baseReserveRequest()
	probe.DomainID, probe.Estimate = "domain-tpm", Estimate{TokensIn: 1, Requests: 1000000}
	_, err := rv.Reserve(t.Context(), probe)
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpd"`, "no bucket")
	requireNoHeldRow(probe.ExecutionID)
	noRPM := baseReserveRequest()
	noRPM.DomainID, noRPM.Estimate = "domain-norpm", Estimate{TokensIn: 1, Requests: 1000000}
	_, err = rv.Reserve(t.Context(), noRPM)
	requireSentinel(t, err, ErrQuotaUnavailable, `dimension "rpm" has no bucket`)
	requireNoHeldRow(noRPM.ExecutionID)

	delete(ReservationUnits[topology.QuotaDomainAPIProject], topology.DimensionRPD)
	t.Cleanup(func() { ReservationUnits[topology.QuotaDomainAPIProject][topology.DimensionRPD] = unitsRequests })
	noShare := baseReserveRequest()
	noShare.DomainID, noShare.Estimate = "domain-noshare", Estimate{TokensIn: 1, Requests: 1}
	_, err = rv.Reserve(t.Context(), noShare)
	requireSentinel(t, err, ErrQuotaUnavailable, `"rpd"`, "barrier")
	requireNoHeldRow(noShare.ExecutionID)
}
