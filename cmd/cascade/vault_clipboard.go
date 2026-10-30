// Purpose: the body of `cascade vault get NAME --clipboard`: read the
//
//	admitted [secrets].clipboard_ttl, pass the P1-SEC-08 proof, mint an
//	in-process ephemeral approval, and hand the value to the clipboard
//	writer, which blocks until the TTL clear is confirmed. SIGINT, SIGTERM
//	and SIGHUP clear at once.
//
// Inputs: vaultDeps (Gate, NewCustody, NewClipboard) and the profile paths.
// Outputs: runVaultGetClipboard, newHostClipboard, pendingClearsPath.
//
// Constraints: the TTL is read through runtime.SecretsClipboardTTL before
//
//	any audit, custody or prompt, so a bad TTL refuses first. The value is
//	never printed. The approval key is generated per call, zeroed at return
//	and never persisted, and the approval is minted only after the gate
//	authorised the read. The audit sink (clipboard_audit_sink.go) writes
//	through the daemon's log or, daemonless, the process log; this file adds
//	no audit.New.
//
// SPORT: cmd/cascade/vault (ADD - get --clipboard, P1-SEC-09).
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// clipboardHost is what one --clipboard call needs beyond vaultDeps: the
// clock, the TTL reader and the process audit sinks.
type clipboardHost struct {
	clock runtime.Clock
	ttl   func(ctx context.Context) (time.Duration, error)
	sinks func(ctx context.Context) (audit.Writer, audit.EffectWriter, func(), error)
}

// runVaultGetClipboard is the --clipboard branch of `vault get`.
func runVaultGetClipboard(cmd *cobra.Command, deps vaultDeps, name string) error {
	return vaultGetClipboard(cmd, deps, name, hostClipboard(deps.Paths))
}

func hostClipboard(paths runtime.PathProvider) clipboardHost {
	clock := runtime.NewSystemClock()
	return clipboardHost{
		clock: clock,
		ttl:   func(ctx context.Context) (time.Duration, error) { return profileClipboardTTL(ctx, paths) },
		sinks: func(ctx context.Context) (audit.Writer, audit.EffectWriter, func(), error) {
			return processClipboardSinks(ctx, paths, clock)
		},
	}
}

// profileClipboardTTL reads the admitted secrets.clipboard_ttl and checks
// its range. A config that cannot be read refuses rather than defaulting.
func profileClipboardTTL(ctx context.Context, paths runtime.PathProvider) (time.Duration, error) {
	cfg, err := loadPolicyConfig(ctx, paths)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInvalidInput, err, "vault get --clipboard: could not read [secrets].clipboard_ttl; nothing was read")
	}
	admitted, err := runtime.SecretsClipboardTTL(runtime.EffectiveConfigOf(cfg))
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInvalidInput, err, "vault get --clipboard: secrets.clipboard_ttl is not valid; nothing was read")
	}
	return secrets.ParseClipboardTTL(map[string]interface{}{"clipboard_ttl": admitted})
}

// processClipboardSinks returns the clipboard audit sink: through the
// daemon when one answers, else one store open per record. Nothing is held
// open, so the gate's own rows and other commands are never locked out.
func processClipboardSinks(_ context.Context, paths runtime.PathProvider, clock runtime.Clock) (audit.Writer, audit.EffectWriter, func(), error) {
	if paths == nil {
		return nil, nil, nil, cascade.New(cascade.KindUnavailable, "vault get --clipboard: no profile paths are configured")
	}
	sink := newClipboardAuditSink(paths, clock)
	return sink, sink, func() {}, nil
}

