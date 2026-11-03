package notify

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// provenanceHome isolates each provenance test under t.TempDir().
func provenanceHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CASCADE_HOME", dir)
}

func TestStampDerivesClassFromOrigin(t *testing.T) {
	provenanceHome(t)
	tiers := []struct {
		tier provider.SensitivityTier
		want provider.SensitivityTier
	}{
		{provider.SensitivityRestricted, provider.SensitivityRestricted},
		{provider.SensitivityLocalOnly, provider.SensitivityLocalOnly},
		{provider.SensitivityInternal, provider.SensitivityInternal},
		{provider.SensitivityPublic, provider.SensitivityInternal},
		{provider.SensitivityTier(9), provider.SensitivityLocalOnly},
	}
	visibilities := []struct {
		v     Visibility
		known bool
	}{
		{VisibilityPrivate, true}, {VisibilityScoped, true}, {VisibilityShared, true},
		{VisibilityExecutive, true}, {"", false}, {"world", false},
	}
	for _, tc := range tiers {
		for _, vc := range visibilities {
			want := tc.want
			if !vc.known {
				want = provider.SensitivityLocalOnly
			}
			// A producer-set class is overwritten: start from the
			// opposite end of the ranking from want.
			preset := provider.SensitivityPublic
			if want == provider.SensitivityPublic {
				preset = provider.SensitivityLocalOnly
			}
			n := Notification{
				Visibility: vc.v, DataClass: preset,
				Origin: Origin{Source: "ci.run", RecordRef: "r1", Tier: tc.tier, Trust: provider.ProvenanceTrusted},
			}
			stamp(&n)
			if n.DataClass != want {
				t.Errorf("tier %v visibility %q: DataClass = %v, want %v", tc.tier, vc.v, n.DataClass, want)
			}
		}
	}
	zero := Notification{Visibility: VisibilityShared, DataClass: provider.SensitivityPublic}
	stamp(&zero)
	if zero.DataClass != provider.SensitivityLocalOnly {
		t.Fatalf("zero Origin: DataClass = %v, want local-only", zero.DataClass)
	}
}

func TestStampTrustClosedProducers(t *testing.T) {
	provenanceHome(t)
	cases := []struct {
		source string
		claim  provider.Provenance
		want   provider.Provenance
	}{
		{"supervision.stall", provider.ProvenanceTrusted, provider.ProvenanceTrusted},
		{"notify.away.digest", provider.ProvenanceTrusted, provider.ProvenanceTrusted},
		{"supervision.stall", provider.ProvenanceUntrustedSource, provider.ProvenanceUntrustedSource},
		{"supervision.stall", "", provider.ProvenanceUntrustedSource},
		{"supervision.stall", "TRUSTED", provider.ProvenanceUntrustedSource},
		{"ci.run", provider.ProvenanceTrusted, provider.ProvenanceUntrustedSource},
		{"Supervision.stall", provider.ProvenanceTrusted, provider.ProvenanceUntrustedSource},
		{"", provider.ProvenanceTrusted, provider.ProvenanceUntrustedSource},
	}
	for _, tc := range cases {
		n := Notification{
			Visibility: VisibilityScoped, Trust: provider.ProvenanceTrusted,
			Origin: Origin{Source: tc.source, Tier: provider.SensitivityInternal, Trust: tc.claim},
		}
		stamp(&n)
		if n.Trust != tc.want {
			t.Errorf("source %q claim %q: Trust = %q, want %q", tc.source, tc.claim, n.Trust, tc.want)
		}
	}
}

// routerHandle feeds one bus event through the router's handle and returns
// the notification it enqueued.
func routerHandle(t *testing.T, kind events.EventKind, p events.NotificationPayload) Notification {
	t.Helper()
	queues := newQueueSet(DefaultConfig(), silentLogger())
	r := NewNotificationRouter(queues, fixedClock{now: time.Unix(7, 0)}, silentLogger())
	r.handle(events.Event{Kind: kind, Payload: events.EncodeNotificationPayload(p)})
	for _, q := range queues.queues {
		select {
		case n := <-q:
			return n
		default:
		}
	}
	t.Fatalf("router enqueued nothing for %s", kind)
	return Notification{}
}

func TestRouterPathIsStamped(t *testing.T) {
	provenanceHome(t)
	n := routerHandle(t, "backup.result", events.NotificationPayload{
		ID: "b1", TargetScope: "s", Visibility: string(VisibilityScoped),
	})
	want := Origin{Source: "backup.result", RecordRef: "b1", Tier: provider.SensitivityInternal,
		Trust: provider.ProvenanceUntrustedSource}
	if n.Origin != want {
		t.Fatalf("Origin = %+v, want %+v", n.Origin, want)
	}
	if n.DataClass != provider.SensitivityInternal || n.Trust != provider.ProvenanceUntrustedSource {
		t.Fatalf("class/trust = %v/%q, want internal/untrusted-source", n.DataClass, n.Trust)
	}
	// An unknown visibility on the wire is never permission.
	u := routerHandle(t, "backup.result", events.NotificationPayload{ID: "b2", TargetScope: "s", Visibility: "world"})
	if u.DataClass != provider.SensitivityLocalOnly {
		t.Fatalf("unknown visibility: DataClass = %v, want local-only", u.DataClass)
	}
}

