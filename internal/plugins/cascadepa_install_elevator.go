package plugins

// Purpose (this file): the real install.Elevator -- 06-FORGE-SPEC.md
//   §5.14's local elevation flow (nonce -> user-session helper local-auth
//   -> hardware-backed signed attestation), the SAME primitives
//   cmd/cascade/backup_elevation.go's attestBackupOperation already uses
//   for backup.create's own elevated verb: rpc.NonceLedger issues the
//   challenge, rpc.ElevationMiddleware gates it, an
//   internal/elevation.ElevationKeystore performs local authentication
//   (Keychain/PAM/Windows Hello) and signs ATOMICALLY in Sign's one call
//   (that call IS the "user-session helper local-auth" step -- no
//   subprocess is spawned; Sign is the same primitive `cascade
//   elevate-helper --sign` itself calls), and rpc.VerifyAttestation checks
//   the result against the enrolled ElevationTrustStore record.
//
// DISCLOSED GAP (recorded, not papered over): no production call site in
//   this tree registers rpc.ElevationMiddleware on the daemon's live
//   *rpc.Registry (verified empirically: grepping the whole module for
//   ".Use(" against any *rpc.Registry returns zero production hits --
//   every doc comment elsewhere in this tree claiming "elevation is
//   enforced by internal/rpc's shared ElevationMiddleware" -- e.g.
//   internal/daemon/sync_rpc.go:20, internal/daemon/node_upgrade_rpc.go:48 --
//   describes a wiring that does not exist in the tree today). That gap
//   is cmd/cascade's (buildRPCServer never calls registry.Use), out of
//   this ticket's files_scope, and this file cannot close it. What this
//   file DOES guarantee, on its own, independent of that gap: Flow
//   (install/flow.go) never sets RunResult.Resumed=true for an elevated
//   install unless THIS Elevate call independently ran the real
//   nonce-issue/local-auth-sign/attestation-verify sequence to a genuine
//   Approved:true -- the chat confirm alone can never satisfy it, exactly
//   as the ticket requires, regardless of whether the daemon's own
//   "plugin.add" RPC handler separately re-checks it.
//
// Inputs: an install.ElevationRequest naming the candidate and runtime.
// Outputs: install.ElevationResult{Approved:true} only after a verified
//   attestation; every other path is a typed, fail-closed error.
// Constraints: CASCADE_NO_INPUT=1 refuses before touching the keystore at
//   all (matching elevate_helper.go's runElevateHelperSign guard); a
//   Windows tier-2 keystore refuses by name (elevation.ErrWindowsTier2);
//   an unenrolled host refuses by name (ElevationTrustStore.GetPubKey's
//   own not-found error) -- never a fabricated Approved:true.
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	goruntime "runtime"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// installElevationMethod is the elevationTable verb this flow attests
// against (internal/rpc/elevation.go's "plugin.add" entry). It is a
// conditional rule keyed on process_tier/grant_expand presence
// (hasJSONField): installElevationArgs always sets process_tier so a
// process-runtime or grant-expanding candidate -- the only case Flow ever
// calls Elevate for (flow.go's install method) -- is always classified
// elevated, never silently waved through.
const installElevationMethod = "plugin.add"

// installAttestationTTL matches elevate_helper.go's own 5-minute contract
// value (attestationTTL) -- duplicated, not imported: that constant is
// unexported in a different package (cmd/cascade).
const installAttestationTTL = 5 * time.Minute

// installElevator implements install.Elevator over the real attestation
// flow. keystore/trustBackend, when non-nil, replace the real
// elevation.SelectKeystore/elevation.NewFileBackend constructions --
// always nil in production; a same-package test sets them, matching
// rpcDoer's documented convention in cascadepa_wiring.go.
type installElevator struct {
	resolvePaths pathResolver
	clock        runtime.Clock
	getenv       func(string) string
	ledger       *rpc.NonceLedger

	keystore     func(dataDir string) elevation.ElevationKeystore
	trustBackend func(dataDir string) elevation.Backend
}

// newInstallElevator builds an installElevator over its collaborators.
func newInstallElevator(resolvePaths pathResolver, clock runtime.Clock, getenv func(string) string) *installElevator {
	return &installElevator{resolvePaths: resolvePaths, clock: clock, getenv: getenv, ledger: rpc.NewNonceLedger(clock)}
}

