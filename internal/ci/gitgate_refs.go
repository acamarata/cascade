package ci

// Purpose: parse pre-receive stdin and refuse ref deletion, non-fast-forward
// updates and tag moves before any tip is handed to verify.
// Inputs: the pre-receive "<old> <new> <ref>" lines and the receiving repo.
// Outputs: parsed ref updates and a push decision with a cascade kind.
// Constraints: fast-forward is decided by a real git merge-base, never by
// string comparison; quarantine object variables are kept for git.
// SPORT: contract:receive-gate ref rules (P1-CI-05a).

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrForcePushRefused is returned when a push would rewind or rewrite a ref:
// a non-fast-forward branch update or a move of an existing tag.
var ErrForcePushRefused = errors.New("git gate: force push refused")

// ErrRefDeletionRefused is returned when a push would delete a ref.
var ErrRefDeletionRefused = errors.New("git gate: ref deletion refused")

// PreReceiveRef is one ref update as git hands it to a pre-receive hook.
type PreReceiveRef struct{ Old, New, Ref string }

const preReceiveMaxLine = 8 * 1024

// ParsePreReceiveStdin parses pre-receive input, one "<old> <new> <ref>" line
// per ref with single spaces. Any malformed line, a duplicate ref or a line
// over 8 KiB is KindInvalidInput; empty input is an empty slice.
func ParsePreReceiveStdin(r io.Reader) ([]PreReceiveRef, error) {
	if r == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "git gate: nil pre-receive input")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), preReceiveMaxLine+1)
	sc.Split(splitNewline)
	refs := make([]PreReceiveRef, 0)
	seen := make(map[string]bool)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > preReceiveMaxLine {
			return nil, cascade.New(cascade.KindInvalidInput, "git gate: pre-receive line too long")
		}
		parts := strings.Split(line, " ")
		if len(parts) != 3 {
			return nil, cascade.New(cascade.KindInvalidInput, "git gate: malformed pre-receive line")
		}
		ref := PreReceiveRef{Old: parts[0], New: parts[1], Ref: parts[2]}
		if err := validatePreReceiveRef(ref); err != nil {
			return nil, err
		}
		if seen[ref.Ref] {
			return nil, cascade.New(cascade.KindInvalidInput, "git gate: duplicate ref in pre-receive input")
		}
		seen[ref.Ref] = true
		refs = append(refs, ref)
	}
	if err := sc.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "git gate: read pre-receive input")
	}
	return refs, nil
}

// CheckPush applies the ref rules to each update in order and stops at the
// first error: deletions and non-fast-forward updates are refused, an
// existing tag may not move, and every new tip is passed to verify as its
// peeled commit, even when the object already exists in the repository.
func CheckPush(ctx context.Context, repo string, refs []PreReceiveRef, verify func(commit string) error) error {
	if verify == nil {
		return cascade.New(cascade.KindPermissionDenied, "git gate: no verifier")
	}
	for _, ref := range refs {
		if err := checkPushRef(ctx, repo, ref, verify); err != nil {
			return err
		}
	}
	return nil
}

func checkPushRef(ctx context.Context, repo string, ref PreReceiveRef, verify func(commit string) error) error {
	if err := validatePreReceiveRef(ref); err != nil {
		return err
	}
	if allZeroID(ref.New) {
		return cascade.Wrap(cascade.KindPermissionDenied, ErrRefDeletionRefused, "git gate")
	}
	out, err := gateGit(ctx, repo, "rev-parse", "--verify", "--quiet", ref.New+"^{commit}")
	commit := strings.TrimSpace(string(out))
	if err != nil || !branchObjectID(commit) {
		return cascade.Newf(cascade.KindPermissionDenied, "git gate: %s: new tip is not a commit", ref.Ref)
	}
	if !allZeroID(ref.Old) {
		if strings.HasPrefix(ref.Ref, "refs/tags/") && ref.Old != ref.New {
			return cascade.Wrap(cascade.KindPermissionDenied, ErrForcePushRefused, "git gate")
		}
		if _, err := gateGit(ctx, repo, "merge-base", "--is-ancestor", ref.Old, ref.New); err != nil {
			return cascade.Wrap(cascade.KindPermissionDenied, ErrForcePushRefused, "git gate")
		}
	}
	if err := verify(commit); err != nil {
		return fmt.Errorf("git gate: %s: %w", ref.Ref, err)
	}
	return nil
}

func validatePreReceiveRef(ref PreReceiveRef) error {
	if !branchObjectID(ref.Old) || !branchObjectID(ref.New) {
		return cascade.New(cascade.KindInvalidInput, "git gate: malformed object id")
	}
	if len(ref.Old) != len(ref.New) {
		return cascade.New(cascade.KindInvalidInput, "git gate: mixed object id lengths")
	}
	if !validGateRefName(ref.Ref) {
		return cascade.New(cascade.KindInvalidInput, "git gate: malformed ref name")
	}
	return nil
}

func validGateRefName(name string) bool {
	if !strings.HasPrefix(name, "refs/") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for _, c := range name {
		if c <= ' ' || c == 0x7f || strings.ContainsRune(`~^:?*[\`, c) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}

// splitNewline splits on '\n' only, so a '\r' stays in the line and fails
// the ref rules instead of being silently dropped.
func splitNewline(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func allZeroID(id string) bool {
	return id != "" && strings.Trim(id, "0") == ""
}

func gateGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects"}, args...)...)
	cmd.Dir = repo
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_DIR=") && !strings.HasPrefix(v, "GIT_WORK_TREE=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	return cmd.Output()
}
