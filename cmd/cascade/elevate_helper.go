// Purpose: `cascade elevate-helper`, the hidden [local] subcommand that is
//
//	the user-session half of the §D-24 elevation flow: --enroll generates
//	(if absent) and TOFU-enrolls this device's Ed25519 elevation key;
//	--sign performs local authentication and produces a signed
//	attestation in the exact shape internal/rpc/elevation_attest.go
//	verifies. --enroll --presence enrolls the linux passphrase tier.
//
// Inputs: cobra flags/args; an injected elevateHelperDeps so no test
//
//	touches the real Keychain/PAM/keyring or CASCADE_HOME (Art.7.1).
//
// Outputs: --enroll prints the enrolled fingerprint to stderr and exits 0
//
//	(or a typed refusal, non-zero); --sign prints the JSON attestation to
//	stdout and exits 0 (or a typed refusal, non-zero).
//
// Constraints: never exposed via MCP (Annotations["local"]="true") and
//
//	Hidden from `cascade --help` per 07-CLI-COMMAND-TREE.md. Output goes
//	through cmd.OutOrStdout()/cmd.ErrOrStderr() because its byte shape is
//	part of a contract (the attestation JSON).
//
// SPORT: cmd/cascade elevate-helper/ADDED (P1-E04-W1-S07-T6).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestationTTL is the fixed 5-minute expiry this ticket's contract
// specifies, driven by the injected Clock (never bare time.Now).
const attestationTTL = 5 * time.Minute

// elevateHelperDeps carries every external input this command needs, so
// tests inject fakes and never touch the real keychain or CASCADE_HOME.
type elevateHelperDeps struct {
	// Keystore resolves source-classified custody for signing.
	Keystore func() elevation.Custody
	// Enroll generates a protected key without falling back to file custody.
	Enroll func() (elevation.Custody, error)
	// TrustBackend constructs the trust-record Backend, deferred so building
	// the command tree never touches the environment.
	TrustBackend func() elevation.Backend
	// EnrollPresence runs the linux passphrase presence enrolment (--enroll
	// --presence) and returns the new key's fingerprint.
	EnrollPresence func(ctx context.Context) (string, error)
	Clock          elevation.Clock
	Getenv         runtime.Getenv
	DataDir        func() string
}

// productionElevateHelperDeps builds elevateHelperDeps against the real
// environment.
func productionElevateHelperDeps() elevateHelperDeps {
	paths := lazyPaths{}
	return elevateHelperDeps{
		Keystore: productionCustody,
		Enroll: func() (elevation.Custody, error) {
			return elevation.Enroll(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
		},
		TrustBackend: func() elevation.Backend {
			return elevation.NewFileBackend(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
		},
		DataDir: func() string { return paths.get(func(p runtime.PathProvider) string { return p.DataDir() }) },
		EnrollPresence: func(ctx context.Context) (string, error) {
			dir := paths.get(func(p runtime.PathProvider) string { return p.DataDir() })
			clock := runtime.NewSystemClock()
			return elevation.EnrollPresence(dir, elevation.NewFileBackend(dir), clock, presenceEffects{ctx: ctx, open: presenceEffectsOpener(paths, clock)})
		},
		Clock:  runtime.NewSystemClock(),
		Getenv: os.Getenv,
	}
}

// mountElevateHelperCmd attaches the hidden `elevate-helper` command.
func mountElevateHelperCmd(root *cobra.Command) {
	root.AddCommand(newElevateHelperCmd(productionElevateHelperDeps()))
}

// newElevateHelperCmd builds the elevate-helper command.
func newElevateHelperCmd(deps elevateHelperDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "elevate-helper",
		Short:       "Local elevation helper: enroll or sign an attestation (internal use only)",
		Hidden:      true,
		Annotations: map[string]string{"local": "true"},
		Args:        usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runElevateHelper(cmd, deps, args)
		},
	}
	cmd.Flags().Bool("replace-file-tier", false, "replace an enrollment bound to the stale file key")
	cmd.Flags().Bool("enroll", false, "generate (if absent) and TOFU-enroll this device's elevation key")
	cmd.Flags().Bool("presence", false, "with --enroll: enroll the linux passphrase presence key (weaker than hardware custody)")
	cmd.Flags().Bool("sign", false, "authenticate and sign <request-id> <action-hash> <nonce> atomically")
	return cmd
}

// runElevateHelper dispatches between --enroll and --sign. Exactly one
// must be set; an ambiguous invocation refuses rather than guessing.
func runElevateHelper(cmd *cobra.Command, deps elevateHelperDeps, args []string) error {
	enroll, _ := cmd.Flags().GetBool("enroll")
	sign, _ := cmd.Flags().GetBool("sign")
	presence, _ := cmd.Flags().GetBool("presence")
	replace, _ := cmd.Flags().GetBool("replace-file-tier")
	switch {
	case presence && (!enroll || replace):
		return cascade.New(cascade.KindInvalidInput, "elevate-helper: --presence is valid only with --enroll, and not with --replace-file-tier")
	case enroll == sign:
		return cascade.New(cascade.KindInvalidInput, "elevate-helper: exactly one of --enroll or --sign is required")
	case enroll:
		if len(args) != 0 {
			return cascade.New(cascade.KindInvalidInput, "elevate-helper --enroll takes no positional arguments")
		}
		if presence {
			return runElevateHelperEnrollPresence(cmd, deps)
		}
		return runElevateHelperEnroll(cmd, deps)
	default:
		if len(args) != 3 {
			return cascade.New(cascade.KindInvalidInput, "elevate-helper --sign requires exactly <request-id> <action-hash> <nonce>")
		}
		return runElevateHelperSign(cmd, deps, args[0], args[1], args[2])
	}
}

