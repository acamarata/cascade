// Purpose: Driver, its injected seams, New, Executable, Capabilities and
//   Spawn: the ordered pre-spawn guards (platform, entitlement, the
//   per-spawn output filter, the job id, unsupported flag values, tool
//   policy, spawn environment, worktree root, lease, tree, secret scan,
//   hook pack, version probe, argv, init probe and protocol negotiation)
//   before any job child exists.
// Inputs: a Config naming the binary, hook-pack directory and seams; one
//   provider.AgentJobSpec per Spawn, carrying its Env, WorktreeRoot,
//   Filter and optional JobID (R-14.329).
// Outputs: provider.AgentProvider behavior; SpawnResult carries the job id
//   and the child's process group (R-21.177).
// Constraints: the child environ is spec.Env.Driver exactly, never
//   the daemon process environment; the child's cwd is spec.WorktreeRoot, which must equal the
//   lease root; every refusal happens before the job child starts. There is
//   no constructor output filter and no transcript-parser seam.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Profile is the J/S-20 profile-registry projection this driver needs.
type Profile struct {
	// VendorAuthVar must equal VendorConfigDirVar (R-16.37).
	VendorAuthVar string
	// KeyVar is the one key variable beside VendorAuthVar: KeyVarOAuthToken
	// for class oauth_token, KeyVarAPIKey for class api_key (B-1).
	KeyVar string
	// RuntimeHome is the per-profile 0700 runtime home (R-21.151).
	RuntimeHome     string
	Posture         provider.CompliancePosture
	CredentialClass CredentialClass
}

// ProfileResolver resolves the active profile (the J/S-20 registry adapter).
type ProfileResolver interface {
	Resolve(ctx context.Context) (Profile, error)
}

// Lease is the AC/S-59.T2 lease record projection: the worktree root and
// the git directory the lease itself names (never read from the tree).
type Lease struct {
	Root   string
	GitDir string
}

// WorktreeLeaser resolves a scope to its leased worktree.
type WorktreeLeaser interface {
	Lease(ctx context.Context, scope provider.WorktreeScope) (Lease, error)
}

// Config bundles everything New needs. Binary is the vendor CLI path; New
// resolves its symlinks and refuses a relative or missing path.
type Config struct {
	Binary       string
	HookPackPath string
	Profile      ProfileResolver
	Worktree     WorktreeLeaser
	Defaults     provider.DriverDefaults
	// Descriptor is the harness descriptor; the zero value means
	// NewDescriptor().
	Descriptor Descriptor
	// ProbeTimeout bounds each pre-spawn probe; zero means 60s.
	ProbeTimeout time.Duration
	// Clock supplies "now" for the envelope expiry check. New accepts nil (a
	// probe-only driver never spawns); Spawn then refuses before any child.
	Clock Clock
}

// Clock is the injected time source (the runtime.Clock method set).
type Clock interface{ Now() time.Time }

// Driver is the headless one-shot AgentProvider. Construct with New.
type Driver struct {
	cfg      Config
	mu       sync.Mutex
	jobs     map[provider.AgentJobID]*jobRecord
	reserved map[provider.AgentJobID]bool // claimed by a Spawn in progress
	nextID   uint64
	approve  chan provider.ApprovalRequest
}

var (
	_ provider.AgentProvider      = (*Driver)(nil)
	_ provider.ExecutableReporter = (*Driver)(nil)
)

// New validates cfg and returns a ready Driver; a nil seam is an error,
// never a degraded driver.
func New(cfg Config) (*Driver, error) {
	if cfg.Profile == nil || cfg.Worktree == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "claude: driver seams must not be nil")
	}
	exe, err := resolveBinary(cfg.Binary)
	if err != nil {
		return nil, err
	}
	cfg.Binary = exe
	if cfg.Defaults.CancelGrace <= 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "claude: DriverDefaults.CancelGrace must be positive")
	}
	if cfg.Descriptor.MinVersions == nil {
		cfg.Descriptor = NewDescriptor()
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = defaultProbeTimeout
	}
	ch := make(chan provider.ApprovalRequest)
	close(ch) // R-21.171: --permission-prompts none raises no approval.
	return &Driver{cfg: cfg, jobs: make(map[provider.AgentJobID]*jobRecord),
		reserved: make(map[provider.AgentJobID]bool), approve: ch}, nil
}

