// Command promotion is the release promotion tool (repo tooling, never a
// cascade command), invoked as `go run ./internal/tools/promotion <verb>`.
//
// Purpose: wire internal/release/promotion into five non-interactive verbs:
//
//	build         --rc-tag --commit --dir --pubkey --gate-report --out
//	verify        --record --staging --public
//	verify-signed --dir --pubkey --final-tag
//	images        --dir --pubkey --final-tag [--name N | --require DIGEST]
//	check-key     --pub --embedded
//
// Inputs: flags only (no environment variable is read); files named by them.
// Outputs: exit 0 ok, 1 refused or mismatched (the error names the file or
// check), 2 bad input. build writes --out only after every check passed.
// Text goes through internal/output.Writer, the sanctioned stream seam.
// Constraints: verify-signed is the promote job's trust root; images prints
// or admits only digests from a verified signed cascade_images.txt.
// SPORT: internal/tools/promotion (ADD).
package main

import (
	"flag"
	"io"
	"os"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func main() {
	w := output.NewDefault(false, false, false, false)
	os.Exit(run(w, runtime.NewSystemClock(), os.Args[1:]))
}

// verbs maps each subcommand to its implementation.
var verbs = map[string]func(w *output.Writer, clk runtime.Clock, args []string) int{
	"build":         runBuild,
	"verify":        runVerify,
	"verify-signed": runVerifySigned,
	"images":        runImages,
	"check-key":     runCheckKey,
}

// run dispatches args[0] over an injected Writer and Clock, so tests never
// touch the real streams, os.Args or the wall clock.
func run(w *output.Writer, clk runtime.Clock, args []string) int {
	if len(args) == 0 || verbs[args[0]] == nil {
		w.Fail(cascade.New(cascade.KindInvalidInput, "promotion: usage: promotion build|verify|verify-signed|images|check-key [flags]"))
		return 2
	}
	return verbs[args[0]](w, clk, args[1:])
}

// fail reports err and maps it to the tool's exit code: a KindIntegrity
// refusal is 1, anything else is bad input (2).
func fail(w *output.Writer, err error) int {
	w.Fail(err)
	if cascade.HasKind(err, cascade.KindIntegrity) {
		return 1
	}
	return 2
}

// parseFlags parses args into a fresh FlagSet built by define and requires
// every named flag to be non-empty.
func parseFlags(name string, args []string, define func(fs *flag.FlagSet), required ...*string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	define(fs)
	if err := fs.Parse(args); err != nil {
		return cascade.Newf(cascade.KindInvalidInput, "promotion %s: %v", name, err)
	}
	if fs.NArg() != 0 {
		return cascade.Newf(cascade.KindInvalidInput, "promotion %s: unexpected argument %q", name, fs.Arg(0))
	}
	for _, v := range required {
		if *v == "" {
			return cascade.Newf(cascade.KindInvalidInput, "promotion %s: a required flag is missing", name)
		}
	}
	return nil
}

// readInput reads a file named by a flag; a missing file is bad input.
func readInput(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, cascade.Newf(cascade.KindInvalidInput, "promotion: cannot read %s: %v", path, err)
	}
	return b, nil
}

// readPubKey reads and parses a minisign public key file.
func readPubKey(path string) (minisign.PublicKey, error) {
	b, err := readInput(path)
	if err != nil {
		return minisign.PublicKey{}, err
	}
	return minisign.ParsePublicKey(b)
}
