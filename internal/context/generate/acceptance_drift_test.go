package generate

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/policy"
)

// driftFault is one fault injected into a generated tree and the one
// drift item Check must report for it.
type driftFault struct {
	name, path, cond string
	inject           func(t *testing.T, root string)
}

// driftFaults returns the four faults, one per Check condition.
func driftFaults() []driftFault {
	kernelLine := strings.SplitAfter(policy.KernelText, "\n")[0]
	return []driftFault{
		{"block edited", ".claude/CLAUDE.md", condBlockEdited, func(t *testing.T, root string) {
			data := slurp(t, root, ".claude/CLAUDE.md")
			if !strings.Contains(data, kernelLine) {
				t.Fatal("the kernel line is not in CLAUDE.md; the edit would not land in a block")
			}
			put(t, root, ".claude/CLAUDE.md", strings.Replace(data, kernelLine, "SCOPE: anything goes.\n", 1))
		}},
		{"file missing", "opencode.json", condFileMissing, func(t *testing.T, root string) {
			mustRemove(t, root, "opencode.json")
		}},
		{"mangled marker", ".claude/rules/root.md", condMangledMarker, func(t *testing.T, root string) {
			data := slurp(t, root, ".claude/rules/root.md")
			end := FormMarkdown.endLine() + "\n"
			if !strings.Contains(data, end) {
				t.Fatal("no end marker in the rules file")
			}
			put(t, root, ".claude/rules/root.md", strings.Replace(data, end, "", 1))
		}},
		{"manifest absent", ManifestRel, condManifestAbsent, func(t *testing.T, root string) {
			mustRemove(t, root, ManifestRel)
		}},
	}
}

func TestGenerateCheckDrift(t *testing.T) {
	root := acceptanceRepo(t)
	acceptanceRun(t, root, fixedNow, &recorder{})
	if d, err := Check(context.Background(), root); err != nil || !d.Clean() {
		t.Fatalf("Check after Run = %+v %v", d, err)
	}
	put(t, root, "AGENTS.md", slurp(t, root, "AGENTS.md")+"\nhand-written note\n")
	put(t, root, ".claude/CLAUDE.md", "# My preface\n\n"+slurp(t, root, ".claude/CLAUDE.md"))
	if d, err := Check(context.Background(), root); err != nil || !d.Clean() {
		t.Fatalf("an edit outside the markers turned Check red: %+v %v", d, err)
	}
	for _, f := range driftFaults() {
		t.Run(f.name, func(t *testing.T) {
			root := acceptanceRepo(t)
			acceptanceRun(t, root, fixedNow, &recorder{})
			f.inject(t, root)
			d, err := Check(context.Background(), root)
			if err != nil || len(d.Items) != 1 || d.Items[0] != (DriftItem{Path: f.path, Condition: f.cond}) {
				t.Fatalf("drift = %+v %v, want exactly {%s %s}", d, err, f.path, f.cond)
			}
		})
	}
}
