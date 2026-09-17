// Purpose: `cascade doctor`'s composition root - the ONE place a
//
//	subsystem's doctor.Check is mounted, plus the small adapters that
//	satisfy each check's injected interface from what the CLI can
//	actually open. Split out of doctor.go so the command file stays
//	command wiring and this file stays mounting, and so the mounting
//	point the internal/build mount gate reads is a file of its own.
//
// Inputs: doctorDeps, and a context so a mount that loads config or opens
//
//	a store does it under the run's own cancellation.
// Outputs: a fresh *doctor.CheckRegistry per invocation (Register panics
//
//	on a duplicate name, so one shared registry across two command
//	executions in a test binary would panic the second time).
//
// Constraints: a check is mounted only against a REAL data source. A
//
//	mount needing a hand-written stand-in is left out and recorded in
//	internal/build's DoctorMountExemptions instead (Art.1).
//
// SPORT: DOCTOR_SECRETS_REGISTRATION: CHANGE (cmd/cascade doctor mounts).

package main

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/backup/targets"
	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// productionCheckRegistry is the composition root for doctor's checks,
// with no init() side effects.
//
// A check whose data source cannot be opened is not silently dropped: the
// mount returns the error, and the run refuses. A doctor report missing
// the checks an operator asked for looks identical to one whose subject
// is healthy, which is the failure this whole surface exists to prevent.
func productionCheckRegistry(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*doctor.CheckRegistry, error) {
	reg := doctor.NewCheckRegistry()
	reg.Register(doctor.NewDoctorSelfCheck())
	// retrieval_index: the on-disk retrieval index exists unconditionally,
	// so there is no unimplemented interface between this check and
	// something real to probe.
	reg.Register(lifecycle.NewDoctorCheck(buildRecallIndexManager(paths, clock)))
	// retrieval_fusion_default: *runtime.Config resolves the effective
	// retrieval.fusion.enabled, and fusionConfigProvider is the adapter
	// over it. A config that will not load yields a nil provider, which
	// the check itself reports as an error rather than as a fusion
	// verdict it could not read.
	reg.Register(doctor.NewRetrievalFusionGateCheck(fusionProviderFor(ctx, paths)))
	// provider_health (P1-E10-W3-S21-T2, R-14.35): providerHealthSourceFor
	// opens the S-20.T2 registry + S-20.T3 health.Manager the same way
	// `cascade provider list/test/health` do (provider_health_cmd.go);
	// the adapter itself never evicts or deletes a provider record.
	reg.Register(doctor.NewProviderHealthCheck(providerHealthSourceFor(paths)))
	// nodes (P1-E17-W4-S36-T2): reads the same file-backed device record
	// store `node serve` writes heartbeats into, so doctor reports the
	// liveness the serving path actually recorded. nodesRecordStoreFor
	// returns nil when the data directory cannot be resolved, and the
	// check reports a nil store as StatusError rather than as "no nodes
	// enrolled" - an unreadable subject is never silently OK.
	reg.Register(nodes.NewHealthCheck(nodesRecordStoreFor(paths, clock), clock, 0))
	reg.Register(targets.NewRcloneDoctorCheck(nil)) // backup (P1-E19-W4-S41-T3)
	// harness (P1-E16-W4-S35-T3): which coding harnesses are installed and
	// whether the instruction files cascade generates for them are current.
	// It is the check `doctor --harness` narrows to.
	reg.Register(cascadecontext.NewHarnessCheck(productionHarnessDetector(), productionHarnessDrift()))
	// context-hydration (P1-E16-W4-S34-T4): counts the degraded-hydration
	// events the prompt hook publishes. It is the only place a degraded
	// hydration becomes visible at all -- the hook fails OPEN by design,
	// so from the user's side a degraded hydration and a session with no
	// context worth injecting look identical.
	reg.Register(hydration.NewCheck(hydrationCountFor(paths, clock), clock))
	checks, err := secretsDoctorChecks(ctx, paths, clock)
	if err != nil {
		return nil, err
	}
	for _, check := range checks {
		reg.Register(check)
	}
	return reg, nil
}

// fusionConfigProvider adapts *runtime.Config to doctor's narrow
// FusionEnabledProvider.
type fusionConfigProvider struct {
	enabled bool
}

// RetrievalFusionEnabled reports the effective retrieval.fusion.enabled.
func (p fusionConfigProvider) RetrievalFusionEnabled() bool { return p.enabled }

// fusionProviderFor loads config and adapts it. A load failure returns a
// nil provider ON PURPOSE: doctor's own check reports a nil provider as
// an error, which is the honest answer for "the config that decides this
// could not be read", and inventing a default here would print a fusion
// verdict nothing measured.
func fusionProviderFor(ctx context.Context, paths runtime.PathProvider) doctor.FusionEnabledProvider {
	cfg, err := loadDoctorConfig(ctx, paths)
	if err != nil {
		return nil
	}
	return fusionConfigProvider{enabled: cfg.FusionEnabled}
}

