// Package plugintest (grant.go): Purpose: the witnessed plugin.add
// InstallTrusted dispatches: the real rpc.ElevationMiddleware, answered
// with a signature from a fresh test key over an injected platform-tier
// custody, then InstallMiddleware and the production plugin.add decode and
// ProvisionElevated.
// Inputs: the plugin id, the manifest bytes, the plugin store.
// Outputs: the handler's error; the record ProvisionElevated saved.
// Constraints: nothing outside internal/rpc mints a witness, so this file
// cannot fake one: an unattested dispatch must draw the
// ELEVATION_REQUIRED challenge first.
// SPORT: internal/plugins/plugintest grant path (P1-PLG-06).
package plugintest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// installParams is the plugin.add wire params.
type installParams struct {
	ID            string `json:"id"`
	Checksum      string `json:"checksum"`
	ManifestBytes []byte `json:"manifest_bytes"`
}

// addParams is plugin.add's params for id, pinned to manifest's sha256.
func addParams(t testing.TB, id string, manifest []byte) json.RawMessage {
	t.Helper()
	sum := sha256.Sum256(manifest)
	raw, err := json.Marshal(installParams{ID: id, Checksum: hex.EncodeToString(sum[:]), ManifestBytes: manifest})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// addChain is InstallMiddleware over the production plugin.add decode and
// ProvisionElevated, writing into store.
func addChain(store provider.Store, db *sql.DB) rpc.HandlerFunc {
	domains := storage.NewPluginDomainRegistry()
	handler := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p installParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		m, err := plugin.ParseManifest(bytes.NewReader(p.ManifestBytes))
		if err != nil {
			return nil, err
		}
		return plugins.ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, fixedClock{}, "", "", store, domains, m, p.Checksum, false, nil)
	}
	return plugins.InstallMiddleware(nil, nil)("plugin.add", handler)
}

// attestedDispatch dispatches args to handler through the real elevation
// middleware gating plugin.add: first unattested, which must draw the
// challenge, then with a signed attestation over the challenge's nonce.
func attestedDispatch(t testing.TB, args json.RawMessage, handler rpc.HandlerFunc) error {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := fixedClock{}
	wrapped := rpc.ElevationMiddleware(rpc.ElevationDeps{
		Ledger: rpc.NewNonceLedger(clock), Trust: rpc.MapTrustStore{"fp": pub}, Clock: clock,
		Audit: rpc.LocalCeremonyAudit{}, Custody: func() (string, bool) { return "platform", true },
		Issuer: rpc.NewWitnessIssuer(),
	})("plugin.add", handler)
	_, challenge := wrapped(context.Background(), args)
	var eo *rpc.ErrorObject
	if !errors.As(challenge, &eo) || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unattested plugin.add = %v, want an ELEVATION_REQUIRED challenge", challenge)
	}
	data, _ := json.Marshal(eo.Data)
	var nonce struct{ Nonce string }
	_ = json.Unmarshal(data, &nonce)
	now := clock.Now()
	att := rpc.Attestation{RequestID: "req", ActionHash: rpc.ParamsHash(args), Method: "plugin.add", Nonce: nonce.Nonce,
		PubkeyFingerprint: "fp", IssuedUnix: now.Unix(), ExpUnix: now.Add(time.Minute).Unix()}
	att.SigB64 = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, rpc.AttestationSignedBytes(att)))
	env, err := json.Marshal(map[string]any{"_attestation": att, "_args": args})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrapped(context.Background(), env)
	return err
}
