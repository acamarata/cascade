package doctor

import (
	"context"
	"reflect"
	"testing"
)

// groupedFake is a fakeCheck that declares a group.
type groupedFake struct {
	fakeCheck
	group string
}

func (g *groupedFake) Group() string { return g.group }

func providersFixture() []Check {
	return []Check{
		&groupedFake{fakeCheck: fakeCheck{name: "prov_a"}, group: GroupProviders},
		&groupedFake{fakeCheck: fakeCheck{name: "prov_b"}, group: GroupProviders},
		&groupedFake{fakeCheck: fakeCheck{name: "other_group"}, group: "storage"},
		&fakeCheck{name: "ungrouped"},
	}
}

func entryNames(r RunReport) []string {
	out := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, e.Name)
	}
	return out
}

func TestRunProvidersOnlyKeepsGroupProviders(t *testing.T) {
	ctx := context.Background()
	only := Run(ctx, providersFixture(), RunOptions{ProvidersOnly: true, Clock: fixedTestClock()})
	if got, want := entryNames(only), []string{"prov_a", "prov_b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ProvidersOnly entries = %v, want %v", got, want)
	}
	all := Run(ctx, providersFixture(), RunOptions{Clock: fixedTestClock()})
	want := []string{"other_group", "prov_a", "prov_b", "ungrouped"}
	if got := entryNames(all); !reflect.DeepEqual(got, want) {
		t.Fatalf("unfiltered entries = %v, want %v", got, want)
	}
	if got := providerChecks(nil); len(got) != 0 {
		t.Fatalf("providerChecks(nil) = %v, want empty", got)
	}
}
