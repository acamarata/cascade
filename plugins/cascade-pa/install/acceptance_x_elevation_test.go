package install_test

// Purpose (this file): a test-local install.Elevator for the Epic X
//   acceptance story that drives 06-FORGE-SPEC.md Sec5.14's real local
//   elevation flow -- rpc.NonceLedger issues the challenge,
//   rpc.ElevationMiddleware gates it, a real internal/elevation.
//   ElevationKeystore (elevation.NewFileKeystore, NOT a test fake: real
//   Ed25519 key generation and real file-backed storage, the same
//   production fallback tier D/S-07.T6 ships when no hardware keystore is
//   available) signs, and rpc.VerifyAttestation checks the result --
//   composed the same way internal/plugins/cascadepa_install_elevator.go
//   composes it, since that type is unexported and this _test.go file
//   (a different package) cannot reach it (see
//   acceptance_x_installer_test.go's header for why the depguard
//   exemption does not also lift Go visibility -- and why this file is
//   "package install_test", not "package install": internal/elevation and
//   internal/rpc are fine to import from either, but sitting in the same
//   external package as acceptance_x_installer_test.go, which MUST be
//   install_test to avoid the internal/plugins import cycle, keeps every
//   acceptance file's package declaration consistent). The small
//   wire-shape duplication below (the elevated envelope, signed-fields
//   encoding) mirrors cascadepa_install_elevator_verify.go's own
//   documented necessity: those shapes are unexported in internal/rpc
//   too.
// Inputs: an install.ElevationRequest naming the candidate/runtime.
// Outputs: install.ElevationResult{Approved:true} only after a genuine
//   nonce-issue -> local-sign -> attestation-verify sequence; every other
//   path is a typed, fail-closed error -- exactly installElevator's own
//   contract.
// Constraints: fail-closed on every branch; the ONLY witness-minting call
//   is install.VerifyElevationWitness (elevation.go), never a composite
//   literal.
// SPORT: plugins/cascade-pa/install:acceptance (ADD) -- P1-E24-W5-S50-T7.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

const (
	acceptElevationMethod = "plugin.add"
	acceptAttestationTTL  = 5 * time.Minute
)

// acceptElevator is this file's install.Elevator.
type acceptElevator struct {
	clock  runtime.Clock
	ledger *rpc.NonceLedger
	ks     elevation.ElevationKeystore
}

// newAcceptElevator generates a real Ed25519 key in a file-backed
// keystore under t.TempDir(), enrolls its public half in a real
// elevation.ElevationTrustStore (mirroring `cascade elevate-helper
// --enroll`'s real effect), and returns an Elevator ready to satisfy an
// elevated install.
func newAcceptElevator(t *testing.T) *acceptElevator {
	t.Helper()
	dir := t.TempDir()
	clock := testkit.NewFrozenClock(fixedAcceptTestTime)
	ks := elevation.NewFileKeystore(dir)
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("newAcceptElevator: GenerateKey: %v", err)
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		t.Fatalf("newAcceptElevator: PubKeyB64: %v", err)
	}
	trustStore := elevation.NewElevationTrustStore(elevation.NewFileBackend(dir), clock)
	if _, err := trustStore.Enroll(pub); err != nil {
		t.Fatalf("newAcceptElevator: Enroll: %v", err)
	}
	return &acceptElevator{clock: clock, ledger: rpc.NewNonceLedger(clock), ks: ks}
}

// Elevate implements install.Elevator.
func (e *acceptElevator) Elevate(ctx context.Context, req install.ElevationRequest) (install.ElevationResult, error) {
	pubB64, err := e.ks.PubKeyB64()
	if err != nil {
		return install.ElevationResult{}, err
	}
	fingerprint, err := elevation.Fingerprint(pubB64)
	if err != nil {
		return install.ElevationResult{}, err
	}
	pubBytes, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return install.ElevationResult{}, cascade.New(cascade.KindIntegrity, "acceptance elevator: enrolled public key is invalid")
	}
	trust := rpc.MapTrustStore{fingerprint: ed25519.PublicKey(pubBytes)}

	args := acceptElevationArgs(req)
	next := func(context.Context, json.RawMessage) (any, error) { return "approved", nil }
	gate := rpc.ElevationMiddleware(e.ledger, trust, e.clock)(acceptElevationMethod, next)

	nonce, err := acceptIssueChallenge(ctx, gate, args)
	if err != nil {
		return install.ElevationResult{}, err
	}
	att, err := e.signAttestation(fingerprint, nonce, args)
	if err != nil {
		return install.ElevationResult{}, err
	}
	witness, err := install.VerifyElevationWitness(ctx, acceptAttestationVerifier{gate: gate, args: args}, att)
	if err != nil {
		return install.ElevationResult{}, err
	}
	return install.ElevationResult{Approved: true, Witness: witness}, nil
}

