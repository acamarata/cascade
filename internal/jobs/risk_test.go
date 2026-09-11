package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func writeProbeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRiskClassifyFootprint_TableDriven(t *testing.T) {
	cases := []struct {
		name         string
		footprint    []string
		repositories []string
		want         RiskClass
	}{
		{"empty footprint", nil, []string{"r1"}, RiskClassNormal},
		{"unresolvable footprint", []string{""}, []string{"r1"}, RiskClassNormal},
		{"docs only", []string{"docs/guide.md", "README.md"}, []string{"r1"}, RiskClassLow},
		{"secrets critical", []string{"internal/secrets/vault.go"}, []string{"r1"}, RiskClassCritical},
		{"policy critical", []string{"internal/policy/matrix.go"}, []string{"r1"}, RiskClassCritical},
		{"elevation critical", []string{"internal/elevation/gate.go"}, []string{"r1"}, RiskClassCritical},
		{"provider auth critical", []string{"providers/anthropic/auth.go"}, []string{"r1"}, RiskClassCritical},
		{"goreleaser critical", []string{".goreleaser.yaml"}, []string{"r1"}, RiskClassCritical},
		{"install.sh critical", []string{"install.sh"}, []string{"r1"}, RiskClassCritical},
		{"migration path high", []string{"internal/storage/migrations/0001.sql"}, []string{"r1"}, RiskClassHigh},
		{"sql path high", []string{"scripts/report.sql"}, []string{"r1"}, RiskClassHigh},
		{"pkg path high", []string{"pkg/provider/model.go"}, []string{"r1"}, RiskClassHigh},
		{"two repositories high, docs only paths", []string{"docs/guide.md"}, []string{"r1", "r2"}, RiskClassHigh},
		{"unrelated code path normal", []string{"internal/fleet/bench.go"}, []string{"r1"}, RiskClassNormal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyFootprint(c.footprint, c.repositories, "")
			if got != c.want {
				t.Fatalf("classifyFootprint(%v, %v) = %v, want %v", c.footprint, c.repositories, got, c.want)
			}
		})
	}
}

// TestCriticalFloor asserts each of the five R-21.182 floor categories
// classifies Critical, and that the floor is evaluated ahead of the
// R-16.37 path set (a docs-only path that ALSO matches a floor category
// stays Critical, never downgraded to Low by the later rule).
func TestCriticalFloor(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"gate table json", "internal/build/testonly-allow.json"},
		{"gate table go", "internal/build/testonlygate.go"},
		{"classifier table risk.go", "internal/jobs/risk.go"},
		{"classifier table riskgates.go", "internal/jobs/riskgates.go"},
		{"classifier table task_classes.go", "internal/conductor/task_classes.go"},
		{"policy file", "internal/policy/approval_matrix.go"},
		{"egress-class file", "internal/secrets/egress.go"},
		{"generated harness artifact template", "internal/repo/templates/hook.tmpl"},
		{"generated harness artifact workflow", ".github/workflows/ci.yml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := criticalFloorMatch(c.path); !ok {
				t.Fatalf("criticalFloorMatch(%q) = false, want a floor match", c.path)
			}
			if got := classifyFootprint([]string{c.path}, []string{"r1"}, ""); got != RiskClassCritical {
				t.Fatalf("classifyFootprint(%q) = %v, want critical", c.path, got)
			}
		})
	}
}

// TestCriticalFloor_Unlowerable asserts a floor hit is never overridden
// by a later rule: an .md path (which alone would be Low) inside a
// floor category stays Critical.
func TestCriticalFloor_Unlowerable(t *testing.T) {
	got := classifyFootprint([]string{"internal/policy/README.md"}, []string{"r1"}, "")
	if got != RiskClassCritical {
		t.Fatalf("classifyFootprint(policy .md) = %v, want critical (floor unlowerable)", got)
	}
}

func TestRiskClassifyFootprint_ContentProbes(t *testing.T) {
	root := t.TempDir()
	writeProbeFile(t, root, "internal/storage/migrations/0002_alter.sql", "ALTER TABLE jobs_job ADD COLUMN x INT;")
	writeProbeFile(t, root, "internal/fleet/racey.go", "import \"sync/atomic\"\nfunc f() {}\n")
	writeProbeFile(t, root, "internal/fleet/goroutine.go", "func f() { go func() {}() }\n")

	t.Run("drop-alter migration is critical", func(t *testing.T) {
		got := classifyFootprint([]string{"internal/storage/migrations/0002_alter.sql"}, []string{"r1"}, root)
		if got != RiskClassCritical {
			t.Fatalf("got %v, want critical", got)
		}
	})
	t.Run("sync/atomic import is high", func(t *testing.T) {
		got := classifyFootprint([]string{"internal/fleet/racey.go"}, []string{"r1"}, root)
		if got != RiskClassHigh {
			t.Fatalf("got %v, want high", got)
		}
	})
	t.Run("go func marker is high", func(t *testing.T) {
		got := classifyFootprint([]string{"internal/fleet/goroutine.go"}, []string{"r1"}, root)
		if got != RiskClassHigh {
			t.Fatalf("got %v, want high", got)
		}
	})
	t.Run("absent path skips content probe, no error", func(t *testing.T) {
		got := classifyFootprint([]string{"internal/storage/migrations/does-not-exist.sql"}, []string{"r1"}, root)
		if got != RiskClassHigh { // path-rule match on /migrations/ still applies
			t.Fatalf("got %v, want high from the path rule alone", got)
		}
	})
}

