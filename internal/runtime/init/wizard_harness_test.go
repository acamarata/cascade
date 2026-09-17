package init

// Purpose: the test fixture every wizard test shares (P1-E16-W4-S35-T6):
//   a Deps whose collaborators RECORD what the wizard asked them to do,
//   so an assertion can be about the real calls rather than about the
//   wizard's own printed output.
// Constraints: these recorders are confined to the test lane and stand in
//   for collaborators cmd/cascade wires to real implementations. They
//   perform no step's work themselves — a recorder that "installed" a
//   daemon would make every assertion below meaningless.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// recorder captures every collaborator call one run made.
type recorder struct {
	detected     []HarnessState
	detectErr    error
	wired        []string
	wireErr      error
	entries      []CatalogEntry
	providers    []string
	installed    bool
	installErr   error
	enrolled     bool
	enrollErr    error
	fingerprint  string
	doctorRan    bool
	doctorErr    error
	subArgs      [][]string
	subErr       error
	probeErr     error
	probedCreate []bool
	secretErr    error
	env          map[string]string
	current      CurrentState
}

func (r *recorder) Detect(context.Context) ([]HarnessState, error) { return r.detected, r.detectErr }

func (r *recorder) Wire(_ context.Context, kind, _ string) error {
	if r.wireErr != nil {
		return r.wireErr
	}
	r.wired = append(r.wired, kind)
	return nil
}

func (r *recorder) Entries() []CatalogEntry { return r.entries }

func (r *recorder) Install(context.Context) error {
	if r.installErr != nil {
		return r.installErr
	}
	r.installed = true
	return nil
}

func (r *recorder) Enroll(context.Context) (string, error) {
	if r.enrollErr != nil {
		return "", r.enrollErr
	}
	r.enrolled = true
	return r.fingerprint, nil
}

func (r *recorder) FirstRun(context.Context) (string, error) {
	if r.doctorErr != nil {
		return "", r.doctorErr
	}
	r.doctorRan = true
	return "   every first-run check passed", nil
}

func (r *recorder) Run(_ context.Context, args ...string) error {
	if r.subErr != nil {
		return r.subErr
	}
	r.subArgs = append(r.subArgs, args)
	if len(args) >= 3 && args[0] == "provider" && args[1] == "add" {
		r.providers = append(r.providers, args[2])
	}
	return nil
}

func (r *recorder) Probe(_ context.Context, _ string, mayCreate bool) error {
	r.probedCreate = append(r.probedCreate, mayCreate)
	return r.probeErr
}

func (r *recorder) Check(field, _ string) error {
	if r.secretErr == nil {
		return nil
	}
	return cascade.Wrapf(cascade.KindInvalidInput, r.secretErr, "cascade init: %s", field)
}

// scriptedPrompter answers from a queue, so a test can drive the
// interactive path without a terminal. An empty queue is an error, not a
// default: a test that ran out of answers asked a question it did not
// expect, and silently defaulting would hide that.
type scriptedPrompter struct {
	confirms []bool
	choices  []string
	lines    []string
	asked    []string
}

var _ Prompter = (*scriptedPrompter)(nil)

func (p *scriptedPrompter) Confirm(question string, _ bool) (bool, error) {
	p.asked = append(p.asked, question)
	if len(p.confirms) == 0 {
		return false, errors.New("unscripted Confirm: " + question)
	}
	next := p.confirms[0]
	p.confirms = p.confirms[1:]
	return next, nil
}

func (p *scriptedPrompter) Choose(question string, _ []string, _ string) (string, error) {
	p.asked = append(p.asked, question)
	if len(p.choices) == 0 {
		return "", errors.New("unscripted Choose: " + question)
	}
	next := p.choices[0]
	p.choices = p.choices[1:]
	return next, nil
}

func (p *scriptedPrompter) Line(question, _ string) (string, error) {
	p.asked = append(p.asked, question)
	if len(p.lines) == 0 {
		return "", errors.New("unscripted Line: " + question)
	}
	next := p.lines[0]
	p.lines = p.lines[1:]
	return next, nil
}

// fixture builds a fully wired wizard over a temp home, with one harness
// detected and one absent, and returns everything a test asserts on.
func fixture(t *testing.T, opts Options, prompt Prompter) (*Wizard, *recorder, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	rec := &recorder{
		detected: []HarnessState{
			{Kind: "claude", Detected: true, InstallPath: "/h/.claude"},
			{Kind: "codex", Detected: false, InstallPath: "/h/.codex"},
			{Kind: "opencode", Detected: true, InstallPath: "/h/.config/opencode"},
		},
		entries:     []CatalogEntry{{Name: "claude", Description: "the first harness", DefaultOn: true}},
		fingerprint: "SHA256:deadbeef",
		env:         map[string]string{},
	}
	out := &bytes.Buffer{}
	if prompt == nil {
		prompt = DefaultPrompter{}
	}
	deps := Deps{
		Home: home, Cwd: t.TempDir(), GOOS: "darwin", Out: out, Prompt: prompt,
		// Under home/data, exactly where the composition root points it
		// — not home/cascade.db, which is the path init used to advertise
		// and nothing ever opened (R-14.279).
		LocalDBPath: filepath.Join(home, "data", "cascade.db"),
		Detector:    rec, Wirer: rec, Catalog: rec, Service: rec,
		Enroller: rec, Doctor: rec, Sub: rec, Storage: rec, Secrets: rec,
		Getenv: func(key string) string { return rec.env[key] },
	}
	return New(deps, opts), rec, out, home
}
