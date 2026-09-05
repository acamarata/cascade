package context

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the harness-agnostic generation seam. Owns the HarnessGenerator
//   interface every harness serializer implements, the HarnessFile it
//   produces, and the on-disk materialization policy that decides what
//   happens when the target file already exists.
// Inputs: a MergedContext (from MergeTiers) for generation; a target path
//   plus a WritePolicy for materialization.
// Outputs: []HarnessFile, or WriteResult values describing exactly what
//   happened to each file; typed cascade.Error otherwise.
// Constraints: 04-PEWS-PLAN-W1-W3.md Wave 2 Epic E S-08 T3. Generation is
//   pure: no clock, no filesystem, no network, no map iteration on the
//   output path. Materialization touches the filesystem and nothing else.
// SPORT: context-engine/harness-gen-interface (ADD, per T-3 sport_updates).

// Managed-block delimiters. The opening marker string is v1's verbatim
// (cascade-cli generate_instructions/cc.rs, tag archive/p9-integration), so
// a file written by v1 is recognised rather than duplicated. v2 appends a
// digest attribute to the opening marker; see digestAttr.
const (
	markerOpenPrefix = "<!-- cascade:generate-instructions"
	markerClose      = "<!-- /cascade:generate-instructions -->"
	digestAttr       = " digest=sha256:"
)

// HarnessFile is one generated instruction file: the bytes to write, the
// path they belong at relative to their tier's root directory, and the tier
// that produced them.
//
// Role is part of the type because Name alone does not identify the file: a
// five-tier render produces five files that all sit at the same relative
// path inside five different tier roots, and only the Role says which root.
// Dropping it would make the write path guess, and guessing which tier a
// generated instruction file belongs to is how a tier's instructions end up
// silently governing the wrong directory.
type HarnessFile struct {
	// Name is the file's path relative to its tier's root directory, in
	// slash form (for example ".claude/CLAUDE.md").
	Name string
	// Content is the exact bytes to write.
	Content []byte
	// Role is the tier whose root Name is relative to.
	Role TierRole
}

// HarnessGenerator renders a merged instruction cascade into the files one
// coding harness reads.
//
// Implementations must be deterministic: the same MergedContext must yield
// byte-identical HarnessFile content on every call, in the same order, with
// no timestamps and no dependence on process or filesystem state. A
// generator that cannot render some part of its input must say so inside
// the output it does produce rather than quietly emitting less, because a
// dropped instruction is one the user believes is in force and is not.
type HarnessGenerator interface {
	// Generate renders mc. It returns an empty slice and a nil error when
	// mc contributes nothing, and a typed cascade.Error when mc itself is
	// malformed.
	Generate(mc MergedContext) ([]HarnessFile, error)
}

// WritePolicy selects what WriteHarnessFile does when the target file's
// managed block has been edited by hand since it was generated.
//
// There is deliberately no silent-overwrite policy. Destroying an edit
// somebody made on purpose, without a word, is the worst of the available
// behaviours, so it is not one of the available behaviours.
type WritePolicy uint8

const (
	// RefuseIfEdited is the zero value and the default: a hand-edited
	// managed block stops the write and returns KindConflict naming the
	// file. The user's edit survives untouched.
	RefuseIfEdited WritePolicy = iota
	// BackupIfEdited copies the existing file to "<name>.cascade-bak"
	// before overwriting it. The result reports the backup path, so the
	// overwrite is announced rather than assumed.
	BackupIfEdited
)

// WriteAction says what WriteHarnessFile actually did.
type WriteAction uint8

const (
	// ActionUnchanged means the file already held exactly these bytes and
	// was not opened for writing.
	ActionUnchanged WriteAction = iota
	// ActionCreated means the file did not exist and was created.
	ActionCreated
	// ActionUpdated means an existing, unedited managed block was replaced.
	ActionUpdated
	// ActionAppended means the file existed with no managed block at all
	// (a hand-authored instruction file) and the block was appended to it.
	ActionAppended
	// ActionBackedUp means a hand-edited managed block was replaced under
	// BackupIfEdited, with the previous file preserved at BackupPath.
	ActionBackedUp
)

// WriteResult describes one materialized file.
type WriteResult struct {
	// Path is the file that was considered, whether or not it changed.
	Path string
	// Action is what happened to it.
	Action WriteAction
	// BackupPath is the preserved copy's path, set only for
	// ActionBackedUp.
	BackupPath string
}

