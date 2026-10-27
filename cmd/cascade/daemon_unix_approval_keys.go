//go:build !windows

// Purpose: the daemon's approval signing key: provisioned once in the vault
//
//	(create-only, reserved name), turned into the signing minter the queue
//	mints every approval with and the verifier approval.grant checks, and
//	reported as the "approval-keys" subsystem. Also the doctor's read-only
//	presence check and the late binding of the elevation witness verifier.
//
// Inputs: the daemon's path provider (its vault broker), the clock, the
//
//	elevation status the middleware wiring produces.
//
// Outputs: signerMinter, approvalKeys, provisionApprovalKeys,
//
//	bindPolicyElevation, approvalKeyStatus.
//
// Constraints: FAIL CLOSED. A key that cannot be provisioned or read leaves
//
//	no signing minter and no verifier: the queue mints nothing (every ask
//	downgrades to deny), approval.grant refuses, and status.get shows
//	"approval-keys" failed with the reason. The crypto/rand minter is never
//	a fallback.
//
// SPORT: cmd/cascade approval-keys wiring/ADDED (P1-SEC-23).

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// approvalKeysSubsystem is the status.get line for the signing key.
const approvalKeysSubsystem = "approval-keys"

// signerMinter mints each approval as a signed record and hands the queue
// the request id and nonce the record itself carries.
type signerMinter struct {
	signer   *policy.ApprovalSigner
	verifier *policy.ApprovalVerifier
}

var _ policy.TokenMinter = signerMinter{}

// Mint implements policy.TokenMinter.
func (m signerMinter) Mint(ctx context.Context, req policy.ApprovalMintRequest) (policy.ApprovalToken, error) {
	signed, err := m.signer.Sign(ctx, policy.ApprovalRecord{
		Verb: req.Verb, ParamsDigest: req.ParamsHash, Requester: req.Subject.String(),
		Origin: "approval-queue", Audience: "cascade-daemon", Risk: req.Level, Sensitivity: "internal",
	})
	if err != nil {
		return policy.ApprovalToken{}, err
	}
	// Verify what was just signed: a key whose halves disagree, or a clock
	// that already lapsed the record, refuses here rather than at grant.
	rec, err := m.verifier.Verify(signed)
	if err != nil {
		return policy.ApprovalToken{}, err
	}
	return policy.ApprovalToken{
		RequestID: rec.RequestID.String(), Nonce: rec.Nonce.String(),
		ActionHash: req.ActionHash, ParamsHash: req.ParamsHash,
		Issued: rec.Issued, Expires: rec.Expiry, Signed: signed,
	}, nil
}

// unavailableApprovalMinter is the minter of a daemon whose key failed: it
// refuses, so no unsigned approval is ever queued.
type unavailableApprovalMinter struct{ reason string }

// Mint implements policy.TokenMinter by refusing.
func (m unavailableApprovalMinter) Mint(context.Context, policy.ApprovalMintRequest) (policy.ApprovalToken, error) {
	return policy.ApprovalToken{}, cascade.Newf(cascade.KindUnavailable,
		"approval signing key unavailable: %s", m.reason)
}

// approvalKeys is the outcome of provisioning: a ready signer/verifier pair,
// or the reason there is none.
type approvalKeys struct {
	minter   policy.TokenMinter
	verifier *policy.ApprovalVerifier
	keyID    string
	reason   string
}

// provisionApprovalKeys provisions the approval key in the daemon vault and
// builds the signer and verifier over it. It never returns an error: a
// failure is carried as the reason, with a refusing minter and no verifier.
func provisionApprovalKeys(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) approvalKeys {
	fail := func(err error) approvalKeys {
		return approvalKeys{minter: unavailableApprovalMinter{reason: err.Error()}, reason: err.Error()}
	}
	fw, err := processFirewallFor(paths)
	if err != nil {
		return fail(err)
	}
	lockPath, err := approvalKeyLockPath(paths.DataDir(), fw.Broker().Backend())
	if err != nil {
		return fail(err)
	}
	keyID, pub, _, err := secrets.ProvisionDaemonKey(ctx, fw.Broker().WithDaemonKeyLock(lockPath), secrets.ApprovalKeyName, rand.Reader)
	if err != nil {
		return fail(err)
	}
	reader, err := secrets.NewApprovalKeyReader(fw.Broker())
	if err != nil {
		return fail(err)
	}
	signer, err := policy.NewApprovalSigner(reader, clock)
	if err != nil {
		return fail(err)
	}
	verifier, err := policy.NewApprovalVerifier(ed25519.PublicKey(pub), clock)
	if err != nil {
		return fail(err)
	}
	return approvalKeys{minter: signerMinter{signer: signer, verifier: verifier}, verifier: verifier, keyID: keyID}
}

