// Purpose: the host shim, the only production caller of an external agent
// driver's Spawn. It enforces driver credential custody in one place.
// Inputs: ShimDeps (the resolver, the credential choices, the profile base,
// the process egress engine, the effect writer, the proxy journal and
// allowlists, the allowed vendor executables) and one SpawnRequest.
// Outputs: a SpawnHandle whose Release undoes everything the spawn made.
// Constraints: order is fixed: lane resolve, envelope, tool policy, minimal
// tree, secret scan, runtime profile, hook pack, driver, executable gate,
// proxy, environments, output filter, effect intent, Spawn, effect confirm.
// A refusal before the intent starts no child and leaves no profile, proxy
// or effect. A driver failure fails the intent. Secrets never reach a log.
// SPORT: internal.providers.agents/ADD (P1-AGT-03, contract:driver-custody-shim).

package agents

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ShimDeps is everything the shim needs. Resolver, Auth, Binaries, Engine,
// Effects, ProxyJournal, Getenv and Clock are required; Exe and ProfileBase
// must be absolute. A harness missing from Postures, Defaults or EgressAllow
// gets the zero posture, provider.NewDriverDefaults and a deny-all allowlist.
type ShimDeps struct {
	Resolver     *Resolver
	Auth         map[HarnessID]DriverAuth
	ProfileBase  string
	Engine       func() (*egress.Engine, error)
	Effects      audit.EffectWriter
	ProxyJournal egressproxy.Journal
	EgressAllow  map[egressproxy.DriverID]egressproxy.DestinationAllowlist
	// Binaries maps a harness to the absolute vendor executable it may run.
	Binaries map[HarnessID]string
	Postures map[HarnessID]provider.CompliancePosture
	Defaults map[HarnessID]provider.DriverDefaults
	// Exe is the cascade binary the hook relay pack calls.
	Exe    string
	Getenv func(string) string
	Clock  runtime.Clock
}

// Shim spawns external agent drivers under custody. Build with NewShim.
type Shim struct {
	d     ShimDeps
	sweep func(ctx context.Context, marker string) error
}

// NewShim validates d and returns the shim.
func NewShim(d ShimDeps) (*Shim, error) {
	switch {
	case d.Resolver == nil || d.Auth == nil || d.Binaries == nil || d.Engine == nil || d.Effects == nil ||
		d.ProxyJournal == nil || d.Getenv == nil || d.Clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "agents: the shim needs a resolver, auth, binaries, engine, effects, journal, getenv and clock")
	case !filepath.IsAbs(d.Exe) || !filepath.IsAbs(d.ProfileBase):
		return nil, cascade.New(cascade.KindInvalidInput, "agents: the shim's relay binary and profile base must be absolute paths")
	}
	for h, bin := range d.Binaries {
		if !filepath.IsAbs(bin) {
			return nil, cascade.Newf(cascade.KindInvalidInput, "agents: the executable for %q must be an absolute path", h)
		}
	}
	return &Shim{d: d, sweep: SweepLease}, nil
}

// SpawnRequest is one spawn. LeaseMarker is minted (NewLeaseMarker) and
// persisted by the caller before Spawn; "" refuses. LeaseID and ChildPrincipal
// are carried unchanged for the capacity binder and never interpreted here.
type SpawnRequest struct {
	Lane           string
	Job            provider.AgentJobSpec
	PrincipalToken string
	LeaseMarker    string
	LeaseID        string
	ChildPrincipal string
}

// SpawnHandle is a running spawn. Release is idempotent and returns the first
// error: Cancel if running, SweepLease, proxy Close, profile removal.
type SpawnHandle struct {
	JobID   provider.AgentJobID
	PGID    int
	Driver  provider.AgentProvider
	Release func(ctx context.Context) error
}

// spawnRun is the state of one Spawn call.
type spawnRun struct {
	s       *Shim
	req     SpawnRequest
	job     provider.AgentJobSpec
	b       Binding
	factory DriverFactory
	auth    DriverAuth
	keyVar  string
	prof    RuntimeProfile
	hook    string
	drv     provider.AgentProvider
	proxy   *egressproxy.Proxy
	built   BuiltEnv
	began   bool
}

// Spawn runs the custody order and starts the driver's child.
func (s *Shim) Spawn(ctx context.Context, req SpawnRequest) (SpawnHandle, error) {
	r := &spawnRun{s: s, req: req, job: req.Job}
	steps := []func(context.Context) error{r.admit, r.checkTree, r.provision, r.buildDriver, r.startProxy, r.buildEnv, r.bindFilter}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			r.rollback()
			return SpawnHandle{}, err
		}
	}
	h, err := r.launch(ctx)
	if err != nil {
		r.rollback()
		return SpawnHandle{}, err
	}
	return h, nil
}

// admit runs every check that needs no filesystem: the request, the lane, the
// credential class, the envelope and the tool policy.
func (r *spawnRun) admit(context.Context) error {
	if !validMarker(r.req.LeaseMarker) {
		return cascade.New(cascade.KindInvalidInput, "agents: the lease marker is empty or not a NewLeaseMarker value")
	}
	if r.req.PrincipalToken == "" {
		return cascade.New(cascade.KindInvalidInput, "agents: the principal token is required")
	}
	var err error
	if r.b, r.factory, err = r.s.d.Resolver.Resolve(r.req.Lane); err != nil {
		return err
	}
	auth, ok := r.s.d.Auth[r.b.Harness]
	if !ok {
		return cascade.Newf(cascade.KindUnavailable, "agents: no [agents.auth.%s] credential choice is configured", r.b.Harness)
	}
	r.auth = auth
	if r.keyVar, err = KeyVarFor(r.b, auth.Class); err != nil {
		return err
	}
	if err = r.job.Envelope.Check(r.job.WorktreeRoot, r.s.d.Clock.Now()); err != nil {
		return err
	}
	if err = checkToolPolicy(r.job.ToolPolicy, r.job.Envelope.Tools); err != nil {
		return err
	}
	if r.job.JobID == "" {
		id, merr := mintJobID()
		if merr != nil {
			return merr
		}
		r.job.JobID = id
	}
	return nil
}

