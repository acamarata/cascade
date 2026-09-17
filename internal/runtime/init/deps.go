package init

// Purpose: the wizard's SEAMS (P1-E16-W4-S35-T6) — every collaborator a
//   step drives, declared as an interface so cmd/cascade can hand the
//   wizard the real provider intake, harness detector, adapters, service
//   installer and doctor.
// Constraints: these are seams, not stand-ins. Production wires every one
//   of them to the shipped implementation; this package contains no
//   fallback that does the step's work itself (Art.1). A nil collaborator
//   is a refusal before anything is written, not a silently skipped step.
// SPORT: internal/runtime/init seams (ADD) — P1-E16-W4-S35-T6.

import (
	"context"
	"io"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
)

// Prompter asks the operator a question. The wizard never reads a
// terminal directly: --yes and --check are whole Prompter
// implementations, which is why neither needs a flag check sprinkled
// through the steps.
type Prompter interface {
	// Confirm asks a yes/no question, returning def when the mode
	// answers for the operator.
	Confirm(question string, def bool) (bool, error)
	// Choose asks for one of options, returning def the same way.
	Choose(question string, options []string, def string) (string, error)
	// Line asks for free text, returning def the same way.
	Line(question, def string) (string, error)
}

// HarnessState is one harness's detection result, reduced to what the
// wizard needs. Transcribed from internal/context's own HarnessState so
// this package can be driven without importing the context engine into
// every test.
type HarnessState struct {
	Kind        string
	Detected    bool
	InstallPath string
}

// Detector reports which harnesses are installed. The wizard CONSUMES
// this; it never re-implements path probing, and there is no fourth
// harness kind anywhere in this package.
type Detector interface {
	Detect(ctx context.Context) ([]HarnessState, error)
}

// HarnessWirer performs one harness's real installation: instruction
// files, hook pack, MCP entry. cmd/cascade wires it to the S-34/S-35.T1
// adapters.
type HarnessWirer interface {
	Wire(ctx context.Context, kind, cwd string) error
}

// CatalogEntry is one row of step 4's checklist.
type CatalogEntry struct {
	// Name is the plugin's own name, as the registry reports it.
	Name string
	// Description is the one-line summary shown beside the checkbox.
	Description string
	// DefaultOn pre-checks the row.
	DefaultOn bool
}

// Catalog lists the plugins this build can actually install.
//
// The catalog is the LIVE registry, never a list written beside the
// wizard: a checklist offering a plugin that does not exist would install
// nothing and report success, which is the Article-1 failure this whole
// surface is most exposed to.
type Catalog interface {
	Entries() []CatalogEntry
}

// ServiceInstaller installs the daemon as a platform service.
type ServiceInstaller interface {
	Install(ctx context.Context) error
}

// HelperEnroller enrolls the elevation helper's public key and returns
// the fingerprint it printed, which step 9 surfaces.
type HelperEnroller interface {
	Enroll(ctx context.Context) (fingerprint string, err error)
}

// DoctorRunner runs the first-run health check and returns its rendered
// summary.
type DoctorRunner interface {
	FirstRun(ctx context.Context) (summary string, err error)
}

// Subprocess runs one cascade subcommand as a real child process.
//
// Two steps hand off this way, for the same reason. The worker profile
// runs `cascade node enroll`, which owns the controller-endpoint prompt.
// The provider step runs `cascade provider add`, which owns the
// credential read, the OAuth browser flow, the shape probe and the live
// micro-verify. Calling into either as a library would mean rebuilding
// its flag surface here, and the two spellings would then drift — which
// is the defect this wizard is most exposed to, since it is by
// construction a second front door onto half the CLI.
//
// It also keeps a property the journal depends on: this package never
// holds a credential at any point, because the process that reads one is
// never this one.
type Subprocess interface {
	Run(ctx context.Context, args ...string) error
}

// StorageProbe reports whether the storage location is usable, which step
// 1's preflight runs before anything else.
//
// mayCreate is not a convenience: a --check run must write NOTHING, and
// creating the cascade home to prove it could be created is a write. With
// mayCreate false the probe answers the same question against the nearest
// directory that already exists, which is the honest reading of "could
// this be created" without creating it.
type StorageProbe interface {
	Probe(ctx context.Context, path string, mayCreate bool) error
}

// Deps are the wizard's injected collaborators. Every one is the REAL
// implementation in production; none is optional-and-silently-skipped —
// a nil collaborator its step needs is a refusal, because a wizard that
// quietly omitted a step would report a machine set up that is not.
type Deps struct {
	// Home is the cascade home the journal and config live under.
	Home string
	// LocalDBPath is where THIS INSTALLATION's sqlite database lives.
	//
	// Injected rather than derived from Home, because the wizard must not
	// be the second place that decides the layout. It used to compute
	// Home/cascade.db while the daemon, the embedded verbs and the doctor
	// checks all open DataDir()/cascade.db — so `init` reported a path
	// nothing opened, and its storage probe created an empty database
	// there (R-14.279). The composition root passes the same value it
	// hands every other subsystem; a blank one is a refusal, not a guess.
	LocalDBPath string
	// Cwd is the project directory harness instructions are written for.
	Cwd string
	// GOOS is the platform whose branch to take. Injected rather than
	// read from runtime.GOOS so every branch is testable on any host.
	GOOS string
	// Out receives the wizard's rendering.
	Out io.Writer
	// Prompt answers the wizard's questions.
	Prompt Prompter

	Detector Detector
	Wirer    HarnessWirer
	Catalog  Catalog
	Service  ServiceInstaller
	Enroller HelperEnroller
	Doctor   DoctorRunner
	Sub      Subprocess
	Storage  StorageProbe

	// Secrets refuses a literal secret typed into a prompt.
	Secrets LiteralSecretGuard
	// Current reads what is already configured, for a reconverge. Nil is
	// legitimate for a fresh run and is a refusal for --reconverge.
	Current CurrentState
	// Getenv reads the environment a setup file's key_env entries point
	// at. Injected so a test never depends on the developer's own
	// environment, and so the ONE place that reads a credential is
	// named rather than reached for inline (Art.7.1).
	Getenv initconfig.EnvFunc
}

// LiteralSecretGuard refuses a value that looks like a secret typed in
// directly rather than referenced from the vault.
type LiteralSecretGuard interface {
	// Check returns a refusal when value carries a literal secret.
	Check(field, value string) error
}
