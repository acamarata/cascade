package topologycheck

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

func testClock() cascaderuntime.Clock {
	return cascaderuntime.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
}

func allChecks(topo TopologyReader, res ReservationReader) []doctor.Check {
	return []doctor.Check{
		NewFreeDomainAbovePublicCheck(topo, res),
		NewDiscoverDomainAbovePublicCheck(topo, res),
		NewCredentialWithoutDomainCheck(topo),
		NewDomainWithoutCredentialCheck(topo),
	}
}

func TestDoctorTopologyChecksNeverFix(t *testing.T) {
	ctx := context.Background()
	topo := cleanTopology()
	topo.creds = append(topo.creds, credential("cred-dangling", "dom-gone"))
	topo.domains = append(topo.domains, domain("dom-bare", topology.QuotaDomainAPIProject, topology.BillingTierFree))
	led := sensitivityLedger("dom-free")
	led.rows = append(led.rows, ReservationRow{ID: "res-disc", DomainID: "dom-disc", State: "held", Sensitivity: "internal"})
	path := filepath.Join(t.TempDir(), "rows.json")
	if err := os.WriteFile(path, dump(t, topo, led), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}
	checks := allChecks(topo, led)
	rep := doctor.Run(ctx, checks, doctor.RunOptions{Fix: true, Clock: testClock()})
	if len(rep.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(rep.Entries))
	}
	for _, e := range rep.Entries {
		if e.Result.Status != doctor.StatusWarn || e.Fixed {
			t.Fatalf("entry %s: status %s fixed %v, want warn and not fixed", e.Name, e.Result.Status, e.Fixed)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if !bytes.Equal(before, dump(t, topo, led)) {
		t.Fatal("a --fix run changed the reader rows")
	}
	for _, c := range checks {
		if c.Metadata().Fixable {
			t.Fatalf("%s declares Fixable", c.Name())
		}
		if _, err := c.Fix(ctx); !errors.Is(err, doctor.ErrCheckNotFixable) || err != doctor.ErrCheckNotFixable {
			t.Fatalf("%s Fix error = %v, want ErrCheckNotFixable", c.Name(), err)
		}
		if c.Describe() == "" {
			t.Fatalf("%s has an empty description", c.Name())
		}
	}
}

func TestDoctorProvidersFilter(t *testing.T) {
	checks := allChecks(cleanTopology(), &stubLedger{})
	for _, c := range checks {
		g, ok := c.(doctor.GroupedCheck)
		if !ok || g.Group() != doctor.GroupProviders {
			t.Fatalf("%s is not in group providers", c.Name())
		}
	}
	other := NewDomainWithoutCredentialCheck(nil)
	checks = append(checks, ungrouped{other})
	rep := doctor.Run(context.Background(), checks, doctor.RunOptions{ProvidersOnly: true, Clock: testClock()})
	var got []string
	for _, e := range rep.Entries {
		got = append(got, e.Name)
	}
	want := []string{"credential_without_domain", "discover_domain_above_public",
		"domain_without_credential", "free_domain_above_public"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProvidersOnly entries = %v, want %v", got, want)
	}
	all := doctor.Run(context.Background(), checks, doctor.RunOptions{Clock: testClock()})
	if len(all.Entries) != 5 {
		t.Fatalf("unfiltered entries = %d, want 5", len(all.Entries))
	}
}

// ungrouped hides the wrapped check's Group method and renames it.
type ungrouped struct{ inner doctor.Check }

func (u ungrouped) Name() string                                        { return "ungrouped_check" }
func (u ungrouped) Describe() string                                    { return u.inner.Describe() }
func (u ungrouped) Metadata() doctor.CheckMeta                          { return u.inner.Metadata() }
func (u ungrouped) Run(ctx context.Context) (doctor.CheckResult, error) { return u.inner.Run(ctx) }
func (u ungrouped) Fix(ctx context.Context) (doctor.FixResult, error)   { return u.inner.Fix(ctx) }
