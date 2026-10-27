// Purpose: UpdateRPCHandler's witness check and guards, driven through the
// real rpc.ElevationMiddleware (nothing outside internal/rpc can mint a
// witness, so these tests cannot fake one either).
// SPORT: internal/plugins plugin.update RPC tests.
package plugins

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestedCall dispatches args to handler through a real elevation
// middleware gating gateMethod, answering its challenge with a signature
// from a fresh test key, so handler runs under a genuine witness for
// gateMethod and args.
func attestedCall(t *testing.T, gateMethod string, args json.RawMessage, handler rpc.HandlerFunc) (any, error) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("Windows tier-2 refuses elevation before the attestation flow")
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	wrapped := rpc.ElevationMiddleware(rpc.ElevationDeps{
		Ledger: rpc.NewNonceLedger(clock), Trust: rpc.MapTrustStore{"fp": pub}, Clock: clock,
		Audit: rpc.LocalCeremonyAudit{}, Custody: func() (string, bool) { return "platform", true },
		Issuer: rpc.NewWitnessIssuer(),
	})(gateMethod, handler)
	_, challenge := wrapped(context.Background(), args)
	var eo *rpc.ErrorObject
	if !errors.As(challenge, &eo) || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unattested %s = %v, want an ELEVATION_REQUIRED challenge", gateMethod, challenge)
	}
	data, _ := json.Marshal(eo.Data)
	var nonce struct{ Nonce string }
	_ = json.Unmarshal(data, &nonce)
	hash := rpc.ParamsHash(args)
	now := clock.Now()
	att := rpc.Attestation{RequestID: "req", ActionHash: hash, Method: gateMethod, Nonce: nonce.Nonce, PubkeyFingerprint: "fp",
		IssuedUnix: now.Unix(), ExpUnix: now.Add(time.Minute).Unix()}
	att.SigB64 = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, rpc.AttestationSignedBytes(att)))
	env, _ := json.Marshal(map[string]any{"_attestation": att, "_args": args})
	return wrapped(context.Background(), env)
}

func updateArgs(t *testing.T, id, manifest string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(UpdateParams{ID: id, ManifestBytes: []byte(manifest)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestUpdateRPCHandlerRefusesWithoutWitness(t *testing.T) {
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)
	before := storedRecordHash(t, store, "demo")
	handler := UpdateRPCHandler(store, nil)
	args := updateArgs(t, "demo", expandedManifest)
	unchanged := func(label string) {
		t.Helper()
		if after := storedRecordHash(t, store, "demo"); after != before {
			t.Fatalf("%s changed the stored record", label)
		}
	}

	_, err := handler(context.Background(), args)
	requireRefusal(t, err, cascade.KindElevationRequired, "no verified elevation")
	unchanged("a direct unattested call")

	// A genuine witness for plugin.add (an IsVerified-only handler would
	// accept it) is not a witness for plugin.update.
	_, err = attestedCall(t, "plugin.add", args, handler)
	requireRefusal(t, err, cascade.KindElevationRequired, "no verified elevation")
	unchanged("a plugin.add witness")

	// A plugin.update witness for other params does not cover these.
	other := updateArgs(t, "demo", updatedManifest)
	_, err = attestedCall(t, UpdateRPCMethod, other, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return handler(ctx, args)
	})
	requireRefusal(t, err, cascade.KindElevationRequired, "no verified elevation")
	unchanged("a witness for other params")

	// Positive control: the matching witness commits, once.
	res, err := attestedCall(t, UpdateRPCMethod, args, handler)
	if err != nil {
		t.Fatalf("attested plugin.update: %v", err)
	}
	rec, ok, _ := LoadMetadata(context.Background(), store, "demo")
	if !ok || rec.InstalledVersion != "1.1.0" || strings.Join(rec.Grants, ",") != "net.http" {
		t.Fatalf("record after an attested update = %+v, want v1.1.0 with net.http", rec)
	}
	if got := res.(updateRPCResult); got.Message != "updated demo to v1.1.0" || strings.Join(got.Grants, ",") != "net.http" {
		t.Fatalf("result = %+v", got)
	}
}

// assertElevatedRemoteUpdateRefused is TestUpdatePlugin_ToRemoteRuntimeRefused's
// elevated leg: an attested plugin.update carrying a remote manifest.
func assertElevatedRemoteUpdateRefused(t *testing.T) {
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)
	before := storedRecordHash(t, store, "demo")
	_, err := attestedCall(t, UpdateRPCMethod, updateArgs(t, "demo", updateRemoteManifest), UpdateRPCHandler(store, nil))
	requireRefusal(t, err, cascade.KindUnsupported, "DEF-P2-remote-plugin-runtime")
	if after := storedRecordHash(t, store, "demo"); after != before {
		t.Fatal("an attested remote update changed the stored record")
	}
}

// TestUpdateRPCHandlerReRunsGuards: an attested call still meets every
// UpdatePlugin guard, and each refusal leaves the store as it was.
func TestUpdateRPCHandlerReRunsGuards(t *testing.T) {
	failing := func() ProcessHandshaker { return alwaysFailHandshaker{err: errors.New("hello_ack timed out")} }
	cases := []struct {
		name, id, manifest, checksum string
		seed                         bool
		handshaker                   func() ProcessHandshaker
		kind                         cascade.Kind
		fragment                     string
	}{
		{"uninstalled", "demo", updatedManifest, "", false, nil, cascade.KindNotFound, "cascade plugin add"},
		{"id mismatch", "other", updatedManifest, "", true, nil, cascade.KindInvalidInput, "does not match requested id"},
		{"process without handshaker", "demo", updateProcessTierManifest, "", true, nil, cascade.KindUnavailable, "cannot confirm the process handshake"},
		{"handshake fails", "demo", updateProcessTierManifest, "", true, failing, cascade.KindUnavailable, "handshake failed"},
		{"checksum mismatch", "demo", updatedManifest, "sha256:" + strings.Repeat("0", 64), true, nil, cascade.KindIntegrity, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storetest.NewMemStore()
			if tc.seed {
				seedInstalled(t, store, "1.0.0", nil)
			}
			before := storedRecordHash(t, store, "demo")
			raw, _ := json.Marshal(UpdateParams{ID: tc.id, Checksum: tc.checksum, ManifestBytes: []byte(tc.manifest)})
			_, err := attestedCall(t, UpdateRPCMethod, raw, UpdateRPCHandler(store, tc.handshaker))
			if kind, _ := cascade.KindOf(err); kind != tc.kind || !strings.Contains(errString(err), tc.fragment) {
				t.Fatalf("error = %v, want %v naming %q", err, tc.kind, tc.fragment)
			}
			if after := storedRecordHash(t, store, "demo"); after != before {
				t.Fatal("a refused attested update changed the stored record")
			}
		})
	}
}

func TestUpdateRPCHandlerRefusesMalformedParamsAndNoStore(t *testing.T) {
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)
	_, err := attestedCall(t, UpdateRPCMethod, json.RawMessage(`{"id":"demo","grants":["x"]}`), UpdateRPCHandler(store, nil))
	requireRefusal(t, err, cascade.KindInvalidInput, "decode params")
	_, err = attestedCall(t, UpdateRPCMethod, updateArgs(t, "demo", updatedManifest), UpdateRPCHandler(nil, nil))
	requireRefusal(t, err, cascade.KindUnavailable, "no plugin store")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
