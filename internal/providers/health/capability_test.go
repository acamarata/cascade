package health

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestCapabilityKindValid(t *testing.T) {
	valid := []CapabilityKind{
		CapabilitySearch, CapabilityURLFetch, CapabilityVision,
		CapabilityToolUse, CapabilityLongContext, CapabilityStructuredOutput,
	}
	for _, k := range valid {
		if !k.Valid() {
			t.Fatalf("%q should be valid", k)
		}
	}
	if CapabilityKind("bogus").Valid() {
		t.Fatal("bogus capability should be invalid")
	}
}

func TestMarkCapabilityUnsupportedRejectsInvalidKind(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	if err := mgr.MarkCapabilityUnsupported(context.Background(), "p1", CapabilityKind("bogus")); err == nil {
		t.Fatal("expected an error for an invalid CapabilityKind")
	}
}

// TestMarkCapabilityUnsupportedEveryDimension exercises all six
// dimensions and asserts each is marked without touching demotion_count,
// health_status, or the other five dimensions.
func TestMarkCapabilityUnsupportedEveryDimension(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 3)
	ctx := context.Background()

	kinds := []CapabilityKind{
		CapabilitySearch, CapabilityURLFetch, CapabilityVision,
		CapabilityToolUse, CapabilityLongContext, CapabilityStructuredOutput,
	}
	for _, k := range kinds {
		name := "p-" + string(k)
		rec := sampleProvider(name)
		rec.Capabilities = provider.Capabilities{
			Search: provider.CapabilitySupported, URLFetch: provider.CapabilitySupported,
			Vision: provider.CapabilitySupported, ToolUse: provider.CapabilitySupported,
			LongContext: provider.CapabilitySupported, StructuredOutput: provider.CapabilitySupported,
		}
		if err := reg.UpsertProvider(ctx, rec); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := mgr.MarkCapabilityUnsupported(ctx, name, k); err != nil {
			t.Fatalf("MarkCapabilityUnsupported(%s): %v", k, err)
		}
		got, err := reg.GetProvider(ctx, name)
		if err != nil {
			t.Fatalf("GetProvider: %v", err)
		}
		if got.DemotionCount != 0 || got.HealthStatus != registry.HealthUnknown {
			t.Fatalf("%s: capability marking mutated health state: %+v", k, got)
		}
		if got := capabilityValue(got.Capabilities, k); got != provider.CapabilityUnsupported {
			t.Fatalf("%s: capability not marked unsupported: %v", k, got)
		}
	}
}

func TestMarkCapabilityUnsupportedUnknownProviderRefused(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, _, _ := newTestManager(t, clk, 3)
	if err := mgr.MarkCapabilityUnsupported(context.Background(), "ghost", CapabilitySearch); err == nil {
		t.Fatal("expected ErrProviderNotFound for an unknown provider")
	}
}

func capabilityValue(caps provider.Capabilities, k CapabilityKind) provider.CapabilityState {
	switch k {
	case CapabilitySearch:
		return caps.Search
	case CapabilityURLFetch:
		return caps.URLFetch
	case CapabilityVision:
		return caps.Vision
	case CapabilityToolUse:
		return caps.ToolUse
	case CapabilityLongContext:
		return caps.LongContext
	case CapabilityStructuredOutput:
		return caps.StructuredOutput
	}
	return provider.CapabilityUnknown
}