// resolveBinary returns the absolute, symlink-resolved path of an existing
// regular file; a relative, missing or non-regular path refuses.
func resolveBinary(bin string) (string, error) {
	if bin == "" || !filepath.IsAbs(bin) {
		return "", cascade.New(cascade.KindInvalidInput, "claude: driver binary must be an absolute path")
	}
	exe, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "claude: resolving the driver binary")
	}
	if fi, err := os.Stat(exe); err != nil || !fi.Mode().IsRegular() {
		return "", cascade.Newf(cascade.KindInvalidInput, "claude: driver binary %q is not a regular file", exe)
	}
	return exe, nil
}

// Executable returns the absolute, symlink-resolved vendor CLI path New
// resolved (provider.ExecutableReporter, the host shim's allowlist).
func (d *Driver) Executable() string { return d.cfg.Binary }

// Capabilities returns the profile's declared CompliancePosture.
func (d *Driver) Capabilities(ctx context.Context, _ string) (provider.Capabilities, error) {
	prof, err := d.cfg.Profile.Resolve(ctx)
	if err != nil {
		return provider.Capabilities{}, err
	}
	return provider.Capabilities{CompliancePosture: prof.Posture}, nil
}

// Spawn runs every pre-spawn guard, then starts the job child. The
// returned errors are the exact sentinels where the contract names one
// (ErrPlatform, ErrEntitlement, ErrEgressSubstitutionFailed for a nil
// spec.Filter, ErrPreSpawnSecretFound). A claimed job id is released when
// any later guard refuses.
func (d *Driver) Spawn(ctx context.Context, job provider.AgentJobSpec) (provider.SpawnResult, error) {
	if refused := platformRefusal(); refused != nil {
		return provider.SpawnResult{}, refused
	}
	prof, err := d.cfg.Profile.Resolve(ctx)
	if err != nil {
		return provider.SpawnResult{}, err
	}
	if !prof.Posture.ProgrammaticEntitlement {
		return provider.SpawnResult{}, provider.ErrEntitlement
	}
	if _, err := keyVarFor(prof); err != nil {
		return provider.SpawnResult{}, err
	}
	if job.Filter == nil {
		return provider.SpawnResult{}, provider.ErrEgressSubstitutionFailed
	}
	id, err := d.claimID(job.JobID)
	if err != nil {
		return provider.SpawnResult{}, err
	}
	plan, err := d.prepare(ctx, job, prof, id)
	if err != nil {
		d.unclaim(id)
		return provider.SpawnResult{}, err
	}
	rec, err := d.startChild(plan)
	if err != nil {
		d.unclaim(id)
		return provider.SpawnResult{}, err
	}
	return provider.SpawnResult{JobID: rec.id, ProcessGroupID: rec.pgid}, nil
}

// spawnPlan is everything startChild needs once every guard has passed.
type spawnPlan struct {
	id       provider.AgentJobID
	job      provider.AgentJobSpec
	prof     Profile
	lease    Lease
	env      []string
	argv     []string
	injected []string
	protocol provider.ProtocolVersion
}