// Elevate implements install.Elevator.
func (e *installElevator) Elevate(ctx context.Context, req install.ElevationRequest) (install.ElevationResult, error) {
	if e.getenv != nil && e.getenv("CASCADE_NO_INPUT") == "1" {
		return install.ElevationResult{}, elevation.ErrNoInput()
	}
	paths, err := e.resolvePaths()
	if err != nil {
		return install.ElevationResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa install: resolve daemon paths")
	}
	dataDir := paths.DataDir()

	ks := e.buildKeystore(dataDir)
	if ks.Tier() == elevation.TierWindowsTier2 {
		return install.ElevationResult{}, elevation.ErrWindowsTier2()
	}

	trustStore := elevation.NewElevationTrustStore(e.buildTrustBackend(dataDir), e.clock)
	pubB64, err := trustStore.GetPubKey()
	if err != nil {
		return install.ElevationResult{}, err
	}
	fingerprint, err := elevation.Fingerprint(pubB64)
	if err != nil {
		return install.ElevationResult{}, err
	}
	pubBytes, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return install.ElevationResult{}, cascade.New(cascade.KindIntegrity, "cascade-pa install: enrolled elevation public key is invalid")
	}
	trust := rpc.MapTrustStore{fingerprint: ed25519.PublicKey(pubBytes)}

	args := installElevationArgs(req)
	next := func(context.Context, json.RawMessage) (any, error) { return "approved", nil }
	gate := rpc.ElevationMiddleware(e.ledger, trust, e.clock)(installElevationMethod, next)

	nonce, err := issueInstallChallenge(ctx, gate, args)
	if err != nil {
		return install.ElevationResult{}, err
	}
	att, err := e.signInstallAttestation(ks, args, fingerprint, nonce)
	if err != nil {
		return install.ElevationResult{}, err
	}
	// round-2 rework (T0 decision D2): the witness is minted by
	// install.VerifyElevationWitness, whose ONE verification call is
	// verifier.Verify below -- elevationAttestationVerifier.Verify's own
	// body is the SAME gate(ctx, envelope) call this file used to make
	// directly, so this is still exactly one real
	// rpc.ElevationMiddleware/rpc.VerifyAttestation invocation, not a
	// second, independent recheck of the same attestation. A caller that
	// merely asserts "elevated" can never produce a valid witness: only a
	// verified attestation can.
	witness, err := install.VerifyElevationWitness(ctx,
		elevationAttestationVerifier{gate: gate, args: args}, installAttestationFromRPC(att))
	if err != nil {
		return install.ElevationResult{}, err
	}
	return install.ElevationResult{Approved: true, Witness: witness}, nil
}

func (e *installElevator) buildKeystore(dataDir string) elevation.ElevationKeystore {
	if e.keystore != nil {
		return e.keystore(dataDir)
	}
	return elevation.SelectKeystore(dataDir)
}

func (e *installElevator) buildTrustBackend(dataDir string) elevation.Backend {
	if e.trustBackend != nil {
		return e.trustBackend(dataDir)
	}
	return elevation.NewFileBackend(dataDir)
}