// provision creates the runtime profile and renders the relay hook pack.
func (r *spawnRun) provision(context.Context) error {
	prof, err := ProvisionRuntimeProfile(r.s.d.ProfileBase, r.b, r.auth, string(r.job.JobID))
	if err != nil {
		return err
	}
	r.prof = prof
	r.hook = filepath.Join(filepath.Dir(prof.Home), "hookpack")
	return RenderAgentHookPack(r.hook, r.s.d.Exe)
}

// buildDriver builds the driver for this site and gates its executable.
func (r *spawnRun) buildDriver(context.Context) error {
	h := r.b.Harness
	want, ok := r.s.d.Binaries[h]
	if !ok {
		return cascade.Newf(cascade.KindUnavailable, "agents: no vendor executable is configured for %q", h)
	}
	defaults, ok := r.s.d.Defaults[h]
	if !ok {
		defaults = provider.NewDriverDefaults()
	}
	drv, err := r.factory(SpawnSite{
		KeyVar: r.keyVar, Binary: want, Posture: r.s.d.Postures[h], Defaults: defaults,
		RuntimeHome: r.prof.Home, VendorAuthVar: r.b.ConfigDirVar, CredentialClass: r.auth.Class,
		WorktreeRoot: r.job.WorktreeRoot, HookPackPath: r.hook, Clock: r.s.d.Clock,
	})
	if err != nil {
		return err
	}
	r.drv = drv
	return checkExecutable(drv, want, r.b)
}

// startProxy starts the per-spawn egress proxy for this driver.
func (r *spawnRun) startProxy(ctx context.Context) error {
	id := egressproxy.DriverID(r.b.Harness)
	p, err := StartSpawnProxy(ctx, id, string(r.job.JobID), r.s.d.EgressAllow[id], r.s.d.ProxyJournal)
	if err != nil {
		return err
	}
	r.proxy = p
	return nil
}

// buildEnv builds the exact driver and tool environments.
func (r *spawnRun) buildEnv(context.Context) error {
	built, err := BuildDriverEnv(r.b, r.auth, r.prof, string(r.job.JobID), r.req.PrincipalToken,
		r.req.LeaseMarker, r.proxy.Env(), r.s.d.Getenv)
	if err != nil {
		return err
	}
	r.built = built
	r.job.Env = provider.SpawnEnv{Driver: built.Driver, Tool: built.Tool, Home: r.prof.Home}
	return nil
}

// launch records intent, calls the driver and records the outcome.
func (r *spawnRun) launch(ctx context.Context) (SpawnHandle, error) {
	id := string(r.job.JobID)
	eff, err := r.s.d.Effects.BeginEffect(ctx, audit.EffectRequest{
		Kind: audit.KindExternalEffect, Actor: "agent-driver:" + string(r.b.Harness), Action: "agent.spawn",
		Key: "agent.spawn:" + id, ParamsHash: audit.HashParams([]byte(id)),
	})
	if err != nil {
		return SpawnHandle{}, err
	}
	r.began = true
	res, err := r.drv.Spawn(ctx, r.job)
	if err != nil {
		_, _ = r.s.d.Effects.FailEffect(context.WithoutCancel(ctx), eff, "driver spawn refused")
		return SpawnHandle{}, err
	}
	if _, err = r.s.d.Effects.ConfirmEffect(ctx, eff, "pgid="+strconv.Itoa(res.ProcessGroupID)); err != nil {
		_ = r.drv.Cancel(context.WithoutCancel(ctx), res.JobID)
		return SpawnHandle{}, err
	}
	return SpawnHandle{JobID: res.JobID, PGID: res.ProcessGroupID, Driver: r.drv, Release: r.releaser(res.JobID)}, nil
}

// releaser returns the idempotent Release for a running job.
func (r *spawnRun) releaser(id provider.AgentJobID) func(context.Context) error {
	var once sync.Once
	var first error
	return func(ctx context.Context) error {
		once.Do(func() { first = r.teardown(ctx, id) })
		return first
	}
}

// teardown cancels a live job, sweeps the lease, closes the proxy and removes
// the profile. Every step runs; the first error is returned.
func (r *spawnRun) teardown(ctx context.Context, id provider.AgentJobID) error {
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if st, err := r.drv.Status(ctx, id); err == nil && !st.Terminal() {
		note(r.drv.Cancel(ctx, id))
	}
	note(r.s.sweep(ctx, r.req.LeaseMarker))
	note(r.closeProxy())
	note(r.removeProfile())
	return first
}

func (r *spawnRun) closeProxy() error {
	if r.proxy == nil {
		return nil
	}
	return r.proxy.Close()
}

func (r *spawnRun) removeProfile() error {
	if r.prof.Home == "" {
		return nil
	}
	return os.RemoveAll(filepath.Dir(r.prof.Home))
}

// rollback undoes a refused spawn: no proxy, no profile, no marked process.
func (r *spawnRun) rollback() {
	if r.began {
		_ = r.s.sweep(context.Background(), r.req.LeaseMarker)
	}
	_ = r.closeProxy()
	_ = r.removeProfile()
}
