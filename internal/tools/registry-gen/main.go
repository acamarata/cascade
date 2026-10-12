// Command registry-gen is the publisher-side registry index generator
// (R-14.73 — NO `registry` CLI noun; this is repo tooling the first-party
// team runs, never a cascade command). Invoked as
// `go run ./internal/tools/registry-gen`.
//
// Purpose: wire pkg/plugin/registry/generator's library into a
//
//	non-interactive main: every input is a flag, there is no wizard and
//	no --yes, and a plaintext --key flag is refused rather than accepted
//	(key material only ever travels as a vault key-ref).
//
// Inputs: --artifacts, --out, --base-url, --key-ref flags (all required
//
//	except --base-url); a plaintext --key flag, which is a hard refusal.
//
// Outputs: index.json written under --out; process exit code 0 on
//
//	success, non-zero on any failure, with the error on stderr via
//	internal/output.Writer (the only sanctioned real-stream seam —
//	internal/build/outputgate.go bans os.Stdout/os.Stderr/fmt.Print*
//	everywhere else, this package included).
//
// Constraints: CASCADE_NO_INPUT=1 has no effect and needs none — this
//
//	command reads no interactive input under any environment (06 §5.8
//	automation parity is satisfied by construction, not by a branch).
//	The signing key is resolved from an environment variable NAMED by
//	--key-ref (base64 or hex Ed25519 private key), not from
//	internal/secrets.Broker: Broker needs a platform Custody +
//	ElevationGate selection this ticket's files_scope (internal/tools/
//	registry-gen only) does not cover, and wiring it here would be a
//	scope decision beyond an S-weight ticket. Disclosed in the journal
//	as a real, deliberate scope boundary, not an oversight.
//
// SPORT: internal/tools/registry-gen (ADD) — P1-E24-W5-S50-T5.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/plugin/registry/generator"
)

var (
	errPlaintextKeyRefused  = errors.New("registry-gen: --key accepts no plaintext key material; use --key-ref with a vault reference")
	errMissingRequiredFlags = errors.New("registry-gen: --artifacts, --out, and --key-ref are required")
)

// envVault resolves a vault key-ref as the name of an environment variable
// holding the Ed25519 private key, base64 or hex encoded. It never accepts
// key material directly on the command line.
type envVault struct{ key []byte }

func (v envVault) Get(_ context.Context, keyRef string) ([]byte, error) {
	if v.key != nil {
		return v.key, nil
	}
	raw := os.Getenv(keyRef)
	if raw == "" {
		return nil, os.ErrNotExist
	}
	if keyRef == "MINISIGN_RELEASE_PASSPHRASE" {
		return []byte(raw), nil
	}
	if key, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return key, nil
	}
	return hex.DecodeString(raw)
}

func main() {
	w := output.NewDefault(false, false, false, false)
	os.Exit(run(w, os.Args[1:]))
}

// run implements the command over an injected Writer and argv slice so
// tests never touch the real process streams or os.Args.
func run(w *output.Writer, args []string) int {
	fs := flag.NewFlagSet("registry-gen", flag.ContinueOnError)
	artifacts := fs.String("artifacts", "", "directory of (artifact, manifest) pairs")
	out := fs.String("out", "", "output directory for index.json")
	baseURL := fs.String("base-url", "", "base URL for DownloadURL construction")
	keyRef := fs.String("key-ref", "", "vault key-ref (env var name) naming the Ed25519 signing key")
	plaintextKey := fs.String("key", "", "REFUSED: plaintext key material is never accepted, use --key-ref")
	derive := fs.Bool("derive-from-release", false, "derive signing key from release custody")
	printPublic := fs.Bool("print-public", false, "print derived registry public key hex")
	printRelease := fs.Bool("print-release-public", false, "print paired minisign release public file")
	printDefault := fs.Bool("print-default-public", false, "print embedded registry public key hex")
	fs.SetOutput(io.Discard) // usage/parse-error text goes through w.Fail below, not flag's own stream
	if err := fs.Parse(args); err != nil {
		w.Fail(err)
		return 2
	}
	if *plaintextKey != "" {
		w.Fail(errPlaintextKeyRefused)
		return 1
	}
	if *derive || *printPublic || *printRelease || *printDefault {
		return runRelease(w, *derive, *printPublic, *printRelease, *printDefault, *keyRef, *artifacts, *baseURL, *out)
	}
	if *artifacts == "" || *out == "" || *keyRef == "" {
		w.Fail(errMissingRequiredFlags)
		return 2
	}
	return generate(w, *artifacts, *baseURL, *keyRef, *out)
}

