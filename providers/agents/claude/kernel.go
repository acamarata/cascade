// Purpose: the daemon-owned policy kernel on the spawn path
//   (contract:daemon-policy-kernel): read the kernel file the composition
//   root names once, through one no-follow descriptor, check its bytes
//   against provider.PolicyKernelSHA256 and hand the checked bytes on as
//   text, so the harness never opens a path a repository could swap.
// Inputs: the DriverDefaults KernelPromptFile / SubagentKernelPromptFile
//   paths (<DataDir>/policy/policy-kernel.md, written at daemon start).
// Outputs: DriverDefaults with KernelPrompt / SubagentKernelPrompt set to
//   the verified bytes and both *File fields cleared; KindIntegrity naming
//   the field on any failure, before any child runs.
// Constraints: a path is accepted only when absolute, clean and ending in
//   the daemon's policy/policy-kernel.md, so a worktree or repository
//   kernel (.cascade/policy-kernel.md) is never read. The file is Lstat-ed,
//   opened with O_NOFOLLOW|O_NONBLOCK (unix; windows relies on the Lstat
//   plus SameFile pair), must be regular and the same file the Lstat saw,
//   and is read at most PolicyKernelMaxBytes+1 bytes from that descriptor;
//   the hash is of exactly those bytes. Text fields set by composition are
//   refused: only this file sets them. The subagent text flag rides only a
//   headless --print argv.
// SPORT: providers.agents.claude/ADD (P1-GEN-12).

package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// daemonKernelSuffix is the tail every accepted kernel path ends in: the
// daemon's <DataDir>/policy/policy-kernel.md (policy.KernelFileRel, which
// this package may not import).
const daemonKernelSuffix = "/policy/policy-kernel.md"

// spawnDefaults is the first guard prepare runs: the kernel verified into
// text, the unsupported-flag refusal on what is left, then the job's tool
// policy resolved, each refusing before the next runs.
func spawnDefaults(d provider.DriverDefaults, toolPolicy string) (provider.DriverDefaults, string, error) {
	def, err := verifiedKernel(d)
	if err != nil {
		return provider.DriverDefaults{}, "", err
	}
	if err := refuseUnsupportedDefaults(def); err != nil {
		return provider.DriverDefaults{}, "", err
	}
	policy, err := provider.ResolveToolPolicy(toolPolicy)
	if err != nil {
		return provider.DriverDefaults{}, "", err
	}
	return def, policy, nil
}

// verifiedKernel reads and checks each non-empty kernel path field and
// moves its bytes into the matching text field.
func verifiedKernel(d provider.DriverDefaults) (provider.DriverDefaults, error) {
	if d.KernelPrompt != "" || d.SubagentKernelPrompt != "" {
		return provider.DriverDefaults{}, cascade.New(cascade.KindIntegrity,
			"claude: DriverDefaults.KernelPrompt/SubagentKernelPrompt are set only by the driver after verification")
	}
	if d.KernelPromptFile != "" {
		text, err := readKernel(d.KernelPromptFile)
		if err != nil {
			return provider.DriverDefaults{}, cascade.Wrapf(cascade.KindIntegrity, err,
				"claude: DriverDefaults.KernelPromptFile is not the verified daemon kernel")
		}
		d.KernelPrompt, d.KernelPromptFile = text, ""
	}
	if d.SubagentKernelPromptFile != "" {
		text, err := readKernel(d.SubagentKernelPromptFile)
		if err != nil {
			// Unverified, the value could only be the -file flag the
			// harness --help does not list, so the refusal also wraps
			// ErrHarnessIncompatible.
			return provider.DriverDefaults{}, cascade.Wrapf(cascade.KindIntegrity, provider.ErrHarnessIncompatible,
				"claude: DriverDefaults.SubagentKernelPromptFile is not the verified daemon kernel (%v); "+
					"the harness --help does not list --append-subagent-system-prompt-file", err)
		}
		d.SubagentKernelPrompt, d.SubagentKernelPromptFile = text, ""
	}
	return d, nil
}

// readKernel returns the bytes of the daemon kernel file at path when they
// hash to provider.PolicyKernelSHA256.
func readKernel(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		!strings.HasSuffix(filepath.ToSlash(path), daemonKernelSuffix) {
		return "", cascade.Newf(cascade.KindIntegrity, "claude: kernel path %q is not the daemon's policy kernel", path)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "claude: kernel file")
	}
	if !before.Mode().IsRegular() {
		return "", cascade.Newf(cascade.KindIntegrity, "claude: kernel %q is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "claude: opening the kernel file")
	}
	defer func() { _ = f.Close() }()
	return readKernelFD(f, before)
}

// readKernelFD checks the open descriptor is the regular file the Lstat
// saw, reads at most PolicyKernelMaxBytes+1 bytes and compares the hash of
// exactly those bytes.
func readKernelFD(f *os.File, before os.FileInfo) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "claude: kernel file stat")
	}
	if !st.Mode().IsRegular() || !os.SameFile(before, st) {
		return "", cascade.New(cascade.KindIntegrity, "claude: the kernel file changed between lstat and open")
	}
	b, err := io.ReadAll(io.LimitReader(f, provider.PolicyKernelMaxBytes+1))
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "claude: reading the kernel file")
	}
	if len(b) > provider.PolicyKernelMaxBytes {
		return "", cascade.Newf(cascade.KindIntegrity, "claude: the kernel file exceeds %d bytes", provider.PolicyKernelMaxBytes)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != provider.PolicyKernelSHA256 {
		return "", cascade.New(cascade.KindIntegrity, "claude: the kernel file's sha256 is not provider.PolicyKernelSHA256")
	}
	return string(b), nil
}

// kernelTextFlags emits the verified kernel text flags; never a -file flag.
func kernelTextFlags(d provider.DriverDefaults) []string {
	var out []string
	if d.KernelPrompt != "" {
		out = append(out, "--append-system-prompt", d.KernelPrompt)
	}
	if d.SubagentKernelPrompt != "" {
		out = append(out, "--append-subagent-system-prompt", d.SubagentKernelPrompt)
	}
	return out
}

// headlessArgv is buildArgv plus the --print rule for the subagent flag.
func headlessArgv(in argvInput) ([]string, error) {
	argv, err := buildArgv(in)
	if err != nil {
		return nil, err
	}
	if err := refuseSubagentWithoutPrint(argv); err != nil {
		return nil, err
	}
	return argv, nil
}

// refuseSubagentWithoutPrint refuses (KindInternal) an argv that carries
// --append-subagent-system-prompt without -p/--print, where the harness
// would ignore it. Only tokens before the literal "--" count.
func refuseSubagentWithoutPrint(argv []string) error {
	headless, subagent := false, false
	for _, a := range argv {
		if a == "--" {
			break
		}
		headless = headless || a == "-p" || a == "--print"
		subagent = subagent || a == "--append-subagent-system-prompt"
	}
	if subagent && !headless {
		return cascade.New(cascade.KindInternal, "claude: --append-subagent-system-prompt needs a --print argv")
	}
	return nil
}
