package build

// Purpose: the caller-site gate judges a promise by its retire ticket's
// state. A row whose ticket is still open may point at a file that exists
// without the call yet; a closed or unknown ticket gets no such grace.

import (
	"testing"
)

// openStateBrokenRows returns the broken rows for one silent-caller entry
// whose retire ticket is rt, given the known and closed ticket sets.
func openStateBrokenRows(t *testing.T, rt string, known, closed map[string]bool) []BrokenCallerSite {
	t.Helper()
	root := t.TempDir()
	writeCallersiteFixtureModule(t, root)
	e := callersiteEntry("internal/widget.Farewell", "internal/app/silent.go")
	e.RetireTicket = rt
	allow := map[string]TestOnlyAllowEntry{"internal/widget.Farewell": e}
	broken, err := checkCallerSitesByTicketState(root, callersiteFixtureModulePath, allow, known, closed)
	if err != nil {
		t.Fatalf("checkCallerSitesByTicketState: %v", err)
	}
	return broken
}

func TestCallerSiteOpenRetireTicketPasses(t *testing.T) {
	known := map[string]bool{"X-01.T1": true}
	if broken := openStateBrokenRows(t, "X-01.T1", known, map[string]bool{}); len(broken) != 0 {
		t.Fatalf("open retire ticket: broken = %+v, want none", broken)
	}
}

func TestCallerSiteClosedRetireTicketFails(t *testing.T) {
	known := map[string]bool{"X-01.T1": true}
	closed := map[string]bool{"X-01.T1": true}
	broken := openStateBrokenRows(t, "X-01.T1", known, closed)
	if len(broken) != 1 || broken[0].Symbol != "internal/widget.Farewell" {
		t.Fatalf("closed retire ticket: broken = %+v, want the Farewell row", broken)
	}
}

func TestCallerSiteUnknownRetireTicketFails(t *testing.T) {
	known := map[string]bool{"X-02.T1": true}
	broken := openStateBrokenRows(t, "X-01.T1", known, map[string]bool{})
	if len(broken) != 1 {
		t.Fatalf("unknown retire ticket: broken = %+v, want 1 row", broken)
	}
}
