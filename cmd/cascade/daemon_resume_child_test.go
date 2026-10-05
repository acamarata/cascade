//go:build !windows && integration

// Purpose: the child half of the child-daemon harness (T0 P1-BF-R120,
//
//	R124). TestDaemonResumeChildProcess runs platformDaemonRun over the
//	parent's throwaway CASCADE_HOME after proving the login keychain is
//	unreachable (H5). When asked, it swaps the "conductor" registration for
//	one built through the production wire path with only a store wrapper
//	(the crash or notify site, H2) and, for the deny test, the policy
//	source (a flag-file policy, H1) injected; authorize, replay and dispatch
//	stay production code.
//
// Constraints: skips unless the parent set CASCADE_TEST_RESUME_CHILD_ROOT;
//
//	runs under its own TMPDIR, so its TestMain gives it its own HOME.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/nodes"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	providertransport "github.com/acamarata/cascade/providers/transport"
)

// TestDaemonResumeChildProcess is the child daemon; a plain test run skips.
func TestDaemonResumeChildProcess(t *testing.T) {
	root := os.Getenv(resumeChildRoot)
	if root == "" {
		t.Skip("child half of the daemon resume tests")
	}
	paths := fakeDaemonPaths{root: root}
	requireKeychainUnreachable(t, os.Getenv("HOME"), paths.DataDir())
	if o := (childOpts{site: os.Getenv(resumeChildSite), notify: os.Getenv(resumeChildNotify), policy: os.Getenv(resumeChildPolicy)}); o != (childOpts{}) {
		swapConductorRegistration(t, o)
	}
	deps := daemonDeps{Paths: paths, Getenv: func(string) string { return "" }, Environ: func() []string { return nil },
		Clock: runtime.SystemClock{}, Executable: os.Executable}
	if err := platformDaemonRun(context.Background(), deps); err != nil {
		t.Fatalf("platformDaemonRun: %v", err)
	}
}

// requireKeychainUnreachable fails the child before its daemon starts
// unless the R105 custody hook is installed, HOME is a throwaway below the
// child's own non-empty TMPDIR (never the passwd home), and the platform
// keychain is proven unreachable from that HOME. Every check fails closed.
func requireKeychainUnreachable(t *testing.T, home, dataDir string) {
	t.Helper()
	if daemonCustodyHook == nil {
		t.Fatal("the R105 custody hook is not installed in the child")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatalf("passwd lookup: %v", err)
	}
	if why := throwawayHomeReason(home, u.HomeDir, os.Getenv("TMPDIR")); why != "" {
		t.Fatalf("child HOME is not a throwaway: %s", why)
	}
	if why := keychainReachable(home, dataDir); why != "" {
		t.Fatalf("the platform keychain is not proven unreachable from the child (%s); refusing to start the daemon", why)
	}
}

// keychainReachable says why the platform keychain is not proven
// unreachable from home, or "" when it is. darwin: the read-only
// default-keychain query under home must report no default keychain
// (custody then lands on the file vault, R-14.260); keychainProbeReason
// refuses every other outcome. Elsewhere: custody selection, which only
// probes a bus there, must pick the file vault.
func keychainReachable(home, dataDir string) string {
	if goruntime.GOOS == "darwin" {
		probe := exec.Command("/usr/bin/security", "default-keychain", "-d", "user")
		probe.Env = append(os.Environ(), "HOME="+home)
		var stdout, stderr strings.Builder
		probe.Stdout, probe.Stderr = &stdout, &stderr
		err := probe.Run() // before reading the buffers: arguments evaluate left to right
		return keychainProbeReason(stdout.String(), stderr.String(), err)
	}
	c, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: dataDir, Runner: testFailingCustodyRunner})
	if err != nil {
		return "custody selection failed: " + err.Error()
	}
	if c.Name() != "file-vault" {
		return "custody " + c.Name()
	}
	return ""
}

// swapConductorRegistration replaces the conductor Wire: the store is
// wrapped by siteStore when a site is set, and with a policy path the
// executor is built with the flag-file policy (wireDenyConductor).
func swapConductorRegistration(t *testing.T, o childOpts) {
	t.Helper()
	for i, r := range daemonRegistrations {
		if r.Name != "conductor" {
			continue
		}
		daemonRegistrations[i].Wire = func(w *daemonWiring) error {
			store := w.Store
			if o.site != "" {
				store = &siteStore{Store: w.Store, site: o.site, notify: o.notify}
			}
			if o.policy == "" {
				fan, err := wireConductorAndReachability(w.Ctx, w.Registry, w.Manifest, w.Paths, w.Clock, store, nodeTunnelLookup(w.Opts))
				w.ConductorFanOut = fan
				return err
			}
			fan, err := wireDenyConductor(w, store, flagPolicy{path: o.policy, n: new(atomic.Int32)})
			w.ConductorFanOut = fan
			return err
		}
		return
	}
	t.Fatal("no conductor registration to swap")
}

