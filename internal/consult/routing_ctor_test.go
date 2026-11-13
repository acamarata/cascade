package consult

import "testing"

// TestNewContextMemoryStoresPreferences checks the constructor returns a ready memory that round-trips a preference.
func TestNewContextMemoryStoresPreferences(t *testing.T) {
	m := NewContextMemory()
	if err := m.SaveRoutingPreference("ctx", RoutingPreference{Class: "c", Model: "m"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	p, err := m.LoadRoutingPreference("ctx")
	if err != nil || p.Class != "c" || p.Model != "m" {
		t.Fatalf("load = %+v, %v; want c/m", p, err)
	}
}
