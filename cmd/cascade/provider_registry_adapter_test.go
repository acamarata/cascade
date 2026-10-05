// Purpose: pool membership through registryAdapter over the DURABLE
//   registry production uses: ListPool returns the real members from their
//   lanes, a re-add without --pool keeps pool and index and leaves one
//   lane, a re-add naming another pool refuses KindConflict with no vault
//   write or provider call, and a join takes max+1 after a removal.
// Inputs: testProviderDepsDurable/runProvider (provider_add_list_test.go,
//   provider_cmd_test.go), with custody forced to a file vault in
//   t.TempDir() and a fresh HOME/USERPROFILE.
// Outputs: none.
// Constraints: no network (fakeProviderDoer, --no-verify), no platform
//   keychain (the runner always fails and the file vault is forced).
// SPORT: cli.provider.add/ADD (pool membership).

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// poolTestDeps is the durable-registry deps with custody pinned to a file
// vault under t.TempDir() and HOME/USERPROFILE redirected.
func poolTestDeps(t *testing.T) providerDeps {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	deps := testProviderDepsDurable(t)
	vaultDir := t.TempDir()
	deps.NewCustody = func() (secrets.Custody, error) {
		return secrets.SelectCustody(secrets.Config{
			Service: "cascade-provider-pool-test", Dir: vaultDir, Passphrase: "cli-test-pass",
			Runner: alwaysFailRunner, ForceFileVault: true,
		})
	}
	return deps
}

// addPooled runs `provider add <name> --key --no-verify [--pool pool] --json`
// and returns the JSON stdout.
func addPooled(t *testing.T, deps providerDeps, name, pool string) (string, error) {
	t.Helper()
	args := []string{"add", name, "--key", "--no-verify", "--json"}
	if pool != "" {
		args = append(args, "--pool", pool)
	}
	stdout, _, err := runProvider(t, deps, args...)
	return stdout, err
}

// withAdapter runs fn against a registryAdapter over a fresh handle on the
// test's providers.db, closing it before returning.
func withAdapter(t *testing.T, deps providerDeps, fn func(a registryAdapter, reg *registry.Registry)) {
	t.Helper()
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	defer func() { _ = store.Close() }()
	fn(registryAdapter{reg: store.Registry}, store.Registry)
}

// addedRecord decodes the record from `provider add --json` output.
func addedRecord(t *testing.T, stdout string) intake.ProviderRecord {
	t.Helper()
	var env struct {
		Data intake.AddResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil || env.Data.Record.Name == "" {
		t.Fatalf("decode add --json output %q: %v", stdout, err)
	}
	return env.Data.Record
}

// seedPool adds each name to pool in order, failing the test on any error.
func seedPool(t *testing.T, deps providerDeps, pool string, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := addPooled(t, deps, n, pool); err != nil {
			t.Fatalf("add %s --pool %s: %v", n, pool, err)
		}
	}
}

func TestAddReaddWithoutPoolKeepsMembership(t *testing.T) {
	deps := poolTestDeps(t)
	seedPool(t, deps, "p", "a", "b")
	stdout, err := addPooled(t, deps, "b", "")
	if err != nil {
		t.Fatalf("re-add b without --pool: %v", err)
	}
	if rec := addedRecord(t, stdout); rec.Pool != "p" || rec.PoolIndex != 1 {
		t.Fatalf("re-add wrote pool %q index %d, want p 1", rec.Pool, rec.PoolIndex)
	}
	withAdapter(t, deps, func(a registryAdapter, _ *registry.Registry) {
		got, err := a.GetProvider(context.Background(), "b")
		if err != nil || got.Pool != "p" || got.PoolIndex != 1 {
			t.Fatalf("GetProvider(b) = pool %q index %d (err %v), want p 1", got.Pool, got.PoolIndex, err)
		}
	})
}

func TestAddReaddLeavesNoSecondLane(t *testing.T) {
	deps := poolTestDeps(t)
	seedPool(t, deps, "p", "a")
	if _, err := addPooled(t, deps, "a", ""); err != nil {
		t.Fatalf("re-add a without --pool: %v", err)
	}
	withAdapter(t, deps, func(_ registryAdapter, reg *registry.Registry) {
		lanes, err := reg.ListLanes(context.Background())
		if err != nil || len(lanes) != 1 || lanes[0].LaneName != "p/a" || lanes[0].ProviderName != "a" {
			t.Fatalf("lanes = %+v (err %v), want exactly one lane p/a", lanes, err)
		}
	})
}

func TestListPoolReturnsMembers(t *testing.T) {
	deps := poolTestDeps(t)
	seedPool(t, deps, "p", "c", "a", "b")
	seedPool(t, deps, "other", "d")
	seedPool(t, deps, "", "solo")
	withAdapter(t, deps, func(a registryAdapter, _ *registry.Registry) {
		got, err := a.ListPool(context.Background(), "p")
		want := []struct {
			name string
			idx  int
		}{{"a", 1}, {"b", 2}, {"c", 0}}
		if err != nil || len(got) != len(want) {
			t.Fatalf("ListPool(p) = %+v (err %v), want a, b, c", got, err)
		}
		for i, w := range want {
			if got[i].Name != w.name || got[i].Pool != "p" || got[i].PoolIndex != w.idx || got[i].Driver != intake.DriverAnthropic {
				t.Fatalf("ListPool(p)[%d] = %+v, want %s in pool p at index %d", i, got[i], w.name, w.idx)
			}
		}
	})
}