// signAttestation mints and signs one Attestation over args, the same
// shape cascadepa_install_elevator.go's signInstallAttestation builds.
func (e *acceptElevator) signAttestation(fingerprint, nonce string, args json.RawMessage) (install.Attestation, error) {
	requestID, err := cascade.NewID()
	if err != nil {
		return install.Attestation{}, cascade.Wrap(cascade.KindInternal, err, "acceptance elevator: mint request id")
	}
	now := e.clock.Now()
	att := rpc.Attestation{
		RequestID: string(requestID), ActionHash: (&rpc.Request{Params: args}).ParamsHash(),
		Nonce: nonce, PubkeyFingerprint: fingerprint,
		IssuedUnix: now.Unix(), ExpUnix: now.Add(acceptAttestationTTL).Unix(),
	}
	sig, err := e.ks.Sign(acceptSignedFields(att))
	if err != nil {
		return install.Attestation{}, err
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	return install.Attestation{
		RequestID: att.RequestID, ActionHash: att.ActionHash, Nonce: att.Nonce,
		PubkeyFingerprint: att.PubkeyFingerprint, IssuedUnix: att.IssuedUnix,
		ExpUnix: att.ExpUnix, SigB64: att.SigB64,
	}, nil
}

// acceptElevationArgs mirrors installElevationArgs: process_tier always
// set, since every candidate this suite elevates is process-tier.
func acceptElevationArgs(req install.ElevationRequest) json.RawMessage {
	b, _ := json.Marshal(struct {
		PluginID    string `json:"plugin_id"`
		Runtime     string `json:"runtime"`
		ProcessTier bool   `json:"process_tier"`
	}{PluginID: req.PluginID, Runtime: string(req.Runtime), ProcessTier: true})
	return b
}

// acceptIssueChallenge mirrors issueInstallChallenge: one gate call with
// no attestation, expecting the ELEVATION_REQUIRED nonce.
func acceptIssueChallenge(ctx context.Context, gate rpc.HandlerFunc, args json.RawMessage) (string, error) {
	_, err := gate(ctx, args)
	rpcErr, ok := err.(*rpc.ErrorObject)
	if !ok || rpcErr.Code != cascade.RPCCodeElevationRequired {
		if err == nil {
			return "", cascade.New(cascade.KindInternal, "acceptance elevator: elevated method did not issue a challenge")
		}
		return "", err
	}
	data, merr := json.Marshal(rpcErr.Data)
	if merr != nil {
		return "", cascade.Wrap(cascade.KindInternal, merr, "acceptance elevator: encode elevation challenge")
	}
	var challenge struct {
		Nonce string `json:"nonce"`
	}
	if merr := json.Unmarshal(data, &challenge); merr != nil {
		return "", cascade.New(cascade.KindIntegrity, "acceptance elevator: elevation challenge has no nonce")
	}
	if challenge.Nonce == "" {
		// On Windows, platformElevationRefusal (internal/rpc/elevation_windows.go)
		// ALWAYS answers with a nonce-less ELEVATION_REQUIRED by design (ci-fix12);
		// only a non-Windows empty nonce is a genuine upstream bug.
		if goruntime.GOOS == "windows" {
			return "", elevation.ErrWindowsTier2()
		}
		return "", cascade.New(cascade.KindIntegrity, "acceptance elevator: elevation challenge has no nonce")
	}
	return challenge.Nonce, nil
}

// acceptAttestationVerifier adapts one rpc.ElevationMiddleware gate call
// to install.AttestationVerifier, mirroring
// cascadepa_install_elevator_verify.go's elevationAttestationVerifier:
// Verify's own envelope-encode-then-gate call IS the real verification
// (nonce/trust/replay via rpc.VerifyAttestation under the hood), not a
// second, independent recheck of the same attestation.
type acceptAttestationVerifier struct {
	gate rpc.HandlerFunc
	args json.RawMessage
}

// acceptElevatedEnvelope duplicates internal/rpc's own unexported
// elevatedEnvelope wire shape ({_attestation,_args}), matching
// cascadepa_install_elevator_verify.go's installElevatedEnvelope
// (unexported in a different package, so it cannot be imported).
type acceptElevatedEnvelope struct {
	Attestation rpc.Attestation `json:"_attestation"`
	Args        json.RawMessage `json:"_args"`
}

// Verify implements install.AttestationVerifier.
func (v acceptAttestationVerifier) Verify(ctx context.Context, att install.Attestation) error {
	rpcAtt := rpc.Attestation{
		RequestID: att.RequestID, ActionHash: att.ActionHash, Nonce: att.Nonce,
		PubkeyFingerprint: att.PubkeyFingerprint, IssuedUnix: att.IssuedUnix,
		ExpUnix: att.ExpUnix, SigB64: att.SigB64,
	}
	envelope, err := json.Marshal(acceptElevatedEnvelope{Attestation: rpcAtt, Args: v.args})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "acceptance elevator: encode elevated request")
	}
	_, err = v.gate(ctx, envelope)
	return err
}

// acceptSignedFields duplicates internal/rpc/elevation_attest.go's own
// unexported signedFields byte format field-for-field (alphabetical by
// JSON tag) -- rpc.VerifyAttestation recomputes this internally, so the
// two must produce byte-identical output for ed25519.Verify to succeed.
func acceptSignedFields(a rpc.Attestation) []byte {
	type signable struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}
	b, err := json.Marshal(signable{
		ActionHash: a.ActionHash, ExpUnix: a.ExpUnix, IssuedUnix: a.IssuedUnix,
		Nonce: a.Nonce, PubkeyFingerprint: a.PubkeyFingerprint, RequestID: a.RequestID,
	})
	if err != nil {
		return nil
	}
	return b
}
