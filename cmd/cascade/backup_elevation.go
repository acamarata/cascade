// Purpose: run the complete nonce, local-auth signature, and middleware
// verification flow before minting a backup.ElevationProof.
// Inputs: a backup method, its exact JSON params, confirmation, and injected
// elevation dependencies.
// Outputs: a single-use verified proof or a typed refusal.
// Constraints: CASCADE_NO_INPUT never prompts; Windows returns ErrWindowsTier2.
// SPORT: cmd.cascade.backup-elevation/ADD (P1-E19-W4-S42-T3).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

type backupElevationChallenge struct {
	Nonce string `json:"nonce"`
}

type backupElevatedEnvelope struct {
	Attestation rpc.Attestation `json:"_attestation"`
	Args        json.RawMessage `json:"_args"`
}

func newBackupAuthorizer(deps elevateHelperDeps) backupAuthorizeFunc {
	return func(ctx context.Context, cmd *cobra.Command, method string, params []byte, yes bool) (backup.ElevationProof, error) {
		ks, err := backupKeystore(deps)
		if err != nil {
			return "", err
		}
		if err := confirmBackupOperation(cmd, deps, method, yes); err != nil {
			return "", err
		}
		if deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1" {
			return "", elevation.ErrNoInput()
		}
		trust, err := backupTrustStore(deps)
		if err != nil {
			return "", err
		}
		return attestBackupOperation(ctx, deps, ks, trust, method, json.RawMessage(params))
	}
}

func backupKeystore(deps elevateHelperDeps) (elevation.ElevationKeystore, error) {
	if deps.Keystore == nil {
		return nil, cascade.New(cascade.KindUnavailable, "backup: no elevation keystore is configured")
	}
	ks := deps.Keystore()
	if ks == nil {
		return nil, cascade.New(cascade.KindUnavailable, "backup: elevation keystore is unavailable")
	}
	if ks.Tier() == elevation.TierWindowsTier2 {
		return nil, elevation.ErrWindowsTier2()
	}
	return ks, nil
}

func confirmBackupOperation(cmd *cobra.Command, deps elevateHelperDeps, method string, yes bool) error {
	if yes {
		return nil
	}
	if deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1" {
		return cascade.Newf(cascade.KindElevationRequired,
			"%s requires --yes when CASCADE_NO_INPUT=1; no prompt was attempted", method)
	}
	if _, err := cmd.ErrOrStderr().Write([]byte(method + " changes backup state. Continue? [y/N] ")); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: write confirmation prompt")
	}
	line := make([]byte, 16)
	n, _ := cmd.InOrStdin().Read(line)
	answer := strings.ToLower(strings.TrimSpace(string(line[:n])))
	if answer != "y" && answer != "yes" {
		return cascade.New(cascade.KindPermissionDenied, "backup: operation was not confirmed")
	}
	return nil
}

func backupTrustStore(deps elevateHelperDeps) (rpc.TrustStore, error) {
	if deps.TrustBackend == nil {
		return nil, cascade.New(cascade.KindUnavailable, "backup: no elevation trust store is configured")
	}
	store := elevation.NewElevationTrustStore(deps.TrustBackend(), deps.Clock)
	encoded, err := store.GetPubKey()
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindIntegrity, "backup: enrolled elevation public key is invalid")
	}
	fingerprint, err := elevation.Fingerprint(encoded)
	if err != nil {
		return nil, err
	}
	return rpc.MapTrustStore{fingerprint: ed25519.PublicKey(key)}, nil
}

func attestBackupOperation(ctx context.Context, deps elevateHelperDeps, ks elevation.ElevationKeystore, trust rpc.TrustStore, method string, params json.RawMessage) (backup.ElevationProof, error) {
	ledger := rpc.NewNonceLedger(deps.Clock)
	next := func(_ context.Context, _ json.RawMessage) (any, error) {
		return backup.ElevationProof("verified"), nil
	}
	gate := rpc.ElevationMiddleware(ledger, trust, deps.Clock)(method, next)
	nonce, err := issueBackupChallenge(ctx, gate, params)
	if err != nil {
		return "", err
	}
	att, err := signBackupAttestation(deps, ks, params, nonce)
	if err != nil {
		return "", err
	}
	envelope, err := json.Marshal(backupElevatedEnvelope{Attestation: att, Args: params})
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "backup: encode elevated request")
	}
	result, err := gate(ctx, envelope)
	if err != nil {
		return "", err
	}
	proof, ok := result.(backup.ElevationProof)
	if !ok || proof == "" {
		return "", cascade.New(cascade.KindInternal, "backup: elevation middleware returned no proof")
	}
	return proof, nil
}

func issueBackupChallenge(ctx context.Context, gate rpc.HandlerFunc, params json.RawMessage) (string, error) {
	_, err := gate(ctx, params)
	rpcErr, ok := err.(*rpc.ErrorObject)
	if !ok || rpcErr.Code != cascade.RPCCodeElevationRequired {
		if err == nil {
			return "", cascade.New(cascade.KindInternal, "backup: elevated method did not issue a challenge")
		}
		return "", err
	}
	data, merr := json.Marshal(rpcErr.Data)
	if merr != nil {
		return "", cascade.Wrap(cascade.KindInternal, merr, "backup: encode elevation challenge")
	}
	var challenge backupElevationChallenge
	if merr := json.Unmarshal(data, &challenge); merr != nil || challenge.Nonce == "" {
		return "", cascade.New(cascade.KindIntegrity, "backup: elevation challenge has no nonce")
	}
	return challenge.Nonce, nil
}

func signBackupAttestation(deps elevateHelperDeps, ks elevation.ElevationKeystore, params json.RawMessage, nonce string) (rpc.Attestation, error) {
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
		return rpc.Attestation{}, cascade.Wrap(cascade.KindInternal, err, "backup: mint elevation request id")
	}
	now := deps.Clock.Now()
	att := rpc.Attestation{
		RequestID: string(requestID), ActionHash: (&rpc.Request{Params: params}).ParamsHash(), Nonce: nonce,
		PubkeyFingerprint: fingerprint, IssuedUnix: now.Unix(), ExpUnix: now.Add(attestationTTL).Unix(),
	}
	sig, err := ks.Sign(canonicalSignedBytes(att))
	if err != nil {
		return rpc.Attestation{}, err
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	return att, nil
}
