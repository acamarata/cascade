package context

// Purpose: harness DETECTION (P1-E16-W4-S35-T3) — which of the three
//   supported harnesses are installed on this machine, where, and whether
//   the instruction files cascade generates for them are in sync.
// Inputs: the running GOOS, an environment accessor, and a path probe;
//   all three injected so a unit test never touches the real filesystem
//   (Art.7.1).
// Outputs: one HarnessState per supported harness, always all three, in a
//   fixed order.
// Constraints: detection is READ-ONLY and daemonless-capable — it opens
//   no database, dials nothing, and writes nothing. Windows is tier-2:
//   Detect returns a structured refusal there rather than panicking or
//   silently reporting an empty fleet (Art.5, matching plugins/claude's
//   hostPathsFor gate, which refuses at the same boundary for the same
//   reason).
// SPORT: internal/context harness detection (ADD) — P1-E16-W4-S35-T3.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HarnessKind is one of the three supported harnesses.
//
// The names are the short, product-neutral generator ids sync.go's
// DriftResult already uses. That is not a style choice: this repository
// is public and carries no downstream product names, and a second,
// product-named vocabulary here would have to be mapped back to those ids
// on every drift lookup anyway.
type HarnessKind string

// The three supported harnesses. There is no fourth: an owner ruling
// fixed this set, and harnessWriters() in harness_gen.go is the only
// place a fourth generator would be added.
const (
	HarnessClaude   HarnessKind = "claude"
	HarnessCodex    HarnessKind = "codex"
	HarnessOpenCode HarnessKind = "opencode"
)

// SupportedHarnesses returns the three kinds in their fixed order, which
// is the order Detect reports and the CLI prints.
func SupportedHarnesses() []HarnessKind {
	return []HarnessKind{HarnessClaude, HarnessCodex, HarnessOpenCode}
}

// HarnessState is one harness's detection result.
//
// Detected and Drift are deliberately separate, and so is DriftReason.
// "Not installed", "installed and in sync", "installed and drifted" and
// "installed, drifted, and here is why" are four different answers, and
// an operator reading a list needs to tell them apart without knowing
// which combination of booleans encodes which.
type HarnessState struct {
	// Kind names the harness.
	Kind HarnessKind `json:"kind"`
	// Detected reports whether this harness's config root exists.
	Detected bool `json:"detected"`
	// InstallPath is the config root that was probed. It is reported
	// whether or not the probe found it, because "we looked here" is the
	// first thing anyone debugging a false negative needs.
	InstallPath string `json:"install_path"`
	// InstructionPath is the instruction file cascade generates for this
	// harness in the current working directory, or empty when the
	// harness is not detected.
	InstructionPath string `json:"instruction_path,omitempty"`
	// Drift reports whether that file's content, or its absence, differs
	// from what a fresh generation would produce.
	Drift bool `json:"drift"`
	// DriftReason names why, in the words sync.go's DriftResult uses.
	DriftReason string `json:"drift_reason,omitempty"`
	// Version is the harness version its own state file records, when
	// this build can read that file. Empty means either the harness
	// records no version or its config is in a format this parser does
	// not read — both are stated as absence rather than guessed.
	Version string `json:"version,omitempty"`
	// CascadeRegistered reports whether cascade appears as an MCP server
	// in the harness's own configuration. False on a harness whose
	// config could not be read, which is why it sits beside Version:
	// the two are filled from the same read and are absent together.
	CascadeRegistered bool `json:"cascade_registered"`
}

// EnvFunc reads one environment variable.
type EnvFunc func(key string) string

// PathProbe reports whether path exists. It is a func rather than a
// filesystem interface because existence is the only question the
// detection HALF asks; reading a config is the separate, explicit
// FileReader seam below, so a detector cannot read a file by accident.
type PathProbe func(path string) bool

// FileReader reads one file. A nil reader means the detector does
// existence only and leaves every state's Version and CascadeRegistered
// unset — which is what those fields mean when nothing read them.
type FileReader func(path string) ([]byte, error)

// HarnessDetector reports the state of every supported harness.
type HarnessDetector interface {
	Detect(ctx context.Context) ([]HarnessState, error)
}

// ErrHarnessDetectionUnsupported is the tier-2 refusal Detect answers with
// on a platform whose harness paths this build does not resolve.
//
// KindUnsupported, and a refusal rather than an empty list: reporting "no
// harnesses installed" on a machine nobody looked at is the shape of a
// false negative an operator would act on.
var ErrHarnessDetectionUnsupported = cascade.New(cascade.KindUnsupported,
	"context: harness detection is not available on this platform (tier-2)")

// PathDetector is the production HarnessDetector.
type PathDetector struct {
	goos  string
	env   EnvFunc
	probe PathProbe
	read  FileReader
}

var _ HarnessDetector = (*PathDetector)(nil)

// NewPathDetector builds a detector for goos over env and probe. It
// reports existence only; use WithFileReader to also read each detected
// harness's own config.
func NewPathDetector(goos string, env EnvFunc, probe PathProbe) *PathDetector {
	return &PathDetector{goos: goos, env: env, probe: probe}
}

// WithFileReader attaches the reader Detect uses to fill Version and
// CascadeRegistered, and returns d.
//
// Separate from the constructor because reading is optional and reading
// somebody's configuration is a bigger act than checking whether a
// directory exists. A caller that wants only "what is installed" passes
// no reader and this detector opens nothing.
func (d *PathDetector) WithFileReader(read FileReader) *PathDetector {
	d.read = read
	return d
}

// Detect reports every supported harness, installed or not.
//
// It always returns all three, in SupportedHarnesses order. A list that
// omitted the absent ones would make "codex is not installed" and "this
// build forgot about codex" indistinguishable in exactly the surface
// built to tell an operator what is set up.
func (d *PathDetector) Detect(ctx context.Context) ([]HarnessState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.env == nil || d.probe == nil {
		return nil, cascade.New(cascade.KindInternal,
			"context: harness detector was built without an environment or a path probe")
	}
	if d.goos == "windows" {
		return nil, ErrHarnessDetectionUnsupported
	}
	out := make([]HarnessState, 0, len(SupportedHarnesses()))
	for _, kind := range SupportedHarnesses() {
		root, err := d.configRoot(kind)
		if err != nil {
			return nil, err
		}
		state := HarnessState{Kind: kind, InstallPath: root, Detected: d.probe(root)}
		if state.Detected {
			d.readConfigInto(&state, root)
		}
		out = append(out, state)
	}
	return out, nil
}

// readConfigInto fills state's Version and CascadeRegistered from the
// harness's own config file, best-effort.
//
// Every failure leaves the fields unset rather than failing detection.
// "This harness is installed" is the answer that matters, and losing it
// because a config file was unreadable, absent, or in a format this
// parser does not handle would trade the useful answer for none at all.
// The harnesses whose config this build cannot read (one keeps TOML)
// carry no config file name in the table and are skipped here.
func (d *PathDetector) readConfigInto(state *HarnessState, root string) {
	if d.read == nil {
		return
	}
	path := d.configFilePath(state.Kind, root)
	if path == "" {
		return
	}
	raw, err := d.read(path)
	if err != nil {
		return
	}
	cfg, err := ParseHarnessConfig(raw)
	if err != nil {
		return
	}
	state.Version = cfg.Version
	state.CascadeRegistered = cfg.CascadeRegistered
}
