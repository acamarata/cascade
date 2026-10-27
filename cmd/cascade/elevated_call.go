// Purpose: compose elevated RPC signing from selected custody.
// Inputs: client, custody, clock and environment. Outputs: result or refusal.
// Constraints: no-input precedes custody; only Signer grants signing authority.
// SPORT: elevated CLI calls.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	goruntime "runtime"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// elevatedCallDeps carries the injected seams callElevated signs through.
type elevatedCallDeps struct {
	Custody func() elevation.Custody
	Clock   runtime.Clock
	Getenv  runtime.Getenv
	Stderr  io.Writer
}

// productionElevatedCallDeps wires the selected custody, the system clock
// and the process environment for a CLI verb.
func productionElevatedCallDeps(stderr io.Writer) elevatedCallDeps {
	return elevatedCallDeps{Custody: productionCustody, Clock: runtime.NewSystemClock(), Getenv: os.Getenv, Stderr: stderr}
}

// callElevated is the only path an elevated CLI verb dials. The first call
// goes out unsigned; only a server challenge reaches the attest step, which
// refuses on Windows, then under CASCADE_NO_INPUT=1 before custody is
// consulted, then signs through Custody().Signer() alone.
func callElevated(ctx context.Context, c *client.Client, deps elevatedCallDeps, method string, params, out any) error {
	return c.DoElevated(ctx, method, params, out, func(_ context.Context, method, hash, nonce string) (json.RawMessage, error) {
		if goruntime.GOOS == "windows" {
			return nil, elevation.ErrWindowsTier2()
		}
		getenv := deps.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		if getenv("CASCADE_NO_INPUT") == "1" {
			return nil, elevation.ErrNoInput()
		}
		ks, err := deps.Custody().Signer()
		if err != nil {
			return nil, err
		}
		att, err := signAttestation(ks, deps.Clock, method, hash, nonce)
		if err != nil {
			return nil, err
		}
		return json.Marshal(att)
	})
}

// signAttestation is the one signing primitive: it binds method, actionHash
// and the server nonce to the keystore's key for attestationTTL, over
// rpc.AttestationSignedBytes. backup_elevation.go reuses it for the
// in-process backup ceremony.
func signAttestation(ks elevation.ElevationKeystore, clock runtime.Clock, method, actionHash, nonce string) (rpc.Attestation, error) {
	encoded, err := ks.PubKeyB64()
	if err != nil {
		return rpc.Attestation{}, err
	}
	fingerprint, err := elevation.Fingerprint(encoded)
	if err != nil {
		return rpc.Attestation{}, err
	}
	requestID, err := cascade.NewID()
	if err != nil {
		return rpc.Attestation{}, cascade.Wrap(cascade.KindInternal, err, "elevation: mint request id")
	}
	now := clock.Now()
	att := rpc.Attestation{RequestID: string(requestID), ActionHash: actionHash, Method: method, Nonce: nonce,
		PubkeyFingerprint: fingerprint, IssuedUnix: now.Unix(), ExpUnix: now.Add(attestationTTL).Unix()}
	sig, err := ks.Sign(rpc.AttestationSignedBytes(att))
	if err != nil {
		return rpc.Attestation{}, err
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	return att, nil
}
