// Purpose (this file): proves the auto-sync workflow template
//
//	(internal/repo/templates/wiki-sync-workflow.yml) is valid GitHub
//	Actions workflow YAML, triggers on push to main filtered to
//	.github/wiki/**, and invokes `cascade github wiki sync --yes` — the
//	ticket's "template validity is test-asserted (parses as workflow
//	YAML, invokes the sync command with --yes)" acceptance criterion.
//
// Constraints: reads the template by its real tracked path (relative from
//
//	this package's directory) rather than a copy, so a future edit to the
//	template is what this test actually verifies, never a stale
//	duplicate.
//
// SPORT: plugins/github/wiki:workflow-template (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// templatePath is the tracked location R-21.187 names: compiled-in
// workflow bodies live only under internal/repo/templates/.
const templatePath = "../../../internal/repo/templates/wiki-sync-workflow.yml"

// workflowStep is the subset of a GitHub Actions step this test cares
// about.
type workflowStep struct {
	Name string `yaml:"name"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

// workflowJob is the subset of a job this test cares about.
type workflowJob struct {
	RunsOn string         `yaml:"runs-on"`
	Steps  []workflowStep `yaml:"steps"`
}

// pushTrigger is the `on.push` shape this test cares about.
type pushTrigger struct {
	Branches []string `yaml:"branches"`
	Paths    []string `yaml:"paths"`
}

// onTrigger wraps push. gopkg.in/yaml.v3 decodes the bare `on:` map key
// as the plain string "on" (not the YAML-1.1 boolean some other decoders
// resolve it to), which TestTemplate_TriggersOnPushToMainFilteredToWiki
// below proves by asserting the fields this struct actually populated.
type onTrigger struct {
	Push pushTrigger `yaml:"push"`
}

// workflowDoc is the minimal shape this test decodes; a real GitHub
// Actions workflow carries more (env, permissions, …), all of it beyond
// this ticket's own acceptance criterion.
type workflowDoc struct {
	Name string                 `yaml:"name"`
	On   onTrigger              `yaml:"on"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

func loadWorkflowTemplate(t *testing.T) workflowDoc {
	t.Helper()
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read %s: %v", templatePath, err)
	}
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s does not parse as valid workflow YAML: %v", templatePath, err)
	}
	return doc
}

// TestTemplate_ParsesAsValidWorkflowYAML is the base validity assertion.
func TestTemplate_ParsesAsValidWorkflowYAML(t *testing.T) {
	doc := loadWorkflowTemplate(t)
	if doc.Name == "" {
		t.Error("workflow has no name")
	}
	if len(doc.Jobs) == 0 {
		t.Fatal("workflow declares no jobs")
	}
}

// TestTemplate_TriggersOnPushToMainFilteredToWiki asserts the exact
// trigger the ticket names: "triggers on push to main (paths filter
// .github/wiki/**)".
func TestTemplate_TriggersOnPushToMainFilteredToWiki(t *testing.T) {
	doc := loadWorkflowTemplate(t)
	if !containsString(doc.On.Push.Branches, "main") {
		t.Errorf("on.push.branches = %v, want it to include %q", doc.On.Push.Branches, "main")
	}
	if !containsString(doc.On.Push.Paths, ".github/wiki/**") {
		t.Errorf("on.push.paths = %v, want it to include %q", doc.On.Push.Paths, ".github/wiki/**")
	}
}

// TestTemplate_InvokesSyncWithYes asserts the ticket's other half:
// "runs `cascade github wiki sync --yes`".
func TestTemplate_InvokesSyncWithYes(t *testing.T) {
	doc := loadWorkflowTemplate(t)
	var found string
	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "cascade github wiki sync") {
				found = step.Run
			}
		}
	}
	if found == "" {
		t.Fatal("no step runs `cascade github wiki sync`")
	}
	if !strings.Contains(found, "--yes") {
		t.Fatalf("sync step run = %q, want it to pass --yes", found)
	}
}

// TestTemplate_ActionsArePinnedToACommitSHA matches this repo's own
// ci.yml/release.yml convention: every `uses:` is a 40-character SHA,
// never a floating tag, so a compromised tag cannot silently swap in
// different action code.
func TestTemplate_ActionsArePinnedToACommitSHA(t *testing.T) {
	doc := loadWorkflowTemplate(t)
	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			at := strings.LastIndex(step.Uses, "@")
			if at < 0 {
				t.Errorf("step %q uses %q with no @ref at all", step.Name, step.Uses)
				continue
			}
			ref := step.Uses[at+1:]
			if len(ref) != 40 {
				t.Errorf("step %q uses %q, ref %q is not a 40-character commit SHA", step.Name, step.Uses, ref)
			}
		}
	}
}