func generate(w *output.Writer, artifacts, baseURL, keyRef, out string) int {
	return generateWithVault(w, artifacts, baseURL, keyRef, out, envVault{})
}

func generateWithVault(w *output.Writer, artifacts, baseURL, keyRef, out string, vault envVault) int {
	idx, err := generator.GenerateIndex(generator.GeneratorConfig{ArtifactsDir: artifacts, BaseURL: baseURL})
	if err != nil {
		w.Fail(err)
		return 1
	}
	key, err := vault.Get(context.Background(), keyRef)
	defer clear(key)
	if err != nil {
		w.Fail(err)
		return 1
	}
	if err := generator.SignIndex(idx, keyRef, envVault{key: key}); err != nil {
		w.Fail(err)
		return 1
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		w.Fail(err)
		return 1
	}
	if err := generator.WriteIndex(idx, out); err != nil {
		w.Fail(err)
		return 1
	}
	w.Println("registry-gen: wrote index.json")
	return 0
}

// deriveAndZeroSeed consumes the caller's seed copy on both success and error.
func deriveAndZeroSeed(seed []byte) (ed25519.PrivateKey, error) {
	defer clear(seed)
	return generator.DeriveRegistryKey(seed)
}

func runRelease(w *output.Writer, derive, printPublic, printRelease, printDefault bool, keyRef, artifacts, baseURL, out string) int {
	if printDefault && !derive && !printPublic && !printRelease && keyRef == "" {
		w.Println(hex.EncodeToString(plugin.DefaultRegistryPublicKey()))
		return 0
	}
	if !derive || keyRef != "" || printDefault || (printPublic && printRelease) {
		w.Fail(errors.New("registry-gen: release derivation and --key-ref are mutually exclusive; public output requires --derive-from-release"))
		return 2
	}
	sk, err := releaseSecret()
	if err != nil {
		w.Fail(err)
		return 1
	}
	defer sk.Zero()
	if printRelease {
		printReleaseKey(w, sk)
		return 0
	}
	key, err := deriveAndZeroSeed(sk.Seed())
	if err != nil {
		w.Fail(err)
		return 1
	}
	defer clear(key)
	if printPublic {
		w.Println(hex.EncodeToString(key.Public().(ed25519.PublicKey)))
		return 0
	}
	if artifacts == "" || out == "" {
		w.Fail(errMissingRequiredFlags)
		return 2
	}
	return generateWithVault(w, artifacts, baseURL, "release", out, envVault{key: key})
}

// releaseSecret reads named custody values; errors expose names, never values.
func releaseSecret() (minisign.SecretKey, error) {
	file, err := (envVault{}).Get(context.Background(), "MINISIGN_RELEASE_SECKEY_B64")
	if err != nil {
		return minisign.SecretKey{}, cascade.New(cascade.KindUnavailable, "registry-gen: missing or invalid MINISIGN_RELEASE_SECKEY_B64")
	}
	defer clear(file)
	pass, err := (envVault{}).Get(context.Background(), "MINISIGN_RELEASE_PASSPHRASE")
	defer clear(pass)
	if err != nil || len(pass) == 0 {
		return minisign.SecretKey{}, cascade.New(cascade.KindUnavailable, "registry-gen: missing MINISIGN_RELEASE_PASSPHRASE")
	}
	return minisign.DecryptSecretKey(file, pass)
}

// printReleaseKey recomputes the paired public file, erasing secret scratch.
func printReleaseKey(w *output.Writer, sk minisign.SecretKey) {
	seed := sk.Seed()
	defer clear(seed)
	priv := ed25519.NewKeyFromSeed(seed)
	defer clear(priv)
	id := sk.KeyID()
	raw := append([]byte("Ed"), id[:]...)
	raw = append(raw, priv.Public().(ed25519.PublicKey)...)
	w.Println(fmt.Sprintf("untrusted comment: minisign public key %016X", binary.LittleEndian.Uint64(id[:])))
	w.Println(base64.StdEncoding.EncodeToString(raw))
}