// prepare runs the guards that need no job child, in order, and returns the
// plan. No guard is skippable: each returns before the next runs.
func (d *Driver) prepare(ctx context.Context, job provider.AgentJobSpec, prof Profile, id provider.AgentJobID) (*spawnPlan, error) {
	if err := refuseUnsupportedDefaults(d.cfg.Defaults); err != nil {
		return nil, err
	}
	policy, err := provider.ResolveToolPolicy(job.ToolPolicy)
	if err != nil {
		return nil, err
	}
	if err := checkModelEffort(job.Model, job.Effort, d.cfg.Defaults.Effort); err != nil {
		return nil, err
	}
	if err := checkSpawnEnv(job.Env, prof); err != nil {
		return nil, err
	}
	lease, err := d.leaseFor(ctx, job)
	if err != nil {
		return nil, err
	}
	dirs, err := d.checkTree(lease, job)
	if err != nil {
		return nil, err
	}
	injected, err := copyHookPack(d.cfg.HookPackPath, filepath.Join(lease.Root, ".claude"))
	if err != nil {
		return nil, err
	}
	version, err := probeVersion(ctx, d.cfg.Binary, job.Env.Driver, lease.Root, d.cfg.ProbeTimeout)
	if err != nil {
		return nil, err
	}
	in := argvInput{def: d.cfg.Defaults, desc: d.cfg.Descriptor, version: version, policy: policy,
		cred: prof.CredentialClass, name: sessionName(job, id), jobID: id, root: lease.Root, addDirs: dirs,
		home: job.Env.Home, prompt: job.Prompt, model: job.Model, effort: job.Effort}
	argv, err := buildArgv(in)
	if err != nil {
		return nil, err
	}
	peer, err := d.probeInit(ctx, in, job.Env.Driver)
	if err != nil {
		return nil, err
	}
	protoVer, err := d.Negotiate(ctx, peer)
	if err != nil {
		return nil, err
	}
	return &spawnPlan{id: id, job: job, prof: prof, lease: lease, env: job.Env.Driver,
		argv: argv, injected: injected, protocol: protoVer}, nil
}

// leaseFor refuses an empty spec.WorktreeRoot (ErrEntitlement naming the
// field), resolves the lease, refuses a WorktreeRoot that is not the lease
// root (the child's cwd is WorktreeRoot, so the two must agree) and refuses
// an incomplete spec.Envelope (ErrEnvelopeIncomplete), all before any child.
func (d *Driver) leaseFor(ctx context.Context, job provider.AgentJobSpec) (Lease, error) {
	if job.WorktreeRoot == "" {
		return Lease{}, cascade.Wrap(cascade.KindPolicyDenied, provider.ErrEntitlement, "claude: spec.WorktreeRoot is empty")
	}
	lease, err := d.cfg.Worktree.Lease(ctx, job.Scope)
	if err != nil {
		return Lease{}, err
	}
	if job.WorktreeRoot != lease.Root {
		return Lease{}, cascade.Wrapf(cascade.KindConflict, ErrWorktreeRootMismatch,
			"claude: spec.WorktreeRoot %q is not the lease root %q", job.WorktreeRoot, lease.Root)
	}
	if d.cfg.Clock == nil {
		return Lease{}, cascade.New(cascade.KindInvalidInput, "claude: Config.Clock is nil, so the envelope expiry cannot be checked")
	}
	if err := job.Envelope.Check(job.WorktreeRoot, d.cfg.Clock.Now()); err != nil {
		return Lease{}, err
	}
	return lease, leaseWithinRoots(lease.Root, job.Envelope.Roots)
}

// checkTree validates the lease and the tree it names, then runs the real
// provider.PreSpawnScan over the whole tree (the child's cwd is the root,
// so every file in it is exposed, whatever job.Scope declared).
func (d *Driver) checkTree(lease Lease, job provider.AgentJobSpec) ([]string, error) {
	if err := checkLease(lease); err != nil {
		return nil, err
	}
	dirs, err := addDirs(lease.Root, job.Scope.InScopePrefixes)
	if err != nil {
		return nil, err
	}
	if err := guardTree(lease.Root, job.Env.Home); err != nil {
		return nil, err
	}
	if err := refuseForeignSettings(filepath.Join(lease.Root, ".claude"), d.cfg.HookPackPath); err != nil {
		return nil, err
	}
	if err := provider.PreSpawnScan(lease.Root, provider.WorktreeScope{InScopePrefixes: []string{"."}}); err != nil {
		return nil, err
	}
	return dirs, nil
}
