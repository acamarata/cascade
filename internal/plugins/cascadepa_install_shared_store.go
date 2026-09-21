package plugins

// Purpose (this file): ONE shared *provider.Store for cascade.db, used by
//   every P1-E24-W5-S50-T4 composition-root adapter that needs a
//   provider.Store against the daemon's cascade.db (installerAdapter,
//   dbEventBus) -- plus InstallHostDeps/SetInstallHostDeps, the seam the
//   daemon's REAL composition root (cmd/cascade/daemon_unix_cascadepa_
//   install.go) uses to hand this package its own already-opened
//   store/db/approval-queue/registry.
//
// REWORK (round-3, T0 decision D1, confirming review round 2 Q3/FLAG 2):
//   round-1 and round-2 both shipped this file with a FALLBACK: when
//   SetInstallHostDeps was never called, open() opened its OWN independent
//   sqlitestore.Driver against the daemon's cascade.db path, and
//   cascadepa_install_confirm.go's approvalConfirmGate built its OWN
//   private ApprovalQueue (Registry/Grants/Store) the same way. The
//   confirming review proved both fallbacks real but WRONG: a private
//   ApprovalQueue is unreachable from `cascade approval grant/deny` (a
//   different queue instance the daemon's real RPC surface never sees),
//   and a private cascade.db handle is a store no other part of the
//   running daemon can observe either -- neither is a stub, but neither is
//   reachable by a human, which is the actual defect. T0's D1 ruling: host
//   deps are the ONLY source of Store/Queue/Registry. When the daemon's
//   composition root has not called SetInstallHostDeps, open() REFUSES
//   with KindUnavailable naming the missing wiring -- it never opens a
//   second, private cascade.db. (The mirror fix to
//   cascadepa_install_confirm.go's init() is in that file.)
//
// Inputs: none at construction; the host-deps lookup is deferred to first
//   use (sync.Once), so importing this package never touches the
//   environment.
// Outputs: one provider.Store, shared by every caller of open() -- always
//   the daemon's own injected InstallHostDeps.Store, never one this file
//   opens itself.
// Constraints: production never calls Close on the shared store -- the
//   daemon process owns that store's lifetime exclusively (Close is a
//   permanent no-op; see its own doc comment).
// SPORT: internal/plugins:cascadepa-install-wiring (CHANGED) -- FIX P1-E24-W5-S50-T4 (D1, round 3).

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// InstallHostDeps are the daemon's own already-opened cascade.db store and
// the SAME policy.ApprovalQueue / policy.CapabilityRegistry the approval.*
// RPC handlers use (policy.RPCDeps / policy.MethodHandlers, built by
// cmd/cascade/daemon_unix_policy.go's wirePolicy) -- injected once by
// cmd/cascade/daemon_unix_cascadepa_install.go so this package's adapters
// share the daemon's single cascade.db connection and approval queue
// instead of each building an unreachable private one.
type InstallHostDeps struct {
	// Store is the daemon's own provider.Store, opened once at
	// cmd/cascade/daemon_unix_store.go's openRuntimeStore.
	Store provider.Store
	// Queue is the ONE approval queue the daemon's approval.* RPC surface
	// reads and decides against.
	Queue policy.ApprovalQueue
	// Registry is the SAME capability registry Queue was built over --
	// required so this package can register its own "cascade-pa.install"
	// capability into the identical registry instance Queue's admission
	// check (StoreApprovals.admissible) consults, rather than a second,
	// disconnected registry the queue would never see.
	Registry policy.CapabilityRegistry
}

// installHostDepsState guards the package-level InstallHostDeps seam.
var installHostDepsState struct {
	mu   sync.RWMutex
	deps *InstallHostDeps
}

// SetInstallHostDeps injects deps. Called once by the daemon's real
// composition root (cmd/cascade/daemon_unix_cascadepa_install.go) before
// any install flow can run. Pass nil to reset to the unconfigured
// default -- every caller of open()/init() then refuses with
// KindUnavailable rather than falling back to a private, unreachable
// store or queue; tests use this to avoid leaking state between runs.
func SetInstallHostDeps(deps *InstallHostDeps) {
	installHostDepsState.mu.Lock()
	installHostDepsState.deps = deps
	installHostDepsState.mu.Unlock()
}

// activeInstallHostDeps returns the injected InstallHostDeps, or nil if
// SetInstallHostDeps has never been called (or was last called with nil).
func activeInstallHostDeps() *InstallHostDeps {
	installHostDepsState.mu.RLock()
	defer installHostDepsState.mu.RUnlock()
	return installHostDepsState.deps
}

// errNoInstallHostStore is returned by sharedCascadeStore.open when the
// daemon composition root has not injected a store. Named so every caller
// (and every test asserting on it) reads the identical refusal text.
const errNoInstallHostStoreMsg = "cascade-pa install: no cascade.db store available -- " +
	"the daemon composition root (cmd/cascade/daemon_unix_cascadepa_install.go) has not " +
	"called SetInstallHostDeps; refusing rather than opening a second, private cascade.db"

// sharedCascadeStore lazily resolves the ONE provider.Store every
// collaborator that needs one is handed: the daemon's own injected store,
// once SetInstallHostDeps has been called. There is no other source (D1).
type sharedCascadeStore struct {
	once    sync.Once
	onceErr error
	store   provider.Store
}

// newSharedCascadeStore builds a sharedCascadeStore.
func newSharedCascadeStore() *sharedCascadeStore {
	return &sharedCascadeStore{}
}

// open returns the shared provider.Store: the daemon's own injected store,
// once SetInstallHostDeps has been called with a non-nil Store. With no
// host deps injected, open refuses with KindUnavailable -- it never opens
// a private cascade.db (D1; see this file's header for the finding this
// closes).
func (s *sharedCascadeStore) open(context.Context) (provider.Store, error) {
	s.once.Do(func() {
		hd := activeInstallHostDeps()
		if hd == nil || hd.Store == nil {
			s.onceErr = cascade.New(cascade.KindUnavailable, errNoInstallHostStoreMsg)
			return
		}
		s.store = hd.Store
	})
	return s.store, s.onceErr
}

// Close is a permanent no-op: open() only ever returns the daemon's own
// injected InstallHostDeps.Store (D1) -- the daemon process owns that
// store's lifetime exclusively, and closing it out from under every OTHER
// composition-root adapter that shares it would be a use-after-close
// hazard, not a cleanup. Safe to call on a sharedCascadeStore that never
// opened anything.
func (s *sharedCascadeStore) Close() error {
	return nil
}