// TestReachabilitySeam covers the nil path (pre/post-image footprint
// only), the injected path (widens the union into a floor path), and
// the error path (fails closed to Critical).
func TestReachabilitySeam(t *testing.T) {
	t.Run("nil seam yields path-only union", func(t *testing.T) {
		got, err := unionFootprint(context.Background(), []string{"a.go", "b.go"}, nil)
		if err != nil {
			t.Fatalf("unionFootprint() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("unionFootprint() = %v, want the two input paths unchanged", got)
		}
	})

	t.Run("injected seam widens union into a floor path", func(t *testing.T) {
		seam := func(_ context.Context, _ []string) ([]string, error) {
			return []string{"internal/policy/matrix.go"}, nil
		}
		union, err := unionFootprint(context.Background(), []string{"a.go"}, seam)
		if err != nil {
			t.Fatalf("unionFootprint() error = %v", err)
		}
		got := classifyFootprint(union, []string{"r1"}, "")
		if got != RiskClassCritical {
			t.Fatalf("classifyFootprint(widened union) = %v, want critical", got)
		}
	})

	t.Run("seam error fails closed to critical via Planner.classify", func(t *testing.T) {
		seam := func(_ context.Context, _ []string) ([]string, error) {
			return nil, cascade.New(cascade.KindUnavailable, "graph store unreachable")
		}
		_, err := unionFootprint(context.Background(), []string{"a.go"}, seam)
		if err == nil {
			t.Fatal("unionFootprint() error = nil, want the seam's error propagated")
		}
		// The Critical-floor fail-closed behavior itself is exercised at
		// the Planner.classify call site: see
		// TestPlanner_Plan_ReachabilitySeamErrorFailsClosedToCritical.
	})
}

// FuzzRiskClassifier asserts classifyFootprint never panics over an
// arbitrary footprint/repository-count and always resolves to one of
// the four closed RiskClass members, for any content bytes an existing
// probed file might contain.
func FuzzRiskClassifier(f *testing.F) {
	f.Add("internal/secrets/vault.go\ndocs/guide.md", 1, "")
	f.Add("internal/policy/matrix.go", 2, "ALTER TABLE x")
	f.Add("pkg/provider/model.go\ninternal/storage/migrations/0001.sql", 1, "go func() {}()")
	f.Add("", 0, "")
	f.Add(".goreleaser.yaml\ninstall.sh\nREADME.md", 5, "sync/atomic")

	f.Fuzz(func(t *testing.T, rawPaths string, repoCount int, probeContent string) {
		footprint := strings.Split(rawPaths, "\n")

		n := repoCount % 8
		if n < 0 {
			n = -n
		}
		repositories := make([]string, 0, n)
		for i := 0; i < n; i++ {
			repositories = append(repositories, "repo-"+string(rune('a'+i%26)))
		}

		root := t.TempDir()
		for i, p := range footprint {
			if p == "" {
				continue
			}
			// Best-effort only: two fuzz-generated paths can collide
			// (one a prefix-directory of another), which is a valid
			// filesystem conflict, not a classifier defect. A failed
			// write just means that path's content probe sees nothing,
			// exactly like any other absent path (HOW-4's own rule).
			full := filepath.Join(root, filepathClean(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				continue
			}
			_ = os.WriteFile(full, []byte(probeContent+string(rune('0'+i%10))), 0o644)
		}

		got := classifyFootprint(footprint, repositories, root)
		switch got {
		case RiskClassLow, RiskClassNormal, RiskClassHigh, RiskClassCritical:
			// one of the four closed members, as required.
		default:
			t.Fatalf("classifyFootprint returned an unknown RiskClass %q", got)
		}
	})
}

// filepathClean guards writeProbeFile against a fuzz-supplied path
// escaping t.TempDir() (e.g. "../../etc/passwd") by stripping any
// leading ".." segment and absolute-path markers -- the fuzz corpus is
// arbitrary text, not a trusted path.
func filepathClean(p string) string {
	p = strings.TrimPrefix(p, "/")
	for strings.Contains(p, "..") {
		p = strings.ReplaceAll(p, "..", "_")
	}
	if p == "" {
		p = "empty"
	}
	return p
}
