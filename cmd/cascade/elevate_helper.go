// Purpose: `cascade elevate-helper`, the hidden [local] subcommand that is
//
//	the user-session half of the §D-24 elevation flow: --enroll generates
//	(if absent) and TOFU-enrolls this device's Ed25519 elevation key;
//	--sign performs local authentication and produces a signed
//	attestation in the exact shape internal/rpc/elevation_attest.go
//	verifies.
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
//	Hidden from `cascade --help` per 07-CLI-COMMAND-TREE.md. Never writes
//	to os.Stdout/os.Stderr directly outside internal/output — this file
//	uses cmd.OutOrStdout()/cmd.ErrOrStderr(), matching version.go's own
//	precedent for output whose exact byte shape is part of a contract
//	(the attestation JSON here; the version banner there).
//
// SPORT: cmd/cascade elevate-helper/ADDED (P1-E04-W1-S07-T6).
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestationTTL is the fixed 5-minute expiry this ticket's contract
// specifies, driven by the injected Clock (never bare time.Now).
const attestationTTL = 5 * time.Minute

// elevateHelperDeps carries every external input this command needs, so
// tests inject fakes and never touch the real Keychain/PAM/keyring or
// CASCADE_HOME (Art.7.1), matching daemonDeps's established pattern.
type elevateHelperDeps struct {
	// Keystore constructs the platform ElevationKeystore. A func rather
	// than a value: production resolves it lazily per invocation (no
	// hardware/OS probing at command-tree construction time), and tests
	// substitute an in-process fake.
	Keystore func() elevation.ElevationKeystore
	// Enroll generates this host's device key, falling back to the file
	// keystore when the platform one is reachable but cannot store (the
	// W-3 gate's OSStatus -34018 case). Separate from Keystore because
	// enrolment is the only operation allowed to CHANGE which backend this
	// host is on. Nil means "use Keystore().GenerateKey()", which is what
	// the in-process fakes in this package's tests want.
	Enroll func() (elevation.ElevationKeystore, error)
	// TrustBackend constructs the trust-record Backend. Deferred for the
	// same reason lazyPaths defers path resolution elsewhere in this
	// package: constructing the tree must never touch the environment.
	TrustBackend func() elevation.Backend
	Clock        elevation.Clock
	Getenv       runtime.Getenv
}

// productionElevateHelperDeps builds elevateHelperDeps against the real
// environment.
func productionElevateHelperDeps() elevateHelperDeps {
	paths := lazyPaths{}
	return elevateHelperDeps{
		Keystore: productionKeystore,
		Enroll: func() (elevation.ElevationKeystore, error) {
			return elevation.Enroll(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
		},
		TrustBackend: func() elevation.Backend {
			return elevation.NewFileBackend(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
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
	cmd.Flags().Bool("enroll", false, "generate (if absent) and TOFU-enroll this device's elevation key")
	cmd.Flags().Bool("sign", false, "authenticate and sign <request-id> <action-hash> <nonce> atomically")
	return cmd
}

// runElevateHelper dispatches between --enroll and --sign. Exactly one
// must be set; anything else is a taxonomy invalid-input refusal, never a
// guess at which mode was intended (R-14.163's spirit applies to CLI
// dispatch too: an ambiguous invocation must refuse, not pick one).
func runElevateHelper(cmd *cobra.Command, deps elevateHelperDeps, args []string) error {
	enroll, _ := cmd.Flags().GetBool("enroll")
	sign, _ := cmd.Flags().GetBool("sign")
	switch {
	case enroll == sign:
		return cascade.New(cascade.KindInvalidInput, "elevate-helper: exactly one of --enroll or --sign is required")
	case enroll:
		if len(args) != 0 {
			return cascade.New(cascade.KindInvalidInput, "elevate-helper --enroll takes no positional arguments")
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
// TOFU-enroll. On a pre-existing record, ElevationTrustStore.Enroll
// refuses with ErrAlreadyEnrolled and this function does NOT print a
// fingerprint or touch the record.
func runElevateHelperEnroll(cmd *cobra.Command, deps elevateHelperDeps) error {
	ks, err := enrollKeystore(deps)
	if err != nil {
		return err
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		return err
	}
	// Name the tier. A file-backed device key is a WEAKER proof than a
	// hardware-gated one — possession of a 0600 file rather than a human at
	// the device — and an operator who is on it must be told, here and by
	// `cascade doctor`. A fallback the operator cannot see is the dishonest
	// version of this fix (P1-W3-01).
	if ks.Tier() == elevation.TierFile {
		if _, werr := fmt.Fprintln(cmd.ErrOrStderr(),
			"elevation: no hardware or OS keystore is usable on this host; "+
				"enrolled a FILE-backed device key instead. This proves possession of a "+
				"0600 key file in the cascade data directory, not local presence."); werr != nil {
			return cascade.Wrap(cascade.KindUnavailable, werr, "elevate-helper: write the tier notice")
		}
	}
	store := elevation.NewElevationTrustStore(deps.TrustBackend(), deps.Clock)
	fp, err := store.Enroll(pub)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "elevation: enrolled trust key, fingerprint sha256:%s\n", fp)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevate-helper: write enrollment confirmation")
	}
	return nil
}

// enrollKeystore runs the deps' enrolment, or the plain generate a test's
// in-process fake supplies.
func enrollKeystore(deps elevateHelperDeps) (elevation.ElevationKeystore, error) {
	if deps.Enroll != nil {
		return deps.Enroll()
	}
	ks := deps.Keystore()
	if err := ks.GenerateKey(); err != nil {
		return nil, err
	}
	return ks, nil
}

// runElevateHelperSign implements --sign: under CASCADE_NO_INPUT=1 this
// refuses BEFORE calling Keystore() at all, so no auth prompt is ever
// reached (§5.8 rule-23 automation parity). Otherwise it authenticates and
// signs atomically via ks.Sign, then prints the exact attestation shape
// internal/rpc/elevation_attest.go verifies.
func runElevateHelperSign(cmd *cobra.Command, deps elevateHelperDeps, requestID, actionHash, nonce string) error {
	if deps.Getenv("CASCADE_NO_INPUT") == "1" {
		return elevation.ErrNoInput()
	}

	ks := deps.Keystore()
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

// canonicalSignedBytes reproduces, field-for-field and in the same
// alphabetical-by-JSON-tag order, the exact byte sequence
// internal/rpc/elevation_attest.go's unexported signedFields computes —
// this ticket does not get to invent its own attestation format, and
// internal/rpc is out of this ticket's files_scope to import the
// unexported helper from, so the shape is reproduced here deliberately,
// proven identical by TestElevateHelperCmd_SignVerifiesAgainstRealVerifier
// signing with THIS function and verifying with rpc.VerifyAttestation.
func canonicalSignedBytes(att rpc.Attestation) []byte {
	type signable struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}
	b, err := json.Marshal(signable{
		ActionHash:        att.ActionHash,
		ExpUnix:           att.ExpUnix,
		IssuedUnix:        att.IssuedUnix,
		Nonce:             att.Nonce,
		PubkeyFingerprint: att.PubkeyFingerprint,
		RequestID:         att.RequestID,
	})
	if err != nil {
		return nil
	}
	return b
}
