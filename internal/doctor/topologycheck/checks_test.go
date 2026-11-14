package topologycheck

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/topology"
)

// sensitivityLedger holds, on the domain under test, a held and a released
// row above public, a public row, an empty and an unparsable row, plus an
// above-public row on the paid domain that must stay silent.
func sensitivityLedger(dom string) *stubLedger {
	return &stubLedger{rows: []ReservationRow{
		{ID: "res-held", DomainID: dom, State: "held", Sensitivity: "internal"},
		{ID: "res-released", DomainID: dom, State: "released", Sensitivity: "restricted"},
		{ID: "res-public", DomainID: dom, State: "held", Sensitivity: "public"},
		{ID: "res-empty", DomainID: dom, State: "committed", Sensitivity: ""},
		{ID: "res-bad", DomainID: dom, State: "parked", Sensitivity: "Secret"},
		{ID: "res-paid", DomainID: "dom-paid", State: "held", Sensitivity: "local-only"},
	}}
}

type sensitivityCtor func(TopologyReader, ReservationReader) doctor.Check

// checkSensitivity is the shared body of the two sensitivity tests.
func checkSensitivity(ctx context.Context, t *testing.T, ctor sensitivityCtor, dom string) {
	t.Helper()
	res := runCheck(ctx, t, ctor(cleanTopology(), sensitivityLedger(dom)))
	wantStatus(t, res, doctor.StatusWarn, "res-held", "res-released", "res-empty", "res-bad", dom)
	for _, silent := range []string{"res-public", "res-paid"} {
		if strings.Contains(res.Detail, silent) {
			t.Fatalf("detail %q reports %s", res.Detail, silent)
		}
	}
	clean := &stubLedger{rows: []ReservationRow{{ID: "res-ok", DomainID: dom, State: "held", Sensitivity: "public"}}}
	wantStatus(t, runCheck(ctx, t, ctor(cleanTopology(), clean)), doctor.StatusOK)
	wantStatus(t, runCheck(ctx, t, ctor(cleanTopology(), nil)), doctor.StatusError, errNilReader.Error())
	wantReadFailures(ctx, t, ctor(nil, clean),
		ctor(cleanTopology(), &stubLedger{err: errStubRead}),
		ctor(&stubTopology{failOn: "domains", err: errStubRead}, clean))
}

func TestDoctorFreeDomainAbovePublic(t *testing.T) {
	checkSensitivity(context.Background(), t, NewFreeDomainAbovePublicCheck, "dom-free")
}

func TestDoctorDiscoverDomainAbovePublic(t *testing.T) {
	checkSensitivity(context.Background(), t, NewDiscoverDomainAbovePublicCheck, "dom-disc")
}

func TestDoctorCredentialWithoutDomain(t *testing.T) {
	ctx := context.Background()
	topo := cleanTopology()
	topo.creds = append(topo.creds, credential("cred-empty", ""), credential("cred-dangling", "dom-gone"))
	res := runCheck(ctx, t, NewCredentialWithoutDomainCheck(topo))
	wantStatus(t, res, doctor.StatusWarn,
		"credential cred-empty (secret_ref ref-cred-empty): empty quota_domain_ref",
		"credential cred-dangling (secret_ref ref-cred-dangling): quota domain dom-gone not found")
	if strings.Contains(res.Detail, "cred-free") {
		t.Fatalf("detail %q reports a credential with a live domain", res.Detail)
	}
	wantStatus(t, runCheck(ctx, t, NewCredentialWithoutDomainCheck(cleanTopology())), doctor.StatusOK)
	wantReadFailures(ctx, t, NewCredentialWithoutDomainCheck(nil),
		NewCredentialWithoutDomainCheck(&stubTopology{failOn: "domains", err: errStubRead}),
		NewCredentialWithoutDomainCheck(&stubTopology{failOn: "creds", err: errStubRead}))
	if _, err := credentialWithoutDomain(ctx, nil); err != errNilReader || err.Error() != errNilReader.Error() {
		t.Fatalf("nil reader error = %v, want the errNilReader sentinel", err)
	}
}

func TestDoctorDomainWithoutCredential(t *testing.T) {
	ctx := context.Background()
	topo := cleanTopology()
	topo.domains = append(topo.domains,
		domain("dom-api-bare", topology.QuotaDomainAPIProject, topology.BillingTierPaid),
		domain("dom-pool-bare", topology.QuotaDomainSharedPool, topology.BillingTierFree),
		domain("dom-sub-bare", topology.QuotaDomainSubscriptionWindow, topology.BillingTierPaid))
	res := runCheck(ctx, t, NewDomainWithoutCredentialCheck(topo))
	wantStatus(t, res, doctor.StatusWarn, "dom-api-bare", "dom-pool-bare")
	for _, silent := range []string{"dom-sub-bare", "dom-free"} {
		if strings.Contains(res.Detail, silent) {
			t.Fatalf("detail %q reports %s", res.Detail, silent)
		}
	}
	wantStatus(t, runCheck(ctx, t, NewDomainWithoutCredentialCheck(cleanTopology())), doctor.StatusOK)
	wantReadFailures(ctx, t, NewDomainWithoutCredentialCheck(nil),
		NewDomainWithoutCredentialCheck(&stubTopology{failOn: "domains", err: errStubRead}),
		NewDomainWithoutCredentialCheck(&stubTopology{failOn: "creds", err: errStubRead}))
	if !needsCredential(topology.QuotaDomainKind("unknown")) {
		t.Fatal("an unknown domain kind must be reported (fail closed)")
	}
}
