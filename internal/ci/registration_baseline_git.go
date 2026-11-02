package ci

// Purpose: read baseline selection facts from local git metadata.
// Inputs: repository path and branch configuration.
// Outputs: upstream config, symbolic HEAD and expanded remote URLs.
// Constraints: errors fail closed and URL expansion contacts no remote.
// SPORT: baseline git fact readers.

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

func gitUpstreamConfig(ctx context.Context, root string) ([]byte, error) {
	out, err := baselineGit(ctx, root, "config", "--null", "--get-regexp", `^(branch\..+\.(remote|merge)|remote\..+\.url)$`)
	if err != nil && (!gitExitOne(err) || len(out) != 0) {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: read upstream config")
	}
	return out, nil
}

func gitCheckedOutBranch(ctx context.Context, root string) (string, bool, error) {
	out, err := baselineGit(ctx, root, "symbolic-ref", "-q", "HEAD")
	if gitExitOne(err) && len(out) == 0 {
		return "", true, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "ci: read checked-out branch")
	}
	ref := strings.TrimSpace(string(out))
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) == len("refs/heads/") {
		return "", false, cascade.New(cascade.KindIntegrity, "ci: malformed checked-out branch")
	}
	return ref, false, nil
}

func gitEffectiveURL(ctx context.Context, root, remote string) (string, error) {
	out, err := baselineGit(ctx, root, "ls-remote", "--get-url", remote)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: resolve effective remote URL")
	}
	u := strings.TrimSpace(string(out))
	if u == "" {
		return "", cascade.New(cascade.KindUnavailable, "ci: empty effective remote URL")
	}
	return u, nil
}

func gitExitOne(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}
