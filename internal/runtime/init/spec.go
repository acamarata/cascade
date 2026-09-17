package init

// Purpose: the non-interactive spec's effect on the wizard
//   (P1-E16-W4-S35-T7) — a Prompter that answers from the spec, and the
//   guard that refuses when nothing can answer.
// Inputs: an *initconfig.Spec resolved from flags, environment and file.
// Outputs: answers at each prompt site, or a refusal naming the site.
// Constraints: the spec reaches the steps as a PROMPTER, the same way
//   --yes and --check do, rather than as a parameter each step checks.
//   Nine steps checking a spec is nine chances to forget one; one
//   Prompter is one place.
// SPORT: internal/runtime/init non-interactive spec (ADD) — P1-E16-W4-S35-T7.

import (
	"strings"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// SpecPrompter answers from a resolved spec, falling back to the default
// when the spec is silent — or refusing, when the environment said no
// prompt may happen.
type SpecPrompter struct {
	spec *initconfig.Spec
}

var _ Prompter = (*SpecPrompter)(nil)

// NewSpecPrompter builds a prompter over spec.
func NewSpecPrompter(spec *initconfig.Spec) *SpecPrompter { return &SpecPrompter{spec: spec} }

// ErrPromptRequired is the CASCADE_NO_INPUT refusal.
//
// A refusal rather than a silent default: the operator asked for a run
// that never blocks, and a run that quietly answered for them would
// configure the machine from values they never supplied. The message
// names both ways out.
var ErrPromptRequired = cascade.New(cascade.KindInvalidInput,
	"cascade init: this step needs an answer and "+initconfig.EnvNoInput+" is set; "+
		"use --yes to accept every default, or --config to supply the values")

// Confirm answers a yes/no question from the spec.
//
// A spec silent about this question falls through to the default —
// unless NO_INPUT is set and --yes is not, in which case accepting a
// default IS answering for the operator, and the guard fires.
func (p *SpecPrompter) Confirm(question string, def bool) (bool, error) {
	if answer, ok := p.confirmFromSpec(question); ok {
		return answer, nil
	}
	if err := p.guard(question); err != nil {
		return false, err
	}
	return def, nil
}

// confirmFromSpec answers the questions the spec has a field for.
//
// Matched on the question's own text. That is fragile if the two are
// written apart, so every branch here is driven through the REAL step in
// spec_test.go: a step whose wording changes without this file changing
// fails those tests.
func (p *SpecPrompter) confirmFromSpec(question string) (bool, bool) {
	switch {
	case strings.HasPrefix(question, "Enable "):
		if !p.spec.PluginsSet {
			return false, false
		}
		return contains(p.spec.Plugins, between(question, "Enable ", "?")), true
	case strings.HasPrefix(question, "Wire "):
		if !p.spec.HarnessesSet {
			return false, false
		}
		return contains(p.spec.Harnesses, between(question, "Wire ", "?")), true
	case strings.HasPrefix(question, "Send anonymous"):
		if !p.spec.TelemetrySet {
			return false, false
		}
		return p.spec.Telemetry, true
	case strings.HasPrefix(question, "Install the cascade daemon"):
		if !p.spec.DaemonSet {
			return false, false
		}
		return p.spec.Daemon, true
	case strings.HasPrefix(question, "Add a provider"):
		// The provider loop is driven from the spec's DIRECTIVES, not
		// from this prompt: answering no ends the interactive loop, and
		// the directives are applied by the step itself.
		return false, true
	default:
		return false, false
	}
}

// Choose answers a selector from the spec.
func (p *SpecPrompter) Choose(question string, options []string, def string) (string, error) {
	if strings.HasPrefix(question, "Which profile") && p.spec.Profile != "" {
		return p.spec.Profile, nil
	}
	if err := p.guard(question); err != nil {
		return "", err
	}
	if def == "" && len(options) > 0 {
		return options[0], nil
	}
	return def, nil
}

// Line answers a free-text question from the spec.
func (p *SpecPrompter) Line(question, def string) (string, error) {
	if answer, ok := p.lineFromSpec(question); ok {
		return answer, nil
	}
	if err := p.guard(question); err != nil {
		return "", err
	}
	return def, nil
}

// lineFromSpec answers the server profile's three storage references.
func (p *SpecPrompter) lineFromSpec(question string) (string, bool) {
	switch {
	case strings.HasPrefix(question, "Postgres connection"):
		return envRef(p.spec.Server.PostgresDSNEnv)
	case strings.HasPrefix(question, "Redis connection"):
		return envRef(p.spec.Server.RedisURLEnv)
	case strings.HasPrefix(question, "S3 endpoint"):
		return envRef(p.spec.Server.S3EnvPrefix)
	default:
		return "", false
	}
}

// envRef renders a variable NAME as the reference form the storage step's
// literal-secret guard accepts.
//
// The setup file gives a variable name and the prompt wants a reference;
// converting here keeps the file's schema simple and the guard's rule
// unchanged, rather than teaching the guard a second accepted shape.
func envRef(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	return "env:" + name, true
}

// guard refuses when nothing can answer and nothing may be assumed.
//
// --yes passes every site by construction: it says every default is
// acceptable. A spec that answered this site passes because it answered.
// Only a site with no answer and no standing permission to assume one
// fires.
func (p *SpecPrompter) guard(question string) error {
	if p.spec.Yes || !p.spec.NoInput {
		return nil
	}
	return cascade.Wrapf(cascade.KindInvalidInput, ErrPromptRequired, "at %q", strings.TrimSuffix(question, "?"))
}

// between extracts the text between prefix and the first suffix after it.
func between(s, prefix, suffix string) string {
	s = strings.TrimPrefix(s, prefix)
	if i := strings.Index(s, suffix); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// contains reports membership.
func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