// fileVaultBackend is the file vault's Backend() name.
const fileVaultBackend = "file-vault"

// userLockRoot is where the per-login-user lock directory lives. A var only
// so tests can point it at a temp dir; production is the system /tmp.
var userLockRoot = "/tmp"

// approvalKeyLockPath scopes the provisioning lock to the custody the key
// lives in: the file vault is per data directory, so its lock is too; a
// platform keystore (keychain, secret service) belongs to the login user,
// so every process of that user takes one lock keyed by backend, service
// and key name, in a directory derived from the uid, never from HOME or
// XDG_* (two processes with different environments share one keystore).
func approvalKeyLockPath(dataDir, backend string) (string, error) {
	if backend == fileVaultBackend {
		return filepath.Join(dataDir, ".cascade-daemon-key.lock"), nil
	}
	base, err := userLockDir(userLockRoot, os.Getuid())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(backend + "\x00" + vaultService + "\x00" + secrets.ApprovalKeyName))
	return filepath.Join(base, "daemon-key-"+hex.EncodeToString(sum[:8])+".lock"), nil
}

// userLockDir returns root/cascade-<uid>, creating it 0700. It refuses (so
// provisioning refuses) a path that is not a real directory owned by uid
// with mode 0700: on a shared root another user could otherwise pre-create
// it.
func userLockDir(root string, uid int) (string, error) {
	dir := filepath.Join(root, fmt.Sprintf("cascade-%d", uid))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "approval key: creating the per-user lock directory")
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "approval key: reading the per-user lock directory")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().Perm() != 0o700 || !ok || int(st.Uid) != uid {
		return "", cascade.Newf(cascade.KindPermissionDenied,
			"approval key: %s is not a 0700 directory owned by this user; refusing to provision", dir)
	}
	return dir, nil
}

// openGrantEffects is the effect log approval.grant claims redemptions in.
// A crash between redeem and confirm left an open intent: it is closed as
// unknown-outcome here, so that request never redeems again. A recovery
// failure (a tampered index) needs no handling: AuditVerify refuses every
// grant while the history does not verify.
func openGrantEffects(ctx context.Context, log *audit.Log) (*audit.EffectLog, error) {
	effects, err := audit.NewEffectLog(log)
	if err != nil {
		return nil, err
	}
	_, _ = policy.RecoverGrantEffects(ctx, effects)
	return effects, nil
}

// bindPolicyElevation gives the policy handlers the attestor over the
// daemon's own witness issuer (P1-BF-R265) and reports the approval key.
// It runs from the elevation wiring, before the handlers are registered.
func bindPolicyElevation(w *daemonWiring, status daemon.ElevationStatus) {
	pol := w.Deps.Policy
	if pol == nil {
		return
	}
	pol.rpcDeps.Attestor = policy.WitnessAttestor(status.Verifier)
	pol.Handlers = policy.MethodHandlers(pol.rpcDeps)
	if pol.approvalKeys.reason != "" {
		w.Manifest.Failed(approvalKeysSubsystem, pol.approvalKeys.reason)
		return
	}
	w.Manifest.Started(approvalKeysSubsystem, "signing key "+pol.approvalKeys.keyID)
}

// approvalKeyStatus is the doctor's read-only presence check: it opens the
// daemon vault without writing a probe or a key file and reports whether a
// well-formed approval key is stored.
// approvalKeyStatusCustody selects the read-only custody; tests force the
// file vault so no host keychain is consulted.
var approvalKeyStatusCustody = secrets.SelectCustodyReadOnly

func approvalKeyStatus(ctx context.Context, paths runtime.PathProvider) (present bool, reason string) {
	if paths == nil || paths.DataDir() == "" {
		return false, "the cascade data directory could not be resolved"
	}
	custody, err := approvalKeyStatusCustody(hostCustodyConfig(paths.DataDir()))
	if err != nil {
		return false, err.Error()
	}
	value, err := custody.Get(ctx, secrets.ApprovalKeyName)
	if err != nil {
		return false, err.Error()
	}
	defer clear(value)
	if len(value) != ed25519.PrivateKeySize {
		return false, "the stored approval key is not an ed25519 private key"
	}
	// The daemon refuses a key whose public half does not match its seed.
	derived := ed25519.NewKeyFromSeed(value[:ed25519.SeedSize])
	defer clear(derived)
	if !bytes.Equal(derived, value) {
		return false, "the stored approval key does not hold a consistent ed25519 key pair"
	}
	return true, ""
}
