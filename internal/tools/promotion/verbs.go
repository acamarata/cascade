package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/release/promotion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runBuild builds, validates and writes the promotion record of a signed
// dist-<id>. The signature is verified before any line is read, the
// directory's bytes are checked against the signed set, and --out is
// written only when every check passed.
func runBuild(w *output.Writer, clk runtime.Clock, args []string) int {
	var rcTag, commit, dir, pubPath, gate, out string
	err := parseFlags("build", args, func(fs *flag.FlagSet) {
		fs.StringVar(&rcTag, "rc-tag", "", "rc tag v2.Y.Z-rc.N")
		fs.StringVar(&commit, "commit", "", "40-hex commit")
		fs.StringVar(&dir, "dir", "", "signed dist-<id> directory")
		fs.StringVar(&pubPath, "pubkey", "", "minisign public key file")
		fs.StringVar(&gate, "gate-report", "", "signed release-gate report")
		fs.StringVar(&out, "out", "", "record JSON to write")
	}, &rcTag, &commit, &dir, &pubPath, &gate, &out)
	if err != nil {
		return fail(w, err)
	}
	rec, err := buildRecord(clk, rcTag, commit, dir, pubPath, gate)
	if err != nil {
		return fail(w, err)
	}
	moduleTag, err := buildinfo.ModuleTagFor(rec.FinalTag)
	if err != nil {
		return fail(w, err)
	}
	if err = writeRecord(out, rec); err != nil {
		return fail(w, err)
	}
	w.Println("promotion record written: " + out)
	w.Println("module tag twin of " + rec.FinalTag + ": " + moduleTag)
	return 0
}

// buildRecord runs Build, VerifySigned over the directory and WithImages.
func buildRecord(clk runtime.Clock, rcTag, commit, dir, pubPath, gate string) (promotion.Record, error) {
	pub, err := readPubKey(pubPath)
	if err != nil {
		return promotion.Record{}, err
	}
	sums, err := readInput(filepath.Join(dir, "cascade_checksums.txt"))
	if err != nil {
		return promotion.Record{}, err
	}
	sig, err := readInput(filepath.Join(dir, "cascade_checksums.txt.minisig"))
	if err != nil {
		return promotion.Record{}, err
	}
	rec, err := promotion.Build(sums, sig, pub, rcTag, commit, gate, clk)
	if err != nil {
		return promotion.Record{}, err
	}
	if _, err = promotion.VerifySigned(dir, pub, rec.FinalTag); err != nil {
		return promotion.Record{}, err
	}
	images, err := readInput(filepath.Join(dir, "cascade_images.txt"))
	if err != nil {
		return promotion.Record{}, err
	}
	if rec, err = promotion.WithImages(rec, images); err != nil {
		return promotion.Record{}, err
	}
	return rec, promotion.Validate(rec)
}

// writeRecord publishes rec at out atomically (old file or new, never torn).
func writeRecord(out string, rec promotion.Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil || len(data) >= promotion.MaxRecordBytes {
		return cascade.Newf(cascade.KindInvalidInput, "promotion: record does not encode within %d bytes", promotion.MaxRecordBytes)
	}
	if err = runtime.WriteFileAtomic(out, append(data, '\n'), 0o644); err != nil {
		return cascade.Newf(cascade.KindInvalidInput, "promotion: cannot write %s: %v", out, err)
	}
	return nil
}

// runVerify checks byte identity of a staging and a public set against a
// record.
func runVerify(w *output.Writer, _ runtime.Clock, args []string) int {
	var recPath, staging, public string
	err := parseFlags("verify", args, func(fs *flag.FlagSet) {
		fs.StringVar(&recPath, "record", "", "promotion record JSON")
		fs.StringVar(&staging, "staging", "", "staging set directory")
		fs.StringVar(&public, "public", "", "public set directory")
	}, &recPath, &staging, &public)
	if err != nil {
		return fail(w, err)
	}
	data, err := readInput(recPath)
	if err != nil {
		return fail(w, err)
	}
	rec, err := promotion.ParseRecord(data)
	if err != nil {
		return fail(w, err)
	}
	sets := [2]map[string]string{}
	for i, dir := range []string{staging, public} {
		if sets[i], err = promotion.SetFromDir(dir); err != nil {
			return fail(w, err)
		}
	}
	rep, err := promotion.VerifyIdentical(rec, sets[0], sets[1])
	if err != nil {
		return fail(w, err)
	}
	w.Println(fmt.Sprintf("identical: %d files match the record in staging and public", len(rep.Files)))
	return 0
}

