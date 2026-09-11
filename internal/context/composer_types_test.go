package context

import "testing"

func TestSlotKindValidAndString(t *testing.T) {
	cases := []struct {
		kind  SlotKind
		valid bool
		str   string
	}{
		{SlotKind(0), false, "invalid-slot-kind"},
		{SlotKindTier, true, "tier"},
		{SlotKindMemory, true, "memory"},
		{SlotKindRetrieval, true, "retrieval"},
		{SlotKindHistory, true, "history"},
		{SlotKindHistory + 1, false, "invalid-slot-kind"},
	}
	for _, c := range cases {
		if got := c.kind.Valid(); got != c.valid {
			t.Errorf("SlotKind(%d).Valid() = %v, want %v", c.kind, got, c.valid)
		}
		if got := c.kind.String(); got != c.str {
			t.Errorf("SlotKind(%d).String() = %q, want %q", c.kind, got, c.str)
		}
	}
}

func TestZeroSlotsEventIsComparable(t *testing.T) {
	a := ZeroSlotsEvent{}
	b := ZeroSlotsEvent{}
	if a != b {
		t.Fatal("ZeroSlotsEvent must be comparable and equal for two zero values")
	}
}
