package daemon

// Purpose (this file): proves the dedup key (status_widget_key.go) ignores
// exactly what it must (seq, generated_at, updated_at, a reset countdown)
// and sees everything else.
//
// SPORT: daemon.status_widget (tests, P1-WID-08).

import (
	"bytes"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
)

func keyOf(t *testing.T, s capacity.WidgetSnapshot) []byte {
	t.Helper()
	k, err := widgetChangeKey(s)
	if err != nil {
		t.Fatalf("widgetChangeKey: %v", err)
	}
	return k
}

// keyFixture is one exhausted row with a 2h reset, composed at harnessNow.
func keyFixture() capacity.WidgetSnapshot {
	reset, pct := 2*time.Hour, 41.5
	return capacity.WidgetSnapshot{
		GeneratedAt:    harnessNow,
		Seq:            3,
		AttentionCount: 1,
		Rows: []capacity.WidgetRow{{
			Ref: "a", Label: "a", Kind: "profile", State: "exhausted", UpdatedAt: harnessNow,
			FiveHour: &capacity.WidgetWindow{UtilizationPct: &pct, ResetsIn: &reset}, SevenDay: &capacity.WidgetWindow{},
		}},
	}
}

func TestStatusWidgetChangeKeyIgnoresTimeAndSeq(t *testing.T) {
	base := keyOf(t, keyFixture())
	later := keyFixture()
	later.Seq, later.GeneratedAt = 9, harnessNow.Add(10*time.Second)
	later.Rows[0].UpdatedAt = later.GeneratedAt
	countdown := 2*time.Hour - 10*time.Second // the same estimate, ten seconds on
	later.Rows[0].FiveHour = &capacity.WidgetWindow{UtilizationPct: later.Rows[0].FiveHour.UtilizationPct, ResetsIn: &countdown}
	if !bytes.Equal(base, keyOf(t, later)) {
		t.Fatalf("key moved with seq, generated_at, updated_at or the countdown:\n%s\n%s", base, keyOf(t, later))
	}
}

func TestStatusWidgetChangeKeySeesRealChanges(t *testing.T) {
	base := keyOf(t, keyFixture())
	mutations := map[string]func(*capacity.WidgetSnapshot){
		"state":     func(s *capacity.WidgetSnapshot) { s.Rows[0].State = "available" },
		"attention": func(s *capacity.WidgetSnapshot) { s.AttentionCount = 2 },
		"estimate": func(s *capacity.WidgetSnapshot) {
			d := 3 * time.Hour // a different reset instant at the same generated_at
			s.Rows[0].FiveHour = &capacity.WidgetWindow{ResetsIn: &d}
		},
		"cleared reset": func(s *capacity.WidgetSnapshot) { s.Rows[0].FiveHour = &capacity.WidgetWindow{} },
		"utilization": func(s *capacity.WidgetSnapshot) {
			p := 50.0
			s.Rows[0].FiveHour = &capacity.WidgetWindow{UtilizationPct: &p, ResetsIn: s.Rows[0].FiveHour.ResetsIn}
		},
		"jobs": func(s *capacity.WidgetSnapshot) { n := 2; s.ActiveJobsCount = &n },
		"node": func(s *capacity.WidgetSnapshot) {
			s.Nodes = []capacity.NodePresenceSummary{{ID: "n", Presence: "reachable"}}
		},
		"ref":    func(s *capacity.WidgetSnapshot) { s.Rows[0].Ref = "b" },
		"reauth": func(s *capacity.WidgetSnapshot) { s.Rows[0].ReauthRequired = true },
	}
	for name, mutate := range mutations {
		s := keyFixture()
		mutate(&s)
		if bytes.Equal(base, keyOf(t, s)) {
			t.Errorf("a change in %s left the key unchanged", name)
		}
	}
}