func vaultGetClipboard(cmd *cobra.Command, deps vaultDeps, name string, host clipboardHost) error {
	// SIGHUP too: closing the terminal mid-TTL must clear, not leave the
	// value for the next daemon start.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ttl, err := host.ttl(ctx)
	if err != nil {
		return err
	}
	if deps.NewClipboard == nil {
		return cascade.New(cascade.KindUnsupported, "vault get --clipboard: no clipboard writer is configured; nothing was read")
	}
	recorder, ok := deps.Gate.(vaultAccessRecorder)
	if !ok {
		return cascade.New(cascade.KindElevationRequired, "vault get: the elevation gate cannot audit an access; refusing to read")
	}
	aw, fx, closeSinks, err := host.sinks(ctx)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "vault get --clipboard: the audit log could not be opened; nothing was read")
	}
	defer closeSinks()
	if err := recorder.RecordVaultAccess(ctx, name, secrets.VerbGet); err != nil {
		if _, werr := fmt.Fprintln(cmd.ErrOrStderr(), "vault get: refused (audit_unavailable): the access could not be recorded"); werr != nil {
			return cascade.Wrap(cascade.KindUnavailable, werr, "vault: writing the refusal failed")
		}
		return err
	}
	broker, err := vaultBroker(deps)
	if err != nil {
		return err
	}
	value, err := broker.Get(ctx, name) // the gate's Authorize runs inside Get
	if err != nil {
		return err
	}
	defer clear(value)
	verifier, signed, err := mintClipboardApproval(ctx, host.clock, name)
	if err != nil {
		return err
	}
	cw, err := deps.NewClipboard(ttl, verifier, aw, fx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "vault get: copying %s to the clipboard; it is cleared after %s (Ctrl-C clears it now)\n", name, ttl); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "vault: writing the notice failed")
	}
	if err := cw.Write(ctx, signed, value); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "vault get: the clipboard was cleared\n")
	return nil
}

// ephemeralApprovalKey hands the per-call key to the signer as a copy the
// signer zeroes; the caller zeroes the original.
type ephemeralApprovalKey struct{ private ed25519.PrivateKey }

func (k ephemeralApprovalKey) ApprovalKey(context.Context) (string, ed25519.PrivateKey, error) {
	return "ephemeral", bytes.Clone(k.private), nil
}

// mintClipboardApproval signs one vault.get.clipboard record over sha256 of
// name under a fresh Ed25519 key and returns the verifier for that key
// alone. Called only after the gate granted the read.
func mintClipboardApproval(ctx context.Context, clock runtime.Clock, name string) (secrets.ApprovalVerifier, []byte, error) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInternal, err, "vault get --clipboard: could not generate the approval key")
	}
	defer clear(private)
	verifier, err := policy.NewApprovalVerifier(pub, clock)
	if err != nil {
		return nil, nil, err
	}
	signer, err := policy.NewApprovalSigner(ephemeralApprovalKey{private: private}, clock)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256([]byte(name))
	signed, err := signer.Sign(ctx, policy.ApprovalRecord{
		Verb: secrets.ClipboardApprovalVerb, ParamsDigest: hex.EncodeToString(digest[:]), Requester: "cli", Approver: "cli",
		Origin: "cli", Audience: secrets.ClipboardApprovalVerb, Risk: policy.L4, Sensitivity: "secret",
	})
	if err != nil {
		return nil, nil, err
	}
	return verifier, signed, nil
}

// pendingClearsPath is <data_dir>/secrets/pending-clears.json.
func pendingClearsPath(paths runtime.PathProvider) (string, error) {
	if paths == nil || paths.DataDir() == "" {
		return "", cascade.New(cascade.KindUnavailable, "clipboard: could not resolve the cascade data directory")
	}
	return filepath.Join(paths.DataDir(), "secrets", "pending-clears.json"), nil
}

// newHostClipboard is vaultDeps.NewClipboard's production default: the host
// clipboard tool, the file pending store and the system scheduler.
func newHostClipboard(paths runtime.PathProvider) func(time.Duration, secrets.ApprovalVerifier, audit.Writer, audit.EffectWriter) (secrets.ClipboardWriter, error) {
	return func(ttl time.Duration, v secrets.ApprovalVerifier, aw audit.Writer, fx audit.EffectWriter) (secrets.ClipboardWriter, error) {
		path, err := pendingClearsPath(paths)
		if err != nil {
			return nil, err
		}
		return secrets.NewClipboardWriter(runtime.NewSystemClock(), aw, fx, v,
			secrets.NewFilePendingClearStore(path), secrets.NewSystemClipboardScheduler(), ttl)
	}
}