// signInstallAttestation mints and signs one Attestation over args. ks.Sign
// is where local authentication actually happens (ElevationKeystore's own
// contract: "performs local authentication and signing ATOMICALLY").
func (e *installElevator) signInstallAttestation(ks elevation.ElevationKeystore, args json.RawMessage,
	fingerprint, nonce string) (rpc.Attestation, error) {
	requestID, err := cascade.NewID()
	if err != nil {
		return rpc.Attestation{}, cascade.Wrap(cascade.KindInternal, err, "cascade-pa install: mint elevation request id")
	}
	now := e.clock.Now()
	att := rpc.Attestation{
		RequestID:         string(requestID),
		ActionHash:        (&rpc.Request{Params: args}).ParamsHash(),
		Nonce:             nonce,
		PubkeyFingerprint: fingerprint,
		IssuedUnix:        now.Unix(),
		ExpUnix:           now.Add(installAttestationTTL).Unix(),
	}
	sig, err := ks.Sign(installSignedFields(att))
	if err != nil {
		return rpc.Attestation{}, err
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	return att, nil
}

// installElevationArgs is the elevated verb's params -- process_tier
// always set (this file's own const doc comment explains why: Flow only
// ever calls Elevate for a candidate the real add path already classified
// AddOutcomeElevationRequired, so tagging it as elevated here is never a
// misclassification).
func installElevationArgs(req install.ElevationRequest) json.RawMessage {
	b, _ := json.Marshal(struct {
		PluginID    string `json:"plugin_id"`
		Runtime     string `json:"runtime"`
		ProcessTier bool   `json:"process_tier"`
	}{PluginID: req.PluginID, Runtime: string(req.Runtime), ProcessTier: true})
	return b
}

// issueInstallChallenge calls gate once with no attestation to obtain the
// ELEVATION_REQUIRED nonce, mirroring backup_elevation.go's
// issueBackupChallenge exactly (that function is unexported in a
// different package, cmd/cascade, hence duplicated here) -- including its
// Windows tier-2 disambiguation, fixed here for the identical reason it was
// fixed there (P1-E19-W4-S42-T3): on Windows, platformElevationRefusal
// (internal/rpc/elevation_windows.go) ALWAYS returns ELEVATION_REQUIRED
// with no nonce, by design -- elevation is unsupported on Windows
// (tier-2), and internal/rpc/elevation_flow_windows_test.go asserts this
// exact nonce-less shape is the canonical Windows behavior, preempting the
// real attestation flow before it ever runs. Reporting that as a
// cascade.KindIntegrity violation mislabels correct, by-design behavior;
// only a non-Windows empty nonce is a genuine upstream bug.
func issueInstallChallenge(ctx context.Context, gate rpc.HandlerFunc, args json.RawMessage) (string, error) {
	_, err := gate(ctx, args)
	rpcErr, ok := err.(*rpc.ErrorObject)
	if !ok || rpcErr.Code != cascade.RPCCodeElevationRequired {
		if err == nil {
			return "", cascade.New(cascade.KindInternal, "cascade-pa install: elevated method did not issue a challenge")
		}
		return "", err
	}
	data, merr := json.Marshal(rpcErr.Data)
	if merr != nil {
		return "", cascade.Wrap(cascade.KindInternal, merr, "cascade-pa install: encode elevation challenge")
	}
	var challenge struct {
		Nonce string `json:"nonce"`
	}
	if merr := json.Unmarshal(data, &challenge); merr != nil {
		return "", cascade.New(cascade.KindIntegrity, "cascade-pa install: elevation challenge has no nonce")
	}
	if challenge.Nonce == "" {
		if goruntime.GOOS == "windows" {
			return "", elevation.ErrWindowsTier2()
		}
		return "", cascade.New(cascade.KindIntegrity, "cascade-pa install: elevation challenge has no nonce")
	}
	return challenge.Nonce, nil
}

// installElevatedEnvelope duplicates internal/rpc's own unexported
// elevatedEnvelope wire shape ({_attestation,_args}) -- the same
// duplication backup_elevation.go's backupElevatedEnvelope already
// performs for the identical reason (elevatedEnvelope is unexported in
// package rpc).
type installElevatedEnvelope struct {
	Attestation rpc.Attestation `json:"_attestation"`
	Args        json.RawMessage `json:"_args"`
}

// installSignedFields duplicates internal/rpc/elevation_attest.go's own
// unexported signedFields byte format field-for-field (alphabetical by
// JSON tag, independent of struct declaration order) -- VerifyAttestation
// recomputes this internally from the Attestation this file sends over
// gate(ctx, envelope), so the two must produce byte-identical output for
// ed25519.Verify to ever succeed. Not imported: signedFields is
// unexported in a different package.
func installSignedFields(a rpc.Attestation) []byte {
	type signable struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}
	b, err := json.Marshal(signable{
		ActionHash:        a.ActionHash,
		ExpUnix:           a.ExpUnix,
		IssuedUnix:        a.IssuedUnix,
		Nonce:             a.Nonce,
		PubkeyFingerprint: a.PubkeyFingerprint,
		RequestID:         a.RequestID,
	})
	if err != nil {
		return nil
	}
	return b
}

// compile-time proof installElevator really is the Elevator install.Flow
// reads, so a signature drift on either side fails here rather than at the
// wiring site.
var _ install.Elevator = (*installElevator)(nil)
