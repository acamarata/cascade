//go:build !windows

// Purpose: the real process handshaker: an update whose candidate
// executable fails the handshake leaves the prior record byte-identical; a
// candidate that comes up commits. Both the attested plugin.update handler
// and the non-elevated UpdatePlugin path launch only an executable a witness
// pinned.
// SPORT: internal/plugins process update handshaker tests (P1-PLG-03).
package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// githubAt is the real cascade-github manifest at version v.
func githubAt(t *testing.T, v string) string {
	t.Helper()
	m := githubManifest(t)
	out := strings.Replace(m, `version = "0.1.0"`, `version = "`+v+`"`, 1)
	if out == m {
		t.Fatal("the cascade-github manifest no longer carries version 0.1.0")
	}
	return out
}

// storedRecord is id's raw stored record bytes.
func (f *launchFix) storedRecord(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := f.store.Get(context.Background(), HostNamespace, metadataKey(id))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProcessHandshakerRollbackReal(t *testing.T) {
	f := newLaunchFix(t)
	f.installGitHub(t)
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	before := f.storedRecord(t, cascadeGitHubPluginID)
	handler := UpdateRPCHandler(f.store, NewProcessHandshaker)

	f.installMarker(t, cascadeGitHubPluginID) // a candidate that never answers the handshake
	err := attested(t, UpdateRPCMethod, UpdateRPCMethod, pinnedArgs(t, cascadeGitHubPluginID, githubAt(t, "0.1.1")), handler)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "handshake failed") {
		t.Fatalf("attested update with a failing candidate = %v, want the handshake refusal", err)
	}
	if after := f.storedRecord(t, cascadeGitHubPluginID); !bytes.Equal(after, before) {
		t.Fatalf("a failed handshake changed the record:\n before %s\n after  %s", before, after)
	}

	exe := f.installGitHub(t)
	if err := attested(t, UpdateRPCMethod, UpdateRPCMethod, pinnedArgs(t, cascadeGitHubPluginID, githubAt(t, "0.1.1")), handler); err != nil {
		t.Fatalf("attested update with a passing candidate: %v", err)
	}
	rec, _, _ := LoadMetadata(context.Background(), f.store, cascadeGitHubPluginID)
	digest, _ := ExecutableDigest(exe)
	if rec.InstalledVersion != "0.1.1" || rec.TrustTier != TrustTrusted || rec.ExecDigest != digest {
		t.Fatalf("record after a passing update = %+v, want 0.1.1, trusted, digest %s", rec, digest)
	}
	requireNonElevatedUpdate(t, f, exe)
	ctx := withGrantedDigest(context.Background(), cascadeGitHubPluginID, "d1")
	if d, ok := grantedDigest(ctx, cascadeGitHubPluginID); !ok || d != "d1" {
		t.Fatalf("grantedDigest = %q, %v; want the pinned digest", d, ok)
	}
	if _, ok := grantedDigest(ctx, "trustdemo"); ok {
		t.Fatal("a digest pinned for one plugin was read for another")
	}
}

// requireNonElevatedUpdate: the CLI's UpdatePlugin path with the real
// handshaker commits over the pinned executable and rolls back once the
// executable no longer matches the stored digest.
func requireNonElevatedUpdate(t *testing.T, f *launchFix, exe string) {
	t.Helper()
	ctx := context.Background()
	m := githubAt(t, "0.1.2")
	res, err := UpdatePlugin(ctx, f.store, nil, NewProcessHandshaker(), []byte(m), sha256OfHex([]byte(m)), []byte(m), true)
	if err != nil || res.Outcome != UpdateOutcomeUpdated {
		t.Fatalf("non-elevated update over the pinned executable = %+v, %v; want updated", res, err)
	}
	before := f.storedRecord(t, cascadeGitHubPluginID)
	appendByte(t, exe)
	m = githubAt(t, "0.1.3")
	res, err = UpdatePlugin(ctx, f.store, nil, NewProcessHandshaker(), []byte(m), sha256OfHex([]byte(m)), []byte(m), true)
	if err != nil || res.Outcome != UpdateOutcomeRolledBack || !cascade.HasKind(res.RollbackErr, cascade.KindPolicyDenied) {
		t.Fatalf("non-elevated update over a changed executable = %+v, %v; want rolled back (policy denied)", res, err)
	}
	if after := f.storedRecord(t, cascadeGitHubPluginID); !bytes.Equal(after, before) {
		t.Fatal("a rolled-back update changed the record")
	}
}