// runElevateHelperEnroll implements --enroll: generate (idempotent) then
// TOFU-enroll. A pre-existing record is refused with ErrAlreadyEnrolled.
func runElevateHelperEnroll(cmd *cobra.Command, deps elevateHelperDeps) error {
	ks, err := enrollKeystore(deps)
	if err != nil {
		return err
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		return err
	}
	store := elevation.NewElevationTrustStore(deps.TrustBackend(), deps.Clock)
	replace, _ := cmd.Flags().GetBool("replace-file-tier")
	var fp string
	if replace {
		fp, err = elevation.ReplaceFileTierEnrollment(deps.Keystore(), deps.TrustBackend(), deps.Clock, deps.DataDir())
	} else {
		fp, err = store.Enroll(pub)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "elevation: enrolled trust key, fingerprint sha256:%s\n", fp)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevate-helper: write enrollment confirmation")
	}
	return nil
}

// runElevateHelperEnrollPresence implements --enroll --presence; prompts,
// rotation check, audit and trust record all live in elevation.EnrollPresence.
func runElevateHelperEnrollPresence(cmd *cobra.Command, deps elevateHelperDeps) error {
	if deps.Getenv("CASCADE_NO_INPUT") == "1" {
		return elevation.ErrNoInput()
	}
	if deps.EnrollPresence == nil {
		return cascade.New(cascade.KindInternal, "elevate-helper: no presence enrolment is configured")
	}
	fp, err := deps.EnrollPresence(cmd.Context())
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "elevation: enrolled presence key (weaker than hardware custody), fingerprint sha256:%s\n", fp); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevate-helper: write enrollment confirmation")
	}
	return nil
}

// presenceEffects audits a presence enrolment through the shared effect
// log: the intent record is committed before the key file is replaced, and a
// failure to open or write it refuses the enrolment.
type presenceEffects struct {
	ctx  context.Context
	open func(context.Context) (audit.EffectWriter, func(), error)
}

// presenceEffectsOpener opens the process's effect log over the runtime
// store; the returned func closes the store.
func presenceEffectsOpener(paths runtime.PathProvider, clock runtime.Clock) func(context.Context) (audit.EffectWriter, func(), error) {
	return func(ctx context.Context) (audit.EffectWriter, func(), error) {
		store, closeStore, err := openPluginStore(ctx, paths, clock)
		if err != nil {
			return nil, nil, err
		}
		log, err := openEffectLog(store, clock, nil, paths)
		if err != nil {
			closeStore()
			return nil, nil, err
		}
		return log, closeStore, nil
	}
}

// Begin commits the enrolment intent and returns the outcome recorder.
func (p presenceEffects) Begin(fingerprint, kind string) (func(error), error) {
	log, closeStore, err := p.open(p.ctx)
	if err != nil {
		return nil, err
	}
	explain, _ := json.Marshal(map[string]string{"fingerprint": fingerprint, "kind": kind})
	h, err := log.BeginEffect(p.ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "elevate-helper",
		Action: "elevation.presence.enroll", Key: "elevation.presence.enroll:" + fingerprint,
		ParamsHash: audit.HashParams([]byte(fingerprint)), Explain: explain})
	if err != nil {
		closeStore()
		return nil, err
	}
	return func(runErr error) {
		defer closeStore()
		if runErr != nil {
			_, _ = log.FailEffect(p.ctx, h, "the enrolment did not complete")
			return
		}
		_, _ = log.ConfirmEffect(p.ctx, h, "enrolled")
	}, nil
}

// enrollKeystore runs the deps' enrolment, or the plain generate a test's
// in-process fake supplies.
func enrollKeystore(deps elevateHelperDeps) (elevation.ElevationKeystore, error) {
	if deps.Enroll != nil {
		c, err := deps.Enroll()
		if err != nil {
			return nil, err
		}
		return c.Signer()
	}
	ks, err := deps.Keystore().Signer()
	if err != nil {
		return nil, err
	}
	if err := ks.GenerateKey(); err != nil {
		return nil, err
	}
	return ks, nil
}

// runElevateHelperSign implements --sign: under CASCADE_NO_INPUT=1 it
// refuses BEFORE calling Keystore() (§5.8 rule-23 automation parity);
// otherwise it signs via ks.Sign and prints the attestation shape
// internal/rpc/elevation_attest.go verifies.
func runElevateHelperSign(cmd *cobra.Command, deps elevateHelperDeps, requestID, actionHash, nonce string) error {
	if deps.Getenv("CASCADE_NO_INPUT") == "1" {
		return elevation.ErrNoInput()
	}
	ks, err := deps.Keystore().Signer()
	if err != nil {
		return err
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		return err
	}
	fp, err := elevation.Fingerprint(pub)
	if err != nil {
		return err
	}
	now := deps.Clock.Now()
	att := rpc.Attestation{
		RequestID:         requestID,
		ActionHash:        actionHash,
		Nonce:             nonce,
		PubkeyFingerprint: fp,
		IssuedUnix:        now.Unix(),
		ExpUnix:           now.Add(attestationTTL).Unix(),
	}
	sig, err := ks.Sign(canonicalSignedBytes(att))
	if err != nil {
		return err
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	data, err := json.Marshal(att)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "elevate-helper: marshal attestation")
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(data)); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevate-helper: write attestation")
	}
	return nil
}