// signedFlags parses --dir --pubkey --final-tag (plus extra) and runs
// VerifySigned.
func signedFlags(name string, args []string, extra func(fs *flag.FlagSet)) (promotion.Report, error) {
	var dir, pubPath, final string
	err := parseFlags(name, args, func(fs *flag.FlagSet) {
		fs.StringVar(&dir, "dir", "", "signed dist-<id> directory")
		fs.StringVar(&pubPath, "pubkey", "", "minisign public key file")
		fs.StringVar(&final, "final-tag", "", "final tag v2.Y.Z")
		extra(fs)
	}, &dir, &pubPath, &final)
	if err != nil {
		return promotion.Report{}, err
	}
	pub, err := readPubKey(pubPath)
	if err != nil {
		return promotion.Report{}, err
	}
	return promotion.VerifySigned(dir, pub, final)
}

// runVerifySigned is the promote job's first gate.
func runVerifySigned(w *output.Writer, _ runtime.Clock, args []string) int {
	rep, err := signedFlags("verify-signed", args, func(*flag.FlagSet) {})
	if err != nil {
		return fail(w, err)
	}
	w.Println(fmt.Sprintf("signed: %d files verified against the signed checksums", len(rep.Files)))
	return 0
}

// runImages prints signed image digests from a verified set: every line,
// the digest of --name, or the name of --require (refusing an unsigned one).
func runImages(w *output.Writer, _ runtime.Clock, args []string) int {
	var name, require string
	rep, err := signedFlags("images", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "print the signed digest of this image name")
		fs.StringVar(&require, "require", "", "refuse unless this digest is signed")
	})
	if err == nil && name != "" && require != "" {
		err = cascade.New(cascade.KindInvalidInput, "promotion images: --name and --require are exclusive")
	}
	if err != nil {
		return fail(w, err)
	}
	switch {
	case require != "":
		signed, reqErr := promotion.RequireSignedImage(rep, require)
		if reqErr != nil {
			return fail(w, reqErr)
		}
		w.Println(signed)
	case name != "":
		d, ok := rep.Images[name]
		if !ok {
			return fail(w, cascade.Newf(cascade.KindIntegrity, "promotion: refused: no signed image named %q", name))
		}
		w.Println(d)
	default:
		for _, n := range slices.Sorted(maps.Keys(rep.Images)) {
			w.Println(rep.Images[n] + "  " + n)
		}
	}
	return 0
}

// runCheckKey is the pages-keys gate: the published key must parse, equal
// the embedded key file byte for byte and be the compiled-in release key.
func runCheckKey(w *output.Writer, _ runtime.Clock, args []string) int {
	var pubPath, embeddedPath string
	err := parseFlags("check-key", args, func(fs *flag.FlagSet) {
		fs.StringVar(&pubPath, "pub", "", "published key (docs/keys/minisign.pub)")
		fs.StringVar(&embeddedPath, "embedded", "", "embedded key (internal/minisign/release.pub)")
	}, &pubPath, &embeddedPath)
	if err != nil {
		return fail(w, err)
	}
	published, err := readInput(pubPath)
	if err != nil {
		return fail(w, err)
	}
	embedded, err := readInput(embeddedPath)
	if err != nil {
		return fail(w, err)
	}
	trusted, err := minisign.ReleasePublicKey()
	if err != nil {
		return fail(w, err)
	}
	if err = promotion.CheckKey(published, embedded, trusted); err != nil {
		return fail(w, err)
	}
	w.Println("key: " + pubPath + " is the embedded release key")
	return 0
}
