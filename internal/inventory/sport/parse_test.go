// SPORT: internal.inventory.sport.ParseMarkerLine/ADDED (tests).
package sport

import "testing"

func TestParseMarkerLine_SlashAttachedStatus(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "internal.storage.cache.Cache/ADDED (P1-E02-W1-S02-T4).")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].Name != "internal.storage.cache.Cache" || got[0].Status != "ADD" || got[0].Ticket != "P1-E02-W1-S02-T4" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseMarkerLine_ColonAttachedStatus(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "MCP_RESPONSE_FIREWALL: ADD (internal/mcp response-marshal egress routing)")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].Name != "MCP_RESPONSE_FIREWALL" || got[0].Status != "ADD" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseMarkerLine_MultiEntityTopLevelCommas(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "a/ADDED, b/ADDED, c/ADDED")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entities, got %d: %+v", len(got), got)
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Name != want || got[i].Status != "ADD" {
			t.Errorf("entity %d = %+v, want name %q ADD", i, got[i], want)
		}
	}
}

func TestParseMarkerLine_NestedCommaNotSplit(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "cmd/cascade/node (test fakes, P1-E17-W4-S36-T4).")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].Name != "cmd/cascade/node" || got[0].Status != StatusUnspecified {
		t.Fatalf("got %+v", got)
	}
}

func TestParseMarkerLine_PlaceholderPrefix(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "placeholder: doctor/bundle (ADD).")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].Name != "doctor/bundle" || got[0].Status != "ADD" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseMarkerLine_ContinuationStatusInfersPriorName(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2), CHANGED")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entities, got %d: %+v", len(got), got)
	}
	if got[1].Name != "providers.sqlite.Driver" || got[1].Status != "CHANGE" {
		t.Fatalf("second entity = %+v, want inferred name providers.sqlite.Driver / CHANGE", got[1])
	}
}

func TestParseMarkerLine_BareStatusFirstIsMalformed(t *testing.T) {
	_, errs := ParseMarkerLine("f.go", 1, "CHANGED")
	if len(errs) != 1 {
		t.Fatalf("want exactly 1 malformed error, got %v", errs)
	}
	if errs[0].File != "f.go" || errs[0].Line != 1 {
		t.Errorf("error site = %+v", errs[0])
	}
}

func TestParseMarkerLine_NoStatusWholeLineIsOneEntity(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "cmd/cascade — cobra-root, global-flags, version, completions.")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].Status != StatusUnspecified {
		t.Fatalf("want exactly one unspecified entity, got %+v", got)
	}
}

func TestParseMarkerLine_ChgAbbreviationNormalizesToChange(t *testing.T) {
	got, errs := ParseMarkerLine("f.go", 1, "cmd/cascade/node (CHG: list/status/drain).")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].Status != "CHANGE" {
		t.Fatalf("got %+v", got)
	}
}
