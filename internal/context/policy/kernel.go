// Package policy holds the execution kernel every generated instruction
// file carries (contract:policy-kernel): five working rules and one
// escalation line, small enough to sit in an always-loaded file.
package policy

// Purpose: the one source of the kernel text, its token budget and the
//   canonical bytes of .cascade/policy-kernel.md.
// Inputs: none for the text; a repository root for EnsureKernelFile.
// Outputs: KernelText, RenderKernelFile's bytes, and EnsureKernelFile's
//   created flag.
// Constraints: RenderKernelFile is THE canonical kernel file. Every writer
//   of the file and the hash constant in pkg/provider derive from it, so
//   the kernel cannot drift between the generator and its consumers. No
//   clock, no environment, no map iteration: equal input, equal bytes.
// SPORT: context-engine/policy-kernel (ADD, P1-GEN-06).

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// KernelVersion is the kernel's version; the managed block id is
// "policy-kernel@" + KernelVersion.
const KernelVersion = "1"

// KernelText is the execution kernel (R-14.249): five rules and the
// escalation line, one per line, LF-terminated.
const KernelText = "SCOPE: change only what the task names; leave the rest as it is.\n" +
	"SIMPLICITY: write the least code that meets the acceptance.\n" +
	"GROUNDING: read the code or run the tool before you state a fact.\n" +
	"AMBIGUITY: state your assumption, or ask, before you guess.\n" +
	"VERIFICATION: done means the named checks ran and passed.\n" +
	"ESCALATION: after three failed attempts, stop and report what failed.\n"

// kernelBlockID is the managed block id the kernel is wrapped in.
const kernelBlockID = "policy-kernel@" + KernelVersion

// TokenCount is the P1 token counter: provider.NaiveTokenCounter's
// estimate of one token per four Unicode code points.
func TokenCount(s string) int {
	n, _ := provider.NaiveTokenCounter{}.Count(context.Background(), s)
	return n
}

// RenderKernelFile returns the canonical bytes of .cascade/policy-kernel.md:
// KernelText inside one Markdown managed block, policy-kernel@1.
func RenderKernelFile() []byte {
	return []byte("<!-- cascade:managed:begin " + kernelBlockID + " -->\n" +
		KernelText + "<!-- cascade:managed:end -->\n")
}

// EnsureKernelFile makes sure repoRoot holds the canonical kernel file at
// provider.PolicyKernelFile. It creates the file when it is absent and
// reports created; a file that already holds the canonical bytes is left
// alone. Any other content is KindConflict and the file is not touched: a
// hand-edited kernel goes back through the generator, never over the top.
func EnsureKernelFile(repoRoot string) (created bool, err error) {
	if repoRoot == "" {
		return false, cascade.New(cascade.KindInvalidInput, "policy: empty repository root")
	}
	want := RenderKernelFile()
	p := filepath.Join(repoRoot, filepath.FromSlash(provider.PolicyKernelFile))
	same, present, err := matchesKernel(p, want)
	if err != nil || present {
		if err == nil && !same {
			err = cascade.Newf(cascade.KindConflict,
				"policy: %s differs from the canonical kernel; regenerate it", provider.PolicyKernelFile)
		}
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "policy: create %s", filepath.Dir(provider.PolicyKernelFile))
	}
	created, err = runtime.CreateFileAtomic(p, want, 0o644)
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "policy: write %s", provider.PolicyKernelFile)
	}
	if !created {
		return EnsureKernelFile(repoRoot)
	}
	return true, nil
}

// matchesKernel reports whether a regular file is at p and whether it
// holds want. A symlink or directory at p is KindConflict.
func matchesKernel(p string, want []byte) (same, present bool, err error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, cascade.Wrapf(cascade.KindUnavailable, err, "policy: stat %s", provider.PolicyKernelFile)
	}
	if !info.Mode().IsRegular() {
		return false, true, cascade.Newf(cascade.KindConflict, "policy: %s is not a regular file", provider.PolicyKernelFile)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		return false, true, cascade.Wrapf(cascade.KindUnavailable, err, "policy: read %s", provider.PolicyKernelFile)
	}
	return bytes.Equal(got, want), true, nil
}
