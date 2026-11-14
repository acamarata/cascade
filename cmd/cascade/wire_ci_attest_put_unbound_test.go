//go:build !windows

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// pinInstallationSigner installs a throwaway installation signer in the
// process memo for one test.
func pinInstallationSigner(t *testing.T) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	installationSignerState.mu.Lock()
	saved := installationSignerState.signer
	installationSignerState.signer = &installationSigner{pub: pub, priv: priv}
	installationSignerState.mu.Unlock()
	t.Cleanup(func() {
		installationSignerState.mu.Lock()
		installationSignerState.signer = saved
		installationSignerState.mu.Unlock()
	})
}

func TestAttestPutUnboundFailsUnavailable(t *testing.T) {
	f := newAttestFixture(t)
	pinInstallationSigner(t)
	wiring := captureWiring(t)
	c := composeElevated(t, true)
	w, ctx := *wiring, context.Background()
	dataDir := c.Deps.Paths.DataDir()
	writeTestDesignation(t, dataDir)
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), c.Deps.Clock)
	if _, err := records.Enroll(f.node, nodes.TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[nodes.repos]\n%q = [\"r1\"]\n", f.node.NodeID)
	writeConfig(t, c.Deps.Paths.ConfigPath(), body)
	var accepted acceptLooseningResult
	if err := c.Call(ctx, runtime.AcceptLooseningMethod, digestParams(body), &accepted); err != nil || !accepted.Reload.Accepted {
		t.Fatalf("config.accept_loosening: %+v, %v", accepted, err)
	}
	db := w.Deps.Fleet.JobsDB()
	nonce := f.accept(t, db, "r1", "ckpt-1")
	row := f.store(t, db, "r1", nonce, "ckpt-1", 0)
	payload, _ := json.Marshal(map[string]string{"job_id": "job-r1", "attestation_nonce": nonce})
	if _, err := w.Bus.Publish(ctx, "jobs.accepted", jobs.EventJobAccepted, "test", payload); err != nil {
		t.Fatal(err)
	}
	effects, err := openEffectLog(w.Store, w.Clock, w.Bus, w.Paths)
	if err != nil {
		t.Fatal(err)
	}
	key := attestPutKeyPrefix + f.node.NodeID + ":" + row.ID
	waitForEffect(t, effects, key, audit.EffectFailed)
	items, err := daemon.NewAttentionStore(w.Store, w.Clock, w.Bus).ListInScopes(ctx,
		[]supervision.ScopeRef{{Kind: scope.ScopeKindGlobal}}, supervision.Filter{})
	if err != nil || len(items) != 1 || items[0].SourceRef != key {
		t.Fatalf("attention items = %+v, %v; want one for %s", items, err, key)
	}
	out := nodes.AttestPutResult{}
	if err := unboundNodeChannel(ctx, nodes.AttestPutMethod, nodes.AttestPutRequest{}, &out); !cascade.HasKind(err, cascade.KindUnavailable) || out.Stored {
		t.Fatalf("unboundNodeChannel = %v, out %+v; want KindUnavailable and out unchanged", err, out)
	}
}

func waitForEffect(t *testing.T, effects *audit.EffectLog, key string, want audit.EffectPhase) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, found, err := effects.EffectState(context.Background(), key)
		if err == nil && found && st == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("effect %s never reached %s", key, want)
}
