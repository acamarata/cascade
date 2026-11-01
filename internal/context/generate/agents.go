package generate

// Purpose: RenderAgentsSkills, the generator for the four specialist
//   agents and the five skills (contract:compiled-templates). Each file is
//   the template's frontmatter followed by one managed block holding the
//   template body with the View values filled in.
// Inputs: a View from BuildView; the compiled-in repo.TemplateFS only.
// Outputs: nine Rendered files in a fixed order, generator ids agents and
//   skills, block ids agent-<name> and skill-<name>.
// Constraints: no template is read from the target repository, a config
//   path, a plugin or the network. The set is closed: a missing or extra
//   template is KindIntegrity. Template data is only View values and
//   policy.KernelVersion, each emitted as inline code (skills.go), so no
//   value can add Markdown structure. Frontmatter is template text only and
//   never carries a hooks key. No clock.
// SPORT: context-engine/agents-skills-generator (ADD, P1-GEN-08).

import (
	"bytes"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The generator ids RenderAgentsSkills records in the manifest.
const (
	genAgents = "agents"
	genSkills = "skills"
)

// The target directories and the template roots inside repo.TemplateFS.
const (
	agentsDir      = ".claude/agents/"
	skillsDir      = ".claude/skills/"
	agentTemplates = "templates/agents"
	skillTemplates = "templates/skills"
	skillFile      = "SKILL.md"
)

// agentNames and skillNames are the closed generated set, in render order.
var (
	agentNames = []string{"architecture-reviewer", "test-engineer", "security-reviewer", "adversarial-reviewer"}
	skillNames = []string{"init", "plan", "verify", "delegate", "cascade-execution"}
)

// frontKeys are the frontmatter keys each kind carries, the keys of the
// captured real-CC fixtures in testdata/real-cc-agents/.
var (
	agentFrontKeys = []string{"name", "description", "tools"}
	skillFrontKeys = []string{"name", "description"}
)

// frontKey matches a top-level frontmatter key line.
var frontKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):`)

// tmplSpec is one template to render.
type tmplSpec struct {
	src, target, generator, block, name string
	keys                                []string
}

// RenderAgentsSkills renders .claude/agents/<name>.md for the four agents
// and .claude/skills/<name>/SKILL.md for the five skills. Equal views
// render equal bytes. A view that names no build, test or lint command is
// KindInvalidInput.
func RenderAgentsSkills(v View) ([]Rendered, error) {
	vals, err := templateValues(v)
	if err != nil {
		return nil, err
	}
	fsys := repo.TemplateFS()
	if err := closedSet(fsys); err != nil {
		return nil, err
	}
	out := make([]Rendered, 0, len(agentNames)+len(skillNames))
	for _, s := range specs() {
		r, err := renderTemplate(fsys, s, vals)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// specs lists the nine templates in render order.
func specs() []tmplSpec {
	var out []tmplSpec
	for _, n := range agentNames {
		out = append(out, tmplSpec{src: agentTemplates + "/" + n + ".md", target: agentsDir + n + ".md",
			generator: genAgents, block: "agent-" + n, name: n, keys: agentFrontKeys})
	}
	for _, n := range skillNames {
		out = append(out, tmplSpec{src: skillTemplates + "/" + n + "/" + skillFile, target: skillsDir + n + "/" + skillFile,
			generator: genSkills, block: "skill-" + n, name: n, keys: skillFrontKeys})
	}
	return out
}

// closedSet refuses an embedded tree that holds anything beyond the four
// agent files and the five skill directories, each with only SKILL.md.
func closedSet(fsys fs.FS) error {
	var agents []string
	for _, n := range agentNames {
		agents = append(agents, n+".md")
	}
	if err := sameEntries(fsys, agentTemplates, agents); err != nil {
		return err
	}
	if err := sameEntries(fsys, skillTemplates, skillNames); err != nil {
		return err
	}
	for _, n := range skillNames {
		if err := sameEntries(fsys, skillTemplates+"/"+n, []string{skillFile}); err != nil {
			return err
		}
	}
	return nil
}

// sameEntries refuses a directory whose entry names differ from want.
func sameEntries(fsys fs.FS, dir string, want []string) error {
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, err, "generate: compiled template directory %s", dir)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	w := append([]string(nil), want...)
	sort.Strings(w)
	if strings.Join(got, ",") != strings.Join(w, ",") {
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled templates in %s are [%s], want [%s]",
			dir, strings.Join(got, ", "), strings.Join(w, ", "))
	}
	return nil
}

// renderTemplate renders one template: its checked frontmatter, a blank
// line, then the filled body inside one managed block.
func renderTemplate(fsys fs.FS, s tmplSpec, vals map[string]string) (Rendered, error) {
	data, err := fs.ReadFile(fsys, s.src)
	if err != nil {
		return Rendered{}, cascade.Wrapf(cascade.KindIntegrity, err, "generate: read compiled template %s", s.src)
	}
	front, body, err := splitFrontmatter(s.src, data)
	if err != nil {
		return Rendered{}, err
	}
	if err := checkFront(s, front); err != nil {
		return Rendered{}, err
	}
	filled, err := fillBody(s.src, body, vals)
	if err != nil {
		return Rendered{}, err
	}
	if err := refuseMarkerBody(FormMarkdown, s.block, filled); err != nil {
		return Rendered{}, err
	}
	content := append(append(front, '\n'), WrapBlock(FormMarkdown, s.block, filled)...)
	return Rendered{Path: s.target, GeneratorID: s.generator, Content: content}, nil
}

// splitFrontmatter splits a template into its "---" frontmatter (both
// fence lines included) and the body after the blank line that follows.
func splitFrontmatter(src string, data []byte) (front, body []byte, err error) {
	const fence = "---\n"
	end := bytes.Index(data, []byte("\n"+fence))
	if !bytes.HasPrefix(data, []byte(fence)) || end < 0 {
		return nil, nil, cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s has no frontmatter", src)
	}
	split := end + 1 + len(fence)
	front = append([]byte(nil), data[:split]...)
	body = bytes.TrimLeft(data[split:], "\n")
	if len(body) == 0 {
		return nil, nil, cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s has no body", src)
	}
	return front, body, nil
}

// checkFront refuses frontmatter whose top-level keys are not exactly the
// kind's keys, whose name is not the template's, or that holds a template
// value.
func checkFront(s tmplSpec, front []byte) error {
	lines := strings.Split(strings.TrimSuffix(string(front), "\n"), "\n")
	var keys []string
	name := ""
	for _, l := range lines[1 : len(lines)-1] {
		if m := frontKey.FindStringSubmatch(l); m != nil {
			keys = append(keys, m[1])
			if m[1] == "name" {
				name = strings.TrimSpace(strings.TrimPrefix(l, "name:"))
			}
		}
	}
	switch {
	case strings.Contains(string(front), "{{"):
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s puts a value in its frontmatter", s.src)
	case strings.Join(keys, ",") != strings.Join(s.keys, ","):
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s frontmatter keys [%s], want [%s]",
			s.src, strings.Join(keys, ", "), strings.Join(s.keys, ", "))
	case name != s.name:
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s is named %q, want %q", s.src, name, s.name)
	}
	return nil
}
