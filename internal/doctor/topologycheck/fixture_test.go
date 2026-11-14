package topologycheck

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stubTopology is an in-memory TopologyReader. A non-nil err fails the
// named list call ("domains" or "creds").
type stubTopology struct {
	domains []topology.QuotaDomain
	creds   []topology.Credential
	err     error
	failOn  string
}

func (s *stubTopology) ListQuotaDomains(context.Context) ([]topology.QuotaDomain, error) {
	if s.failOn == "domains" {
		return nil, s.err
	}
	return s.domains, nil
}

func (s *stubTopology) ListCredentials(context.Context) ([]topology.Credential, error) {
	if s.failOn == "creds" {
		return nil, s.err
	}
	return s.creds, nil
}

// stubLedger is an in-memory ReservationReader.
type stubLedger struct {
	rows []ReservationRow
	err  error
}

func (s *stubLedger) ListReservations(context.Context) ([]ReservationRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

var errStubRead = cascade.New(cascade.KindUnavailable, "stub: store read failed")

func domain(id string, kind topology.QuotaDomainKind, tier topology.BillingTier) topology.QuotaDomain {
	return topology.QuotaDomain{ID: topology.DomainID(id), Kind: kind, BillingTier: tier}
}

func credential(id, domainRef string) topology.Credential {
	return topology.Credential{
		ID:             topology.CredentialID(id),
		QuotaDomainRef: topology.DomainID(domainRef),
		SecretRef:      topology.VaultKeyRef("ref-" + id),
	}
}

// cleanTopology has one domain of every kind and tier pairing the tests
// use, each referenced by a credential.
func cleanTopology() *stubTopology {
	return &stubTopology{
		domains: []topology.QuotaDomain{
			domain("dom-free", topology.QuotaDomainAPIProject, topology.BillingTierFree),
			domain("dom-disc", topology.QuotaDomainSharedPool, topology.BillingTierDiscover),
			domain("dom-paid", topology.QuotaDomainAPIProject, topology.BillingTierPaid),
		},
		creds: []topology.Credential{
			credential("cred-free", "dom-free"),
			credential("cred-disc", "dom-disc"),
			credential("cred-paid", "dom-paid"),
		},
	}
}

// runCheck runs c and fails the test on a returned error: the checks
// report every failure through the result.
func runCheck(ctx context.Context, t *testing.T, c doctor.Check) doctor.CheckResult {
	t.Helper()
	res, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("%s Run error = %v, want nil (failures go in the result)", c.Name(), err)
	}
	return res
}

// wantStatus asserts the status, and for a warn a non-empty detail
// containing every want string.
func wantStatus(t *testing.T, res doctor.CheckResult, status doctor.Status, want ...string) {
	t.Helper()
	if res.Status != status {
		t.Fatalf("status = %s (%s), want %s", res.Status, res.Detail, status)
	}
	if status == doctor.StatusWarn && res.Detail == "" {
		t.Fatal("warn with an empty Detail")
	}
	for _, w := range want {
		if !strings.Contains(res.Detail, w) {
			t.Fatalf("detail %q lacks %q", res.Detail, w)
		}
	}
}

// wantReadFailures asserts StatusError for a nil reader and for each
// reader error.
func wantReadFailures(ctx context.Context, t *testing.T, nilCheck doctor.Check, failing ...doctor.Check) {
	t.Helper()
	res := runCheck(ctx, t, nilCheck)
	wantStatus(t, res, doctor.StatusError, errNilReader.Error())
	for _, c := range failing {
		wantStatus(t, runCheck(ctx, t, c), doctor.StatusError, errStubRead.Error())
	}
}

// dump renders every row the readers hold, for byte comparison.
func dump(t *testing.T, topo *stubTopology, led *stubLedger) []byte {
	t.Helper()
	b, err := json.Marshal([]any{topo.domains, topo.creds, led.rows})
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	return b
}