func TestRaiseDataClassRefusesLower(t *testing.T) {
	provenanceHome(t)
	n := Notification{DataClass: provider.SensitivityRestricted}
	err := RaiseDataClass(&n, provider.SensitivityInternal)
	if err != ErrDataClassLowered { //nolint:errorlint // identity is the contract
		t.Fatalf("lower: err = %v, want ErrDataClassLowered by identity", err)
	}
	if err.Error() != "permission-denied: notify: data class cannot be lowered" {
		t.Fatalf("message = %q", err.Error())
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindPermissionDenied {
		t.Fatalf("kind = %v, want permission-denied", k)
	}
	if n.DataClass != provider.SensitivityRestricted {
		t.Fatalf("refused lower changed DataClass to %v", n.DataClass)
	}
	if err := RaiseDataClass(&n, provider.SensitivityLocalOnly); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if n.DataClass != provider.SensitivityLocalOnly {
		t.Fatalf("raise: DataClass = %v, want local-only", n.DataClass)
	}
	err = RaiseDataClass(&n, provider.SensitivityTier(250))
	if err != ErrDataClassLowered || n.DataClass != provider.SensitivityLocalOnly { //nolint:errorlint // identity
		t.Fatalf("invalid target: err = %v, DataClass = %v; want ErrDataClassLowered, unchanged", err, n.DataClass)
	}
	if err := RaiseDataClass(&n, provider.SensitivityPublic); err != ErrDataClassLowered { //nolint:errorlint // identity
		t.Fatalf("lower from local-only: err = %v", err)
	}
}

func TestInboxRecordKeepsFirstOriginJoinsClass(t *testing.T) {
	provenanceHome(t)
	first := Notification{ID: "x", Class: ClassGlobalCritical, Visibility: VisibilityShared,
		Origin: Origin{Source: "ci.run", RecordRef: "r1", Tier: provider.SensitivityRestricted}}
	stamp(&first)
	second := Notification{ID: "x", Class: ClassGlobalCritical, Visibility: VisibilityShared,
		Origin: Origin{Source: "supervision.stall", RecordRef: "r2", Tier: provider.SensitivityPublic,
			Trust: provider.ProvenanceTrusted}}
	stamp(&second)
	if second.DataClass != provider.SensitivityInternal || second.Trust != provider.ProvenanceTrusted {
		t.Fatalf("fixture: second stamped %v/%q", second.DataClass, second.Trust)
	}
	ib := NewInbox()
	ib.record(first)
	ib.record(second)
	got := ib.records["x"].n
	if got.Origin != first.Origin {
		t.Fatalf("Origin = %+v, want first %+v", got.Origin, first.Origin)
	}
	if got.DataClass != provider.SensitivityRestricted {
		t.Fatalf("lower re-record: DataClass = %v, want restricted", got.DataClass)
	}
	if got.Trust != provider.ProvenanceUntrustedSource {
		t.Fatalf("trusted re-record: Trust = %q, want untrusted-source", got.Trust)
	}
	if k, p := JoinItems(nil); k != provider.SensitivityLocalOnly || p != provider.ProvenanceUntrustedSource {
		t.Fatalf("JoinItems(nil) = %v/%q", k, p)
	}
}

func TestProducerWithoutOriginIsLocalOnly(t *testing.T) {
	provenanceHome(t)
	queues := newQueueSet(DefaultConfig(), silentLogger())
	nf := NewNotifier(queues, fixedClock{now: time.Unix(42, 0)})
	err := nf.Deliver(context.Background(), Notification{
		ID: "p1", Class: ClassGlobalCritical, Priority: PriorityUrgent, Visibility: VisibilityShared,
		DataClass: provider.SensitivityPublic, Trust: provider.ProvenanceTrusted,
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	n := <-queues.queues[PriorityUrgent]
	if n.DataClass != provider.SensitivityLocalOnly || n.Trust != provider.ProvenanceUntrustedSource {
		t.Fatalf("no Origin: class/trust = %v/%q, want local-only/untrusted-source", n.DataClass, n.Trust)
	}
}

func TestRouterStallOriginInternalTrusted(t *testing.T) {
	provenanceHome(t)
	n := routerHandle(t, "supervision.stalled", events.NotificationPayload{
		ID: "s1", TargetScope: "s", Visibility: string(VisibilityScoped),
	})
	if n.Origin.Source != "supervision.stall" || n.Origin.RecordRef != "s1" {
		t.Fatalf("Origin = %+v", n.Origin)
	}
	if n.DataClass != provider.SensitivityInternal || n.Trust != provider.ProvenanceTrusted {
		t.Fatalf("stall: class/trust = %v/%q, want internal/trusted", n.DataClass, n.Trust)
	}
	// requeue keeps the stamped fields.
	queues := newQueueSet(DefaultConfig(), silentLogger())
	r := NewNotificationRouter(queues, fixedClock{now: time.Unix(8, 0)}, silentLogger())
	r.requeue(n)
	back := <-queues.queues[n.Priority]
	if back.Origin != n.Origin || back.DataClass != n.DataClass || back.Trust != n.Trust {
		t.Fatalf("requeue changed provenance: %+v", back)
	}
}
