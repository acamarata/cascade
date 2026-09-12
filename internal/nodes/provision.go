// Purpose: enroll-time and rollout binary provisioning (§D-17): ship a
//   minisign-verified release artifact to an enrolled node over ssh, or
//   verify the node's preinstalled stamp and skip — install-class
//   idempotent (06 §5.9: a second run is verify-only convergence, exit 0,
//   delta reported).
// Inputs: a Target (ssh reachability, caller-supplied — see tunnel.go's
//   identical CONTRADICTION note: DeviceRecord has no Host/User field of
//   its own; travel.go's Route carries the same {User, Addr} shape a
//   caller derives a Target from), an Artifact (the release binary bytes
//   plus its detached minisign signature), and ProvisionDeps.
// Outputs: a ProvisionResult reporting skip-vs-install and the version
//   delta, or a typed fail-closed refusal.
// Constraints: THE ARTIFACT'S MINISIGN SIGNATURE IS VERIFIED BEFORE ANY
//   DIAL OR TRANSFER (§D-32 fail-closed: an invalid artifact is never
//   even offered to a node). The post-transfer checksum re-check compares
//   the RECEIVING end's actual bytes against a digest computed locally
//   over the already-verified artifact BEFORE transfer — never against a
//   hash the transfer itself produced, closing the "verify an artifact
//   against a hash it supplied" gap. Install is an atomic rename
//   (`mv -f`): a crash or refusal at any point before that rename leaves
//   the PREVIOUS binary running untouched — the rollback proof this
//   ticket's safety requirement demands is structural, not a separate
//   recovery step. EXECSESSION, NOT TUNNEL.SESSION (CONTRADICTION):
//   tunnel.go's Session interface exposes only ListenUnix (the heartbeat
//   remote-listen direction) and is out of this ticket's files_scope to
//   extend; ExecSession/ExecDialer below are this ticket's own, narrower
//   ssh capability (run a command, write a file), independent of
//   tunnel.go's Manager/reconnect state machine.
// SPORT: internal/nodes Provision/ADDED, ExecDialer/ADDED
//   (P1-E17-W4-S36-T5).

package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Artifact is the release binary this ticket ships to a node.
type Artifact struct {
	// Version is the §D-33 artifact version (buildinfo.Version-shaped).
	Version string
	// Data is the binary's bytes.
	Data []byte
	// Signature is the detached minisign .minisig file's raw bytes,
	// produced by the §D-16 release train over Data.
	Signature []byte
}

// ExecSession is the narrow remote-command capability provisioning
// needs. See this file's package doc for why it is not tunnel.go's
// Session.
type ExecSession interface {
	// Output runs cmd on the node and returns its combined stdout+stderr.
	// A non-zero remote exit status is returned as an error.
	Output(ctx context.Context, cmd string) ([]byte, error)
	// WriteFile writes data to remotePath on the node, replacing any
	// existing content.
	WriteFile(ctx context.Context, remotePath string, data []byte) error
	Close() error
}

// ExecDialer establishes an ExecSession to target, verifying the
// presented host key via verify before the handshake completes — the
// same HostKeyVerifier contract tunnel.go's Dialer uses (R-21.220).
type ExecDialer interface {
	Dial(ctx context.Context, target Target, verify HostKeyVerifier) (ExecSession, error)
}

// ProvisionDeps carries Provision's collaborators.
type ProvisionDeps struct {
	Dialer ExecDialer
	// VerifyFor builds the HostKeyVerifier for one dial. A verifier is
	// PER-TARGET (it checks a specific pinned host's fingerprint, per
	// KnownHosts.Verify's own host-keyed contract), so a single fixed
	// HostKeyVerifier cannot be shared across a --all rollout's many
	// different node hosts; the caller supplies a factory instead.
	VerifyFor func(Target) HostKeyVerifier
	PublicKey MinisignPublicKey
	// InstallPath is the remote path the binary is installed to. Empty
	// resolves to defaultRemoteInstallPath.
	InstallPath string
	// ControllerVersion is this controller's own buildinfo.Version-shaped
	// stamp (§D-17). When non-empty and parseable, Provision refuses to
	// ship an artifact whose version falls outside ControllerVersion's
	// same-minor window (NegotiateVersion) — a controller must never push
	// a binary it could not itself negotiate with. Empty or unparseable
	// (e.g. "dev", an unstamped local build) skips this check entirely,
	// mirroring internal/daemon/upgrade_skew.go's identical "unstamped
	// build bypasses the check" precedent rather than hard-refusing a
	// state Provision cannot meaningfully judge.
	ControllerVersion string
}

// ProvisionResult reports Provision's outcome.
type ProvisionResult struct {
	NodeID          string
	Skipped         bool
	Installed       bool
	PreviousVersion string
	NewVersion      string
}

