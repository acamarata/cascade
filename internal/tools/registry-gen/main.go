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
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/plugin/registry/generator"
)

var (
	errPlaintextKeyRefused  = errors.New("registry-gen: --key accepts no plaintext key material; use --key-ref with a vault reference")
	errMissingRequiredFlags = errors.New("registry-gen: --artifacts, --out, and --key-ref are required")
)

// envVault resolves a vault key-ref as the name of an environment variable
// holding the Ed25519 private key, base64 or hex encoded. It never accepts
// key material directly on the command line.
type envVault struct{}

func (envVault) Get(_ context.Context, keyRef string) ([]byte, error) {
	raw := os.Getenv(keyRef)
	if raw == "" {
		return nil, os.ErrNotExist
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
	fs.SetOutput(io.Discard) // usage/parse-error text goes through w.Fail below, not flag's own stream
	if err := fs.Parse(args); err != nil {
		w.Fail(err)
		return 2
	}
	if *plaintextKey != "" {
		w.Fail(errPlaintextKeyRefused)
		return 1
	}
	if *artifacts == "" || *out == "" || *keyRef == "" {
		w.Fail(errMissingRequiredFlags)
		return 2
	}
	return generate(w, *artifacts, *baseURL, *keyRef, *out)
}

func generate(w *output.Writer, artifacts, baseURL, keyRef, out string) int {
	idx, err := generator.GenerateIndex(generator.GeneratorConfig{ArtifactsDir: artifacts, BaseURL: baseURL})
	if err != nil {
		w.Fail(err)
		return 1
	}
	if err := generator.SignIndex(idx, keyRef, envVault{}); err != nil {
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