// WriteHarnessFile materializes f at path under policy.
//
// # What counts as a hand edit
//
// The opening marker carries a digest of the managed block's body as it was
// written. On a later run the digest is recomputed from the bytes actually
// on disk: equal means nobody has touched the block since cascade wrote it,
// and it may be replaced; different means somebody has, and policy decides.
// Content OUTSIDE the markers is never read for this comparison and never
// rewritten, so a user's own prose around the block is always safe.
//
// A file with no marker at all is hand-authored and is appended to, never
// truncated. Writing identical bytes is skipped entirely, which is what
// makes repeated runs idempotent at the filesystem level as well as the
// byte level.
func WriteHarnessFile(path string, f HarnessFile, policy WritePolicy) (WriteResult, error) {
	res := WriteResult{Path: path}
	existing, err := os.ReadFile(path) //nolint:gosec // path is composed by the caller from discovered tier roots.
	switch {
	case os.IsNotExist(err):
		if err := writeFileAtomic(path, f.Content); err != nil {
			return WriteResult{}, err
		}
		res.Action = ActionCreated
		return res, nil
	case err != nil:
		return WriteResult{}, wrapTierFSErr(err, "read harness file "+path)
	}

	block := strings.TrimRight(string(f.Content), "\n")
	next, action, err := mergeManagedBlock(path, string(existing), block, policy)
	if err != nil {
		return WriteResult{}, err
	}
	if next == string(existing) {
		res.Action = ActionUnchanged
		return res, nil
	}
	if action == ActionBackedUp {
		res.BackupPath = path + ".cascade-bak"
		if err := writeFileAtomic(res.BackupPath, existing); err != nil {
			return WriteResult{}, err
		}
	}
	if err := writeFileAtomic(path, []byte(next)); err != nil {
		return WriteResult{}, err
	}
	res.Action = action
	return res, nil
}

// mergeManagedBlock splices block into existing and reports which action the
// splice amounts to. It is the whole of the clobber decision, kept separate
// from the filesystem so every branch is testable without a temp directory.
func mergeManagedBlock(path, existing, block string, policy WritePolicy) (string, WriteAction, error) {
	start, end, found := findManagedBlock(existing)
	if !found {
		sep := "\n\n"
		if existing == "" {
			sep = ""
		} else if strings.HasSuffix(existing, "\n\n") {
			sep = ""
		} else if strings.HasSuffix(existing, "\n") {
			sep = "\n"
		}
		return existing + sep + block + "\n", ActionAppended, nil
	}

	action := ActionUpdated
	if !managedBlockIntact(existing[start:end]) {
		switch policy {
		case BackupIfEdited:
			action = ActionBackedUp
		case RefuseIfEdited:
			return "", 0, handEditRefusal(path)
		default:
			// An unrecognised policy fails closed as a refusal. A
			// policy nobody defined must not be read as consent to
			// destroy an edit somebody made.
			return "", 0, handEditRefusal(path)
		}
	}
	return existing[:start] + block + existing[end:], action, nil
}

// handEditRefusal is the typed error returned when a managed block has been
// edited by hand and the policy in force does not allow overwriting it.
func handEditRefusal(path string) error {
	return cascade.Newf(cascade.KindConflict,
		"context: %s: the cascade-managed block has been edited by hand; refusing to overwrite it (back it up or remove the block to regenerate)", path)
}

// findManagedBlock locates the managed block's byte range in s, from the
// start of its opening marker line to the end of its closing marker line.
// A marker pair that is present but out of order is reported as not found,
// so a mangled file is appended to rather than spliced at a nonsense offset.
func findManagedBlock(s string) (start, end int, found bool) {
	start = strings.Index(s, markerOpenPrefix)
	if start < 0 {
		return 0, 0, false
	}
	rel := strings.Index(s[start:], markerClose)
	if rel < 0 {
		return 0, 0, false
	}
	return start, start + rel + len(markerClose), true
}

// managedBlockIntact reports whether block's recorded digest still matches
// its body. A block with no digest attribute predates the attribute (v1
// wrote one such marker) and is treated as intact: refusing to touch every
// v1-written file would strand exactly the users an upgrade has to serve.
func managedBlockIntact(block string) bool {
	line, body, ok := strings.Cut(block, "\n")
	if !ok {
		return false
	}
	idx := strings.Index(line, digestAttr)
	if idx < 0 {
		return true
	}
	recorded := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[idx+len(digestAttr):]), "-->"))
	return recorded == bodyDigest(body)
}

// bodyDigest is the managed block's content hash: SHA-256 of the body,
// hex-encoded. It exists to detect an edit, not to resist one, so the
// choice of hash is about stability across platforms, nothing more.
func bodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so an interrupted run leaves either the old file or the new
// one, never half of either.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return wrapTierFSErr(err, "create directory "+dir)
	}
	tmp, err := os.CreateTemp(dir, ".cascade-harness-*")
	if err != nil {
		return wrapTierFSErr(err, "create temporary file in "+dir)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return wrapTierFSErr(err, "write "+path)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return wrapTierFSErr(err, "close "+path)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return wrapTierFSErr(err, "set mode on "+path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return wrapTierFSErr(err, "replace "+path)
	}
	return nil
}
