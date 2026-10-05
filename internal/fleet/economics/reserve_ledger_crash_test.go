package economics

// Purpose: crash-injection proofs for the step ledger. The recipe is the
//   re-exec one: the parent re-runs this test binary as a child that
//   builds a real Reserver over a real SQLite file under the parent's
//   t.TempDir() and exits 3 at the injected site (or is killed with
//   SIGKILL after READY); the parent then recovers the row with a fresh
//   Reserver. Subsystem effects live in a JSON file so both processes
//   see (and count) the same leases and worktrees.
// SPORT: fleet/economics/reservation/ADD.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/topology"
)

const (
	crashDBEnv   = "CASCADE_RESERVE_CRASH_DB"
	crashFakeEnv = "CASCADE_RESERVE_CRASH_FAKE"
	crashSiteEnv = "CASCADE_RESERVE_CRASH_SITE"
)

type fakeLease struct {
	Repo, Job, Glob string
	Live            bool
}

type fakeWorktree struct {
	Lease string
	Live  bool
}

type fakeState struct {
	Seq       int                     `json:"seq"`
	Leases    map[string]fakeLease    `json:"leases"`
	Worktrees map[string]fakeWorktree `json:"worktrees"`
	Calls     map[string]int          `json:"calls"`
}

// durableFake is a lease and worktree subsystem whose state is one JSON
// file, so effects survive the crashing process.
type durableFake struct {
	mu   sync.Mutex
	path string
	site string // crash site; empty in the parent
}

func (d *durableFake) update(fn func(*fakeState)) fakeState {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := fakeState{Leases: map[string]fakeLease{}, Worktrees: map[string]fakeWorktree{}, Calls: map[string]int{}}
	if raw, err := os.ReadFile(d.path); err == nil {
		if err := json.Unmarshal(raw, &st); err != nil {
			panic(err)
		}
	}
	fn(&st)
	raw, err := json.Marshal(st)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(d.path, raw, 0o600); err != nil {
		panic(err)
	}
	return st
}

// crash exits 3 when the child reaches the named site.
func (d *durableFake) crash(site string) {
	if d.site == site {
		os.Exit(3)
	}
}

func (d *durableFake) acquire(_ context.Context, repo, job string, globs []string) ([]string, error) {
	d.crash("leases_pending")
	var ids []string
	d.update(func(s *fakeState) {
		s.Calls["acquire"]++
		for _, g := range globs {
			s.Seq++
			id := fmt.Sprintf("L%d", s.Seq)
			s.Leases[id] = fakeLease{Repo: repo, Job: job, Glob: g, Live: true}
			ids = append(ids, id)
		}
	})
	d.crash("leases_acquired")
	return ids, nil
}

func (d *durableFake) allocate(_ context.Context, lease string) (string, error) {
	d.crash("worktree_pending")
	path := "wt-" + lease
	d.update(func(s *fakeState) { s.Calls["allocate"]++; s.Worktrees[path] = fakeWorktree{Lease: lease, Live: true} })
	d.crash("worktree_acquired")
	return path, nil
}

func (d *durableFake) find(_ context.Context, repo, job string, globs []string) ([]string, error) {
	var ids []string
	d.update(func(s *fakeState) {
		for id, l := range s.Leases {
			for _, g := range globs {
				if l.Live && l.Repo == repo && l.Job == job && l.Glob == g {
					ids = append(ids, id)
				}
			}
		}
	})
	sort.Strings(ids)
	return ids, nil
}

func (d *durableFake) findWorktree(_ context.Context, lease string) (string, bool, error) {
	var path string
	d.update(func(s *fakeState) {
		for p, w := range s.Worktrees {
			if w.Live && w.Lease == lease {
				path = p
			}
		}
	})
	return path, path != "", nil
}

func (d *durableFake) seams() ReserverSeams {
	return ReserverSeams{
		Buckets: func(context.Context, string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
			d.crash("quota_pending")
			out := map[string]topology.Bucket{}
			for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
				out[dim] = topology.Bucket{Name: dim, Limit: 1 << 40, LimitScopeID: "scope-1", CapacityObserved: 1 << 40}
			}
			return topology.QuotaDomainAPIProject, "scope-1", out, nil
		},
		ReserveFraction: func(context.Context, string) (float64, error) { return 0, nil },
		Projects:        &ActiveProjectCount{},
		Permit: func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
			return governor.Permit{}, nil
		},
		AcquireLeases: d.acquire,
		ReleaseLeases: func(_ context.Context, ids []string) error {
			d.update(func(s *fakeState) {
				s.Calls["release"]++
				for _, id := range ids {
					l := s.Leases[id]
					l.Live = false
					s.Leases[id] = l
				}
			})
			return nil
		},
		AllocateWorktree: d.allocate,
		RemoveWorktree: func(_ context.Context, path string) error {
			d.update(func(s *fakeState) {
				s.Calls["remove"]++
				s.Worktrees[path] = fakeWorktree{Lease: s.Worktrees[path].Lease}
			})
			return nil
		},
		ValidateLeases: func(_ context.Context, _ string, ids []string) ([]string, error) { return ids, nil },
		RenewLeases:    func(context.Context, []string) error { return nil },
		FindLeases:     d.find,
		FindWorktree:   d.findWorktree,
		RaiseAttention: func(context.Context, string, string) error { return nil },
	}
}

// TestReserveCrashHelperProcess is the re-exec'd child. In a normal run
// the site variable is unset and it returns at once.
func TestReserveCrashHelperProcess(_ *testing.T) {
	site := os.Getenv(crashSiteEnv)
	if site == "" {
		return
	}
	db, err := sql.Open("sqlite", os.Getenv(crashDBEnv))
	if err != nil {
		os.Exit(5)
	}
	db.SetMaxOpenConns(1)
	store, _ := NewReservationStore(db, newTestClock())
	fake := &durableFake{path: os.Getenv(crashFakeEnv), site: site}
	rv, err := NewReserver(store, newTestClock(), "epoch-child", fake.seams())
	if err != nil {
		os.Exit(6)
	}
	if site == "kill9" {
		runKill9Child(rv)
	}
	_, err = rv.Reserve(context.Background(), baseReserveRequest())
	_, _ = fmt.Fprintf(os.Stderr, "helper: Reserve returned without crashing: %v\n", err)
	os.Exit(4)
}

// runKill9Child holds one held and one committed reservation, prints
// READY and blocks until the parent kills it.
func runKill9Child(rv *Reserver) {
	ctx := context.Background()
	if _, err := rv.Reserve(ctx, baseReserveRequest()); err != nil {
		os.Exit(7)
	}
	c, err := rv.Reserve(ctx, baseReserveRequest())
	if err != nil {
		os.Exit(7)
	}
	if _, err := rv.Commit(ctx, c.ID); err != nil {
		os.Exit(7)
	}
	_, _ = fmt.Fprintln(os.Stdout, "READY")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	os.Exit(8)
}

// runCrashChild re-execs the helper at site and requires exit status 3.
func runCrashChild(t *testing.T, dbPath, fakePath, site string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReserveCrashHelperProcess$")
	cmd.Env = append(os.Environ(), crashDBEnv+"="+dbPath, crashFakeEnv+"="+fakePath, crashSiteEnv+"="+site)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("child at %s: err=%v output=%s, want exit status 3", site, err, out)
	}
}
