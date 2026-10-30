package build

// Purpose: the guarded-config coverage gate is green on the real tree and
// red, naming file and key, on each seeded unmodeled read: a second decoder
// under [policy], a new file reading [nodes].allow_remote, a helper reading
// [conductor] through a const name, a whole-tree range without a row, a
// stale or reasonless exemption row, and the shapes in shapes.go.txt (a
// comparison, a helper result indexed, a package-level func literal, a
// handoff to encoding/json, a local section name, a parent table handed to a
// helper, a range over an alias of the tree, the whole tree handed to
// encoding/json, a range over maps.All of the tree, the tree laundered through a
// module wrapper, a stdlib call nested in a module one, an unsafe use hidden by a
// safe one on the same line, a computed section key, the tree handed to a
// helper that marshals it, a method expression's shifted argument, and a key
// local rewritten by a range clause or through a pointer).
// SPORT: internal/build guarded config gate tests.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func guardedConfigFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fixture\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for dest, name := range map[string]string{
		"internal/runtime/runtime.go": "runtime", "internal/policy/policy.go": "policy",
		"cmd/app/families.go": "families", "cmd/app/newfile.go": "newfile", "cmd/app/helper.go": "helper",
		"cmd/app/shapes.go": "shapes", "plugins/none/none.go": "none", "cmd/app/hooks_unmodeled.go": "hooks_unmodeled",
	} {
		body, err := os.ReadFile(filepath.Join("testdata", "seeded-violations", "guarded-config", name+".go.txt"))
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGuardedConfigGate_RealTreeGreen(t *testing.T) {
	got, err := UnmodeledGuardedKeys(archModuleRoot(t))
	if err != nil {
		t.Fatalf("guarded config gate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("guarded config reads no family models (%d):\n%s", len(got), strings.Join(got, "\n"))
	}
}

// guardedSeededFindings is every finding the seeded fixture must produce.
var guardedSeededFindings = []string{
	"cmd/app/hooks_unmodeled.go:10: hooks",
	"cmd/app/helper.go:11: conductor",
	"cmd/app/newfile.go:8: nodes.allow_remote",
	"internal/policy/policy.go:16: policy.bypass",
	"cmd/app/shapes.go:14: nodes.allow_remote",
	"cmd/app/shapes.go:24: nodes.allow_remote",
	"cmd/app/shapes.go:29: nodes.tier",
	"cmd/app/shapes.go:34: nodes",
	"cmd/app/shapes.go:41: nodes",
	"cmd/app/shapes.go:48: agents",
	"cmd/app/shapes.go:54: *",
	"cmd/app/shapes.go:62: *",
	"cmd/app/shapes.go:68: *",
	"cmd/app/shapes.go:80: *",
	"cmd/app/shapes.go:89: *",
	"cmd/app/shapes.go:94: *",
	"cmd/app/shapes.go:98: *",
	"cmd/app/shapes.go:102: *",
	"cmd/app/shapes.go:107: *",
	"cmd/app/shapes.go:107: nodes",
	"cmd/app/shapes.go:118: *",
	"cmd/app/shapes.go:125: *",
	"cmd/app/shapes.go:132: *",
}

func TestGuardedConfigGate_SeededRed(t *testing.T) {
	root := guardedConfigFixture(t)
	reasoned := guardedExemption{"cmd/app/helper.go", "*", "fixture: counts the tables, reads no value"}
	stale := guardedExemption{"cmd/app/gone.go", "nodes.allow_remote", "the file no longer exists"}
	cases := []struct {
		name   string
		exempt []guardedExemption
		extra  []string
	}{
		{"stale row", []guardedExemption{reasoned, stale},
			[]string{"cmd/app/gone.go: exemption nodes.allow_remote matches no unmodeled read (stale row, or no reason)"}},
		{"range without a row", nil, []string{"cmd/app/helper.go:17: *"}},
		{"row without a reason", []guardedExemption{{"cmd/app/helper.go", "*", ""}}, []string{
			"cmd/app/helper.go: exemption * matches no unmodeled read (stale row, or no reason)", "cmd/app/helper.go:17: *"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func(e []guardedExemption, b []string) { guardedConfigExemptions, guardedConfigBuiltins = e, b }(guardedConfigExemptions, guardedConfigBuiltins)
			guardedConfigExemptions, guardedConfigBuiltins = tc.exempt, nil
			got, err := UnmodeledGuardedKeys(root)
			if err != nil {
				t.Fatal(err)
			}
			want := append(slices.Clone(guardedSeededFindings), tc.extra...)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// TestGuardedConfigGate_FailsClosed: a module with no family registration,
// or a package that does not type-check, is an error, never a green pass.
func TestGuardedConfigGate_FailsClosed(t *testing.T) {
	root := guardedConfigFixture(t)
	if err := os.Remove(filepath.Join(root, "cmd", "app", "families.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := UnmodeledGuardedKeys(root); err == nil {
		t.Fatal("no registration passed the gate")
	}
	broken := guardedConfigFixture(t)
	if err := os.WriteFile(filepath.Join(broken, "cmd", "app", "broken.go"), []byte("package main\n\nfunc broken() { undefinedCall() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UnmodeledGuardedKeys(broken); err == nil {
		t.Fatal("a package that does not type-check passed the gate")
	}
}
