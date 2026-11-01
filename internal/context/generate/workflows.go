package generate

// Purpose: RenderWorkflows, the harness workflows generator
//   (contract:workflows-probe). Behind a supported probe it renders
//   verify.js, review.js and implement.js from the compiled-in templates
//   plus cascade-workflows.json, the detected commands as data. Every
//   other probe outcome renders nothing and returns a deferral.
// Inputs: a View; a WorkflowsSupportProbe; repo.TemplateFS only.
// Outputs: a WorkflowsLeg: four Rendered files under generator id
//   workflows, each one `//` managed block, or a Deferral.
// Constraints: the three scripts carry no repository value, so they are
//   byte-identical for every repository; a template holding a value
//   placeholder or a host-access token is KindIntegrity. The deferral is
//   data, never ErrHarnessIncompatible. No Fleet, Conductor or driver
//   package reads the output.
// SPORT: context-engine/workflows-generator (ADD, P1-GEN-09).

import (
	"context"
	"encoding/json"
	"io/fs"
	"regexp"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The generator id, target directory and template root of the workflows leg.
const (
	genWorkflows       = "workflows"
	workflowsDir       = ".claude/workflows/"
	workflowsTemplates = "templates/workflows"
	workflowsDataFile  = "cascade-workflows.json"
	workflowsDataBlock = "workflows-data"
)

// The deferral owner and expiry every non-supported outcome records.
const (
	workflowsDeferralOwner  = "DEF-P3-cc-workflows"
	workflowsDeferralExpiry = "2026-12-21"
)

// workflowNames is the closed set of scripts, in render order.
var workflowNames = []string{"verify", "review", "implement"}

// forbiddenScriptToken matches any host-access token a script body may
// not hold: module loading, the file system, subprocesses, the runtime
// global and the Deno global.
var forbiddenScriptToken = regexp.MustCompile(`require|import|\bfs\b|child_process|process\.|Deno\.`)

// scriptPlaceholder matches a template value or a JavaScript template
// substitution, neither of which a script body may hold.
var scriptPlaceholder = regexp.MustCompile(`\{\{|\$\{`)

// WorkflowsLeg is the outcome of RenderWorkflows: the files to write, or
// the deferral to record. Exactly one is set.
type WorkflowsLeg struct {
	Files    []Rendered
	Deferral *Deferral
}

// RenderWorkflows negotiates with p and, only for revision 1, renders the
// three scripts and the data file. Every other outcome returns a leg with
// nil Files and a Deferral. The error is a probe or render failure only.
func RenderWorkflows(ctx context.Context, v View, p WorkflowsSupportProbe) (WorkflowsLeg, error) {
	r, err := Negotiate(ctx, p)
	if err == ErrHarnessIncompatible {
		return WorkflowsLeg{Deferral: &Deferral{Generator: genWorkflows, Result: r.Result, Reason: deferralReason(r),
			Owner: workflowsDeferralOwner, Expiry: workflowsDeferralExpiry}}, nil
	}
	if err != nil {
		return WorkflowsLeg{}, err
	}
	files, err := renderWorkflowFiles(v)
	if err != nil {
		return WorkflowsLeg{}, err
	}
	return WorkflowsLeg{Files: files}, nil
}

// deferralReason states why the leg did not render. It never claims a
// workflow exists.
func deferralReason(r ProbeResult) string {
	switch r.Result {
	case resultUnsupported:
		if r.Binary == "" {
			return "harness CLI not found on PATH; no workflows rendered"
		}
		return "harness CLI " + r.Version + " lists no workflows command; no workflows rendered"
	case resultOutOfRange:
		return "harness CLI " + r.Version + " is older than " + MinCLIVersion + "; no workflows rendered"
	}
	return "harness workflows support could not be determined; no workflows rendered"
}

// renderWorkflowFiles renders the three scripts then the data file.
func renderWorkflowFiles(v View) ([]Rendered, error) {
	fsys := repo.TemplateFS()
	var want []string
	for _, n := range workflowNames {
		want = append(want, n+".js.tmpl")
	}
	if err := sameEntries(fsys, workflowsTemplates, want); err != nil {
		return nil, err
	}
	out := make([]Rendered, 0, len(workflowNames)+1)
	for _, n := range workflowNames {
		r, err := renderScript(fsys, n)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	data, err := renderWorkflowsData(v)
	if err != nil {
		return nil, err
	}
	return append(out, data), nil
}

// renderScript wraps one template, unchanged, in its managed block.
func renderScript(fsys fs.FS, name string) (Rendered, error) {
	src := workflowsTemplates + "/" + name + ".js.tmpl"
	body, err := fs.ReadFile(fsys, src)
	if err != nil {
		return Rendered{}, cascade.Wrapf(cascade.KindIntegrity, err, "generate: read compiled template %s", src)
	}
	if err := checkScriptBody(src, body); err != nil {
		return Rendered{}, err
	}
	id := "workflow-" + name
	if err := refuseMarkerBody(FormSlash, id, body); err != nil {
		return Rendered{}, err
	}
	return Rendered{Path: workflowsDir + name + ".js", GeneratorID: genWorkflows, Content: WrapBlock(FormSlash, id, body)}, nil
}

// checkScriptBody refuses an empty template, one with a value placeholder
// and one with a host-access token.
func checkScriptBody(src string, body []byte) error {
	switch {
	case len(body) == 0:
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s is empty", src)
	case scriptPlaceholder.Match(body):
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s holds a value placeholder", src)
	case forbiddenScriptToken.Match(body):
		return cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s holds a host-access token", src)
	}
	return nil
}

// workflowsData is the data file's JSON shape: the detected commands.
type workflowsData struct {
	Version int                  `json:"version"`
	Scopes  []workflowsDataScope `json:"scopes"`
}

// workflowsDataScope is one scope's detected commands.
type workflowsDataScope struct {
	Rel       string              `json:"rel"`
	Languages []workflowsDataLang `json:"languages"`
}

// workflowsDataLang is one detected language and its commands.
type workflowsDataLang struct {
	Language string `json:"language"`
	Build    string `json:"build"`
	Test     string `json:"test"`
	Lint     string `json:"lint"`
}

// renderWorkflowsData renders the detected commands of every scope as
// JSON inside one managed block. JSON escapes every control character,
// so no value can add a line.
func renderWorkflowsData(v View) (Rendered, error) {
	d := workflowsData{Version: 1, Scopes: []workflowsDataScope{}}
	for _, s := range v.Scopes {
		ds := workflowsDataScope{Rel: s.Rel, Languages: []workflowsDataLang{}}
		for _, l := range s.Languages {
			ds.Languages = append(ds.Languages, workflowsDataLang(l))
		}
		d.Scopes = append(d.Scopes, ds)
	}
	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return Rendered{}, cascade.Wrap(cascade.KindInternal, err, "generate: encode workflows data")
	}
	body = append(body, '\n')
	if err := refuseMarkerBody(FormSlash, workflowsDataBlock, body); err != nil {
		return Rendered{}, err
	}
	return Rendered{Path: workflowsDir + workflowsDataFile, GeneratorID: genWorkflows,
		Content: WrapBlock(FormSlash, workflowsDataBlock, body)}, nil
}