// wireDenyConductor is wireConductorAndReachability with one change: the
// security pipeline's policy is pol. Every other collaborator is the
// production constructor wireConductorExecute uses, over store;
// TestDenyConductorMirrorsProductionWiring fails when the two drift apart.
func wireDenyConductor(w *daemonWiring, store provider.Store, pol conductor.PolicyEvaluator) (daemon.ConductorFanOut, error) {
	regDB, err := openMigratedDB(w.Ctx, filepath.Join(w.Paths.DataDir(), providerRegistryDBFile), func(ctx context.Context, db *sql.DB) error {
		return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, w.Clock, "", "")
	})
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	reg := providerregistry.NewRegistry(regDB, w.Clock)
	credentials, err := daemonCredentialSource(w.Paths)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	resolver, err := providerdispatch.NewResolver(reg, credentials, w.Clock, providertransport.NewHTTPTransport(&http.Client{}))
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	security, err := conductorSecurity(w.Paths)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	security.Policy = pol
	accounting, err := wireConductorAccounting(w.Ctx, w.Paths, w.Clock, reg)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	return registerDenyConductor(w, store, reg, resolver, security, accounting)
}

// registerDenyConductor finishes wireDenyConductor exactly as
// wireConductorExecute and wireConductorAndReachability do.
func registerDenyConductor(w *daemonWiring, store provider.Store, reg *providerregistry.Registry, resolver conductor.ProviderResolver,
	security daemon.ConductorSecurity, accounting daemon.ConductorAccounting) (daemon.ConductorFanOut, error) {
	quotaCfg, _, err := conductor.ParseQuotaConfig(nil)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	fan := daemon.NewConductorFanOut(store, w.Clock)
	if err := daemon.RegisterConductorExecuteHandler(w.Registry, w.Manifest, providerregistry.NewReader(reg),
		conductor.NewQuotaPolicy(quotaCfg, w.Clock), resolver, audit.New(store, w.Clock, nil), w.Clock, security, accounting, fan,
		conductor.NodePlacement(nodes.Engine{Tunnels: nodeTunnelLookup(w.Opts)}, nodeRecordStore(w.Paths, w.Clock))); err != nil {
		return fan, err
	}
	return fan, daemon.WireReachability(w.Ctx, w.Manifest, w.Paths, w.Clock)
}

// flagPolicy allows every authorization while its flag file is absent.
// Once the file exists it allows the first N authorizations (N is the
// file's integer: 1 lets the re-attach's parent door pass) and refuses
// every later one, so the leg doors are what refuse.
type flagPolicy struct {
	path string
	n    *atomic.Int32
}

// Authorize implements conductor.PolicyEvaluator.
func (p flagPolicy) Authorize(context.Context, provider.ModelRequest) error {
	b, err := os.ReadFile(p.path)
	if os.IsNotExist(err) {
		return nil
	}
	if allow, convErr := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && convErr == nil && int(p.n.Add(1)) <= allow {
		return nil
	}
	return cascade.New(cascade.KindPolicyDenied, "test: policy now denies this request")
}

// siteStore fires once a committed write matches site: it writes notify
// when set, else SIGKILLs this process. Sites: cursor, request, marker,
// done:N (the Nth ok leg done).
type siteStore struct {
	provider.Store
	site, notify string
	mu           sync.Mutex
	dones        int
	fired        bool
}

// siteTx records every value a transaction writes.
type siteTx struct {
	provider.Tx
	vals *[]string
}

func (r siteTx) Put(ctx context.Context, ns, key string, v []byte) error {
	*r.vals = append(*r.vals, ns+"|"+string(v))
	return r.Tx.Put(ctx, ns, key, v)
}

func (r siteTx) CompareAndSwap(ctx context.Context, ns, key string, old, v []byte) error {
	*r.vals = append(*r.vals, ns+"|"+string(v))
	return r.Tx.CompareAndSwap(ctx, ns, key, old, v)
}

// Tx runs fn, then fires when a committed value matches the site.
func (s *siteStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	var vals []string
	err := s.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, siteTx{Tx: tx, vals: &vals}) })
	for _, v := range vals {
		if err == nil && s.hit(v) {
			s.fire()
		}
	}
	return err
}

// hit reports whether v is the site's write (journal values only for the
// cursor, done and marker sites).
func (s *siteStore) hit(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	journalValue := !strings.HasPrefix(v, "conductor.fanout.")
	if s.fired {
		return false
	}
	if journalValue && strings.Contains(v, "result_key") {
		s.dones++
	}
	want, _ := strconv.Atoi(strings.TrimPrefix(s.site, "done:"))
	s.fired = s.site == "cursor" && journalValue && strings.Contains(v, "request_key") ||
		s.site == "request" && strings.HasPrefix(v, "conductor.fanout.requests|") ||
		s.site == "marker" && journalValue && strings.Contains(v, "fanout_final") ||
		strings.HasPrefix(s.site, "done:") && s.dones == want
	return s.fired
}

// fire notifies the parent or dies by SIGKILL, mid-run.
func (s *siteStore) fire() {
	if s.notify != "" {
		_ = os.WriteFile(s.notify, []byte("hit"), 0o600)
		return
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}