// loadDoctorConfig loads config.toml through the same runtime.Load entry
// point every other command uses.
func loadDoctorConfig(ctx context.Context, paths runtime.PathProvider) (*runtime.Config, error) {
	return runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath()})
}

// newDoctorCustody opens the custody backend the vault checks probe. It
// is a package variable so a test can drive the REAL registry against a
// temp-dir file vault rather than the operator's own keychain;
// production never replaces it.
var newDoctorCustody = func(dir string) (secrets.Custody, error) {
	return secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: dir})
}

// secretsDoctorChecks builds the five vault and detector checks over the
// real custody backend, the real quarantine ledger and the real detector.
func secretsDoctorChecks(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) ([]doctor.Check, error) {
	dir := paths.DataDir()
	if dir == "" {
		return nil, cascade.New(cascade.KindUnavailable,
			"cascade doctor: could not resolve the cascade data directory the vault checks probe")
	}
	custody, err := newDoctorCustody(dir)
	if err != nil {
		return nil, err
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return nil, err
	}
	quarantine, err := secrets.NewQuarantineStore(filepath.Join(dir, quarantineDirName), clock)
	if err != nil {
		return nil, err
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, err
	}
	return secrets.NewDoctorChecks(secrets.DoctorCheckDeps{
		Broker:     broker,
		Quarantine: quarantine,
		Detector:   detector,
		ConfigKeys: configuredVaultKeys(ctx, paths),
		Now:        clock.Now,
	})
}

// configuredVaultKeys returns the vault reference names the effective
// config's values point at. A config that will not load contributes no
// names: the keys-resolvable check then reports that configuration
// references none, which is true of a config that could not be read at
// all, and the config's own load failure is reported by the surfaces that
// own it rather than invented into a missing-key list here.
func configuredVaultKeys(ctx context.Context, paths runtime.PathProvider) []string {
	cfg, err := loadDoctorConfig(ctx, paths)
	if err != nil {
		return nil
	}
	var b strings.Builder
	for _, entry := range cfg.EffectiveEntries() {
		if text, ok := entry.Value.(string); ok {
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return secrets.VaultRefsIn(b.String())
}

// providerHealthSourceFor returns the production doctor.ProviderHealthSource.
// paths is accepted for signature symmetry with this file's other
// providerFor helpers; the adapter resolves the real environment paths
// via productionProviderDeps(), the same composition root `cascade
// provider add` already uses.
func providerHealthSourceFor(paths runtime.PathProvider) doctor.ProviderHealthSource {
	return providerHealthSourceAdapter{paths: paths}
}

// ListProviderHealth implements doctor.ProviderHealthSource.
func (a providerHealthSourceAdapter) ListProviderHealth(ctx context.Context) ([]doctor.ProviderHealthRow, error) {
	deps := productionProviderDeps()
	if a.paths != nil {
		// providerDepsFor, NOT a field reassignment: NewCustody and Gate
		// capture the PathProvider in closures, so overwriting deps.Paths
		// alone would leave those two still resolving the real home.
		deps = providerDepsFor(a.paths)
	}
	store, err := openProviderStorage(ctx, deps)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	recs, err := store.Registry.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]doctor.ProviderHealthRow, len(recs))
	for i, rec := range recs {
		rows[i] = doctor.ProviderHealthRow{Name: rec.Name, Status: string(rec.HealthStatus)}
	}
	return rows, nil
}

// RecoverProviderHealth implements doctor.ProviderHealthSource.
func (providerHealthSourceAdapter) RecoverProviderHealth(ctx context.Context, name string) (bool, error) {
	store, err := openProviderStorage(ctx, productionProviderDeps())
	if err != nil {
		return false, err
	}
	defer func() { _ = store.Close() }()
	return store.Health.RecoverProbe(ctx, name)
}

// nodesRecordStoreFor opens the file-backed device record store that
// `node serve` writes heartbeats into. It returns nil ON PURPOSE when the
// data directory cannot be resolved: nodes.HealthCheck reports a nil store
// as StatusError, which is the honest answer for "the records that decide
// this could not be located". Inventing an empty store here would print
// "no nodes enrolled" for an installation whose nodes are simply
// unreadable, which is the one wrong answer a fleet check can give.
func nodesRecordStoreFor(paths runtime.PathProvider, clock runtime.Clock) *nodes.RecordStore {
	dataDir := paths.DataDir()
	if dataDir == "" {
		return nil
	}
	return nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), clock)
}