// TestUpdateBuiltinNeverHandshakes: wiring the real handshaker does not make
// a builtin update launch anything: it commits with no launch effect.
func TestUpdateBuiltinNeverHandshakes(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	rec := PluginMetadata{Name: "demo", InstalledVersion: "1.0.0", Enabled: true, RuntimeMode: plugin.RuntimeBuiltin}
	v2 := strings.Replace(builtinManifest, `version = "1.0.0"`, `version = "1.1.0"`, 1)
	if err := SaveMetadata(ctx, f.store, rec); err != nil {
		t.Fatal(err)
	}
	before := len(f.allRecords(t))
	res, err := UpdatePlugin(ctx, f.store, nil, NewProcessHandshakerOver(f.store), []byte(v2), "", nil, true)
	if err != nil || res.Outcome != UpdateOutcomeUpdated {
		t.Fatalf("builtin update with the real handshaker = %+v, %v; want updated", res, err)
	}
	if err := NewProcessHandshaker().Handshake(ctx, "demo", "1.2.0"); err != nil {
		t.Fatalf("the launch-env handshaker over a builtin record = %v, want nil", err)
	}
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("a builtin update wrote %d effect records, want none", after-before)
	}
}

// TestProcessHandshakerRefusals: the handshaker launches only what a trusted
// record or an attested grant pinned. No wired environment, an unreadable
// store, an unknown plugin, an untrusted record and an unpinned record each
// refuse before any launch effect is written.
func TestProcessHandshakerRefusals(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	exe := f.installMarker(t, "trustdemo")
	// A process record that was never trusted, though it carries a digest.
	proc := PluginMetadata{Name: "trustdemo", Enabled: true, RuntimeMode: plugin.RuntimeProcess, ExecDigest: "abc"}
	if err := SaveMetadata(ctx, f.store, proc); err != nil {
		t.Fatal(err)
	}
	before := len(f.allRecords(t))

	t.Run("untrusted process record", func(t *testing.T) {
		err := NewProcessHandshaker().Handshake(ctx, "trustdemo", "1.0.0")
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "no trusted executable to confirm") ||
			!strings.Contains(err.Error(), "cascade plugin update trustdemo") {
			t.Fatalf("handshake over an untrusted record = %v, want the trust refusal", err)
		}
	})
	t.Run("unknown plugin", func(t *testing.T) {
		err := NewProcessHandshakerOver(f.store).Handshake(ctx, "ghost", "1.0.0")
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "ghost has no trusted executable") {
			t.Fatalf("handshake of an unknown plugin = %v, want the trust refusal", err)
		}
	})
	t.Run("trusted but unpinned", func(t *testing.T) {
		rec := proc
		rec.TrustTier, rec.ExecDigest = TrustTrusted, "" // trusted yet carrying no executable digest
		if err := SaveMetadata(ctx, f.store, rec); err != nil {
			t.Fatal(err)
		}
		if err := NewProcessHandshaker().Handshake(ctx, "trustdemo", "1.0.0"); !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("handshake over a record without a digest = %v, want a policy refusal", err)
		}
	})
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("refused handshakes wrote %d effect records, want none", after-before)
	}
	t.Run("unreadable store", func(t *testing.T) {
		closeStore(t, f.store)
		for name, h := range map[string]ProcessHandshaker{"over the store": NewProcessHandshakerOver(f.store), "via the environment": NewProcessHandshaker()} {
			err := h.Handshake(ctx, "trustdemo", "1.0.0")
			if err == nil || cascade.HasKind(err, cascade.KindPolicyDenied) {
				t.Fatalf("handshake %s with a closed store = %v, want the read error, not a trust verdict", name, err)
			}
		}
	})
	if _, serr := os.Stat(exe + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("a refused handshake ran the executable")
	}
}

// TestProcessHandshakerNeedsALaunchEnvironment: with no environment wired
// the handshaker refuses whether or not a grant pinned a digest.
func TestProcessHandshakerNeedsALaunchEnvironment(t *testing.T) {
	ctx := context.Background()
	defer SetLaunchEnvSource(nil)()
	for name, c := range map[string]context.Context{"unpinned": ctx, "pinned": withGrantedDigest(ctx, "trustdemo", "d1")} {
		if err := NewProcessHandshaker().Handshake(c, "trustdemo", "1.0.0"); !cascade.HasKind(err, cascade.KindUnavailable) ||
			!strings.Contains(err.Error(), "no plugin launch environment") {
			t.Fatalf("%s handshake without an environment = %v, want the unavailable refusal", name, err)
		}
	}
}