// poolWriteCustody counts the writes that reach the vault.
type poolWriteCustody struct {
	secrets.Custody
	writes *int
}

func (c poolWriteCustody) Set(ctx context.Context, name string, v []byte) error {
	*c.writes++
	return c.Custody.Set(ctx, name, v)
}

func (c poolWriteCustody) Delete(ctx context.Context, name string) error {
	*c.writes++
	return c.Custody.Delete(ctx, name)
}

// poolCallDoer counts the provider calls that leave the process.
type poolCallDoer struct {
	intake.Doer
	calls *int
}

func (d poolCallDoer) Do(ctx context.Context, req intake.HTTPRequest) (intake.HTTPResponse, error) {
	*d.calls++
	return d.Doer.Do(ctx, req)
}

// vaultHolds reports whether any stored value contains secret.
func vaultHolds(t *testing.T, deps providerDeps, secret string) bool {
	t.Helper()
	cust, err := deps.NewCustody()
	if err != nil {
		t.Fatalf("NewCustody: %v", err)
	}
	names, err := cust.List(context.Background())
	if err != nil {
		t.Fatalf("custody List: %v", err)
	}
	for _, n := range names {
		v, err := cust.Get(context.Background(), n)
		if err != nil || strings.Contains(string(v), secret) {
			return true
		}
	}
	return false
}

func TestAddReaddOtherPoolRefuses(t *testing.T) {
	deps := poolTestDeps(t)
	seedPool(t, deps, "p", "a")
	seedPool(t, deps, "", "solo")
	writes, calls := 0, 0
	newCustody, inner := deps.NewCustody, deps.Doer
	deps.NewCustody = func() (secrets.Custody, error) {
		c, err := newCustody()
		return poolWriteCustody{Custody: c, writes: &writes}, err
	}
	deps.Doer = poolCallDoer{Doer: inner, calls: &calls}
	const canary = "sk-ant-canary-must-never-be-stored"
	deps.ReadStdin = func() ([]byte, error) { return []byte(canary + "\n"), nil }
	for _, name := range []string{"a", "solo"} {
		stdout, stderr, err := runProvider(t, deps, "add", name, "--key", "--no-verify", "--pool", "q")
		if !cascade.HasKind(err, cascade.KindConflict) {
			t.Fatalf("re-add %s --pool q: %v, want KindConflict", name, err)
		}
		if all := err.Error() + stdout + stderr; strings.Contains(all, canary) {
			t.Fatalf("re-add %s: the refused key leaked into the output: %q", name, all)
		}
	}
	if writes != 0 || calls != 0 || vaultHolds(t, deps, canary) {
		t.Fatalf("refused re-adds made %d vault writes and %d provider calls, vault holds canary: %v; want none",
			writes, calls, vaultHolds(t, deps, canary))
	}
	withAdapter(t, deps, func(_ registryAdapter, reg *registry.Registry) {
		lanes, err := reg.ListLanes(context.Background())
		if err != nil || len(lanes) != 2 || lanes[0].LaneName != "p/a" || lanes[1].LaneName != "solo" {
			t.Fatalf("lanes after refusals = %+v (err %v), want p/a and solo only", lanes, err)
		}
	})
}

// TestPoolJoinTakesMaxPlusOneAfterRemoval: with {a:0,b:1,c:2} and a removed
// the pool holds {b:1,c:2}; the next member gets 3 (max+1), not 2 (count,
// which collides with c). The first member of an empty pool gets 0.
func TestPoolJoinTakesMaxPlusOneAfterRemoval(t *testing.T) {
	deps := poolTestDeps(t)
	stdout, err := addPooled(t, deps, "first", "empty")
	if err != nil {
		t.Fatalf("first add into an empty pool: %v", err)
	}
	if rec := addedRecord(t, stdout); rec.PoolIndex != 0 {
		t.Fatalf("first member of an empty pool got index %d, want 0", rec.PoolIndex)
	}
	seedPool(t, deps, "p", "a", "b", "c")
	if _, _, err := runProvider(t, deps, "remove", "a"); err != nil {
		t.Fatalf("remove a: %v", err)
	}
	stdout, err = addPooled(t, deps, "d", "p")
	if err != nil {
		t.Fatalf("add d --pool p: %v", err)
	}
	if rec := addedRecord(t, stdout); rec.PoolIndex != 3 {
		t.Fatalf("d joined pool p at index %d, want 3 (max+1 over {b:1,c:2}); a count would give 2", rec.PoolIndex)
	}
}

// TestListPoolReadErrorIsReturned: a failed registry read surfaces as an
// error, never as an empty pool intake would index from 0.
func TestListPoolReadErrorIsReturned(t *testing.T) {
	deps := poolTestDeps(t)
	seedPool(t, deps, "p", "a")
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	_ = store.Close()
	got, err := registryAdapter{reg: store.Registry}.ListPool(context.Background(), "p")
	if err == nil || got != nil {
		t.Fatalf("ListPool on a closed store = %+v, %v; want nil and an error", got, err)
	}
}
