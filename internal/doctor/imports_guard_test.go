package doctor

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenDeps lists every dependency of pkg that is internal/fleet,
// internal/daemon or a package under either. A go list failure fails
// the test; it never skips.
func forbiddenDeps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatalf("go list -deps %s returned nothing", pkg)
	}
	var got []string
	for _, p := range deps {
		p = strings.TrimPrefix(p, "github.com/acamarata/cascade/")
		for _, bad := range []string{"internal/fleet", "internal/daemon"} {
			if p == bad || strings.HasPrefix(p, bad+"/") {
				got = append(got, p)
			}
		}
	}
	return got
}

func TestDoctorReachesNoFleet(t *testing.T) {
	if got := forbiddenDeps(t, "."); len(got) != 0 {
		t.Fatalf("internal/doctor reaches %v; topology code belongs in internal/doctor/topologycheck", got)
	}
	leaf := forbiddenDeps(t, "./topologycheck")
	found := false
	for _, p := range leaf {
		if p == "internal/fleet/topology" {
			found = true
		}
	}
	if !found {
		t.Fatalf("topologycheck deps under fleet/daemon = %v, want internal/fleet/topology among them", leaf)
	}
}