const defaultRemoteInstallPath = "/usr/local/bin/cascade"
const remoteTempSuffix = ".cascade-upload.tmp"

// VerifyArtifactSignature parses artifact.Signature and verifies it over
// artifact.Data against pub. This is the §D-32 gate: called before any
// dial, so an invalid artifact is never offered to a node at all.
func VerifyArtifactSignature(artifact Artifact, pub MinisignPublicKey) error {
	if len(artifact.Signature) == 0 {
		return ErrSignatureInvalid("artifact carries no signature")
	}
	sig, err := ParseMinisignSignature(artifact.Signature)
	if err != nil {
		return err
	}
	return VerifyMinisign(pub, artifact.Data, sig)
}

// Provision ships artifact to target, or verifies the node is already at
// artifact.Version and skips (idempotent convergence).
func Provision(ctx context.Context, target Target, artifact Artifact, deps ProvisionDeps) (ProvisionResult, error) {
	if err := VerifyArtifactSignature(artifact, deps.PublicKey); err != nil {
		return ProvisionResult{}, err
	}
	if deps.ControllerVersion != "" {
		if _, err := NegotiateVersion(deps.ControllerVersion, artifact.Version); err != nil {
			return ProvisionResult{}, err
		}
	}
	installPath := deps.InstallPath
	if installPath == "" {
		installPath = defaultRemoteInstallPath
	}

	sess, err := deps.Dialer.Dial(ctx, target, deps.VerifyFor(target))
	if err != nil {
		return ProvisionResult{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: dial")
	}
	defer func() { _ = sess.Close() }()

	installedVersion := queryInstalledVersion(ctx, sess, installPath)
	if installedVersion != "" && installedVersion == artifact.Version {
		return ProvisionResult{NodeID: target.NodeID, Skipped: true, PreviousVersion: installedVersion, NewVersion: installedVersion}, nil
	}

	tempPath := installPath + remoteTempSuffix
	if err := sess.WriteFile(ctx, tempPath, artifact.Data); err != nil {
		return ProvisionResult{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: transfer")
	}
	if err := verifyTransferredChecksum(ctx, sess, tempPath, artifact.Data); err != nil {
		_, _ = sess.Output(ctx, "rm -f "+shellQuote(tempPath))
		return ProvisionResult{}, err
	}
	installCmd := fmt.Sprintf("chmod +x %s && mv -f %s %s", shellQuote(tempPath), shellQuote(tempPath), shellQuote(installPath))
	if _, err := sess.Output(ctx, installCmd); err != nil {
		_, _ = sess.Output(ctx, "rm -f "+shellQuote(tempPath))
		return ProvisionResult{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: install")
	}
	return ProvisionResult{NodeID: target.NodeID, Installed: true, PreviousVersion: installedVersion, NewVersion: artifact.Version}, nil
}

// verifyTransferredChecksum compares a checksum computed locally over
// localData (BEFORE transfer, over the already minisign-verified bytes)
// against the receiving end's own checksum of what actually landed at
// remotePath. This never trusts a hash the transfer itself produced for
// BOTH sides of the comparison — the expected side is fixed before any
// bytes cross the wire.
func verifyTransferredChecksum(ctx context.Context, sess ExecSession, remotePath string, localData []byte) error {
	expected := sha256.Sum256(localData)
	out, err := sess.Output(ctx, "sha256sum "+shellQuote(remotePath))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: checksum transferred artifact")
	}
	if firstField(string(out)) != hex.EncodeToString(expected[:]) {
		return cascade.New(cascade.KindIntegrity,
			"nodes: provision: transferred artifact checksum mismatch (interrupted or corrupted transfer); previous binary left in place")
	}
	return nil
}

// queryInstalledVersion runs `<installPath> version` on the node and
// extracts the version token from the "cascade version X.Y.Z" first
// line (cmd/cascade/version.go's exact output shape). Any failure
// (binary absent, exec error, unparseable output) reports "" — treated
// by Provision as "not installed," never as an error, since a fresh node
// legitimately has no binary yet.
func queryInstalledVersion(ctx context.Context, sess ExecSession, installPath string) string {
	out, err := sess.Output(ctx, shellQuote(installPath)+" version")
	if err != nil {
		return ""
	}
	firstLine := strings.SplitN(string(out), "\n", 2)[0]
	const prefix = "cascade version "
	if !strings.HasPrefix(firstLine, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(firstLine, prefix))
}

// shellQuote wraps s in single quotes for safe interpolation into a
// remote shell command, escaping any embedded single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// firstField returns the first whitespace-delimited field of s (the
// checksum column of `sha256sum`'s output), or "" if s is empty.
func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
