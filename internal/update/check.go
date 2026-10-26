package update

// Purpose: decide what self-update would do, writing nothing and signalling
//   nothing: the channel, the installed files against the receipt, the
//   latest signed release and the downgrade rule.
// Inputs: Deps (channel env, running version, source, fetcher, platform,
//   trust root).
// Outputs: a CheckResult; Apply acts on the same plan.
// Constraints: brew and oci are no-ops, node-managed defers to the
//   controller (nodes.DeferSelfUpdate), manual has no receipt to act on.
//   For a script install every receipt file is digest-checked against
//   install_dir before any fetch ("install differs from receipt"). A dev
//   build refuses unless CASCADE_UPDATE_DIST is set. The signed version is
//   compared by semver precedence with the running version and with the
//   receipt's release tag: older than either refuses ("downgrade"), equal is
//   none, a signed archive digest equal to the receipt's is none, newer is
//   update. Compatibility is always null in this phase.
// SPORT: internal/update Check/ADDED, CheckResult/ADDED.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Action is what self-update does or would do.
type Action string

// The actions.
const (
	ActionNone            Action = "none"
	ActionUpdate          Action = "update"
	ActionNoopChannel     Action = "noop-channel"
	ActionDeferController Action = "defer-controller"
)

// Ident names one release: the installed one or the latest signed one.
type Ident struct {
	ReleaseTag    string `json:"release_tag"`
	ArchiveSHA256 string `json:"archive_sha256"`
	Version       string `json:"version"`
}

// CheckResult is the report-only answer (`self-update --check --json`).
type CheckResult struct {
	Channel       Channel          `json:"channel"`
	Reason        string           `json:"reason"`
	Action        Action           `json:"action"`
	Current       Ident            `json:"current"`
	Latest        Ident            `json:"latest"`
	Compatibility *json.RawMessage `json:"compatibility"`
	BaseURLHost   string           `json:"base_url_host,omitempty"`
}

// Deps carries every input Check and Apply use.
type Deps struct {
	Env       Env
	Version   string // the running buildinfo.Version
	Source    Source
	Fetcher   Fetcher
	Platform  Platform
	TrustRoot func() (minisign.PublicKey, error)
	DataDir   string // <dataDir>: update/previous and update/work-* live here
	Clock     runtime.Clock
	Daemon    DrainDeps
}

// plan is Check's answer plus what Apply needs to act on it.
type plan struct {
	res     CheckResult
	receipt Receipt
	rel     signedRelease
}

// Check reports what Apply would do. It writes nothing and signals nothing.
func Check(ctx context.Context, d Deps) (CheckResult, error) {
	p, err := makePlan(ctx, d)
	return p.res, err
}

func makePlan(ctx context.Context, d Deps) (plan, error) {
	ch, reason := ResolveChannel(d.Env)
	p := plan{res: CheckResult{Channel: ch, Reason: reason, BaseURLHost: d.Source.BaseURLHost(), Current: Ident{Version: d.Version}}}
	switch {
	case nodes.DeferSelfUpdate(string(ch)):
		p.res.Action, p.res.Reason = ActionDeferController, reason+"; "+nodes.ChannelDeferralError().Error()
		return p, nil
	case ch == ChannelBrew || ch == ChannelOCI:
		p.res.Action = ActionNoopChannel
		return p, nil
	case ch != ChannelScript:
		return p, manualPlan(&p, d.Env)
	}
	r, err := ReadReceipt(d.Env.receiptPath())
	if err != nil {
		return p, err
	}
	p.receipt = r
	p.res.Current.ReleaseTag, p.res.Current.ArchiveSHA256 = r.ReleaseTag, r.ArchiveSHA256
	if err := verifyInstall(r); err != nil {
		return p, err
	}
	return p, remotePlan(ctx, d, &p)
}

// manualPlan is a no-op unless a receipt exists that ResolveChannel could
// not use; that broken receipt is reported instead of hidden.
func manualPlan(p *plan, e Env) error {
	p.res.Action = ActionNoopChannel
	path := e.receiptPath()
	if path == "" {
		return nil
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err := ReadReceipt(path)
	return err
}

// remotePlan fetches the latest signed checksums and applies the downgrade rule.
func remotePlan(ctx context.Context, d Deps, p *plan) error {
	if d.Version == devVersion && d.Source.Dist == "" {
		return cascade.New(cascade.KindUnsupported,
			"update: a dev build does not self-update; install a release with install.sh ("+EnvDist+" enables the local test lane)")
	}
	if d.TrustRoot == nil || d.Fetcher == nil {
		return cascade.New(cascade.KindInternal, "update: check needs a trust root and a fetcher")
	}
	pub, err := d.TrustRoot()
	if err != nil {
		return err
	}
	tag, err := LatestStable(ctx, d.Fetcher, d.Source)
	if err != nil {
		return err
	}
	rel, err := fetchSigned(ctx, d.Fetcher, d.Source, tag, d.Platform, pub)
	if err != nil {
		return err
	}
	p.rel = rel
	p.res.Latest = Ident{ReleaseTag: tag, ArchiveSHA256: rel.Entry.SHA256, Version: rel.Version}
	cmp, err := downgradeRule(rel.Version, d.Version, p.receipt)
	switch {
	case err != nil:
		return err
	case cmp == 0, rel.Entry.SHA256 == p.receipt.ArchiveSHA256:
		p.res.Action = ActionNone
	default:
		p.res.Action = ActionUpdate
	}
	return nil
}

// downgradeRule compares signed with the running version and refuses a
// release older than it, or older than the version the receipt records as
// installed (another updater may have installed a newer one since this
// binary started). It returns the comparison with the running version,
// or 0 (nothing to do) when signed equals the installed version.
func downgradeRule(signed, running string, r Receipt) (int, error) {
	cmp, err := compareVersions(signed, running)
	if err != nil {
		return 0, err
	}
	if cmp < 0 {
		return 0, integrity("downgrade: the signed release %s is older than the running %s; nothing was changed", signed, running)
	}
	if !validTag(r.ReleaseTag) {
		return 0, integrity("downgrade: the installed release tag %q is not a version; nothing was changed", r.ReleaseTag)
	}
	c, cerr := compareVersions(signed, r.ReleaseTag[1:])
	if cerr != nil || c < 0 {
		return 0, integrity("downgrade: the signed release %s is older than the installed %s; nothing was changed", signed, r.ReleaseTag)
	}
	if c == 0 {
		return 0, nil
	}
	return cmp, nil
}

// verifyInstall digest-checks every receipt file in install_dir.
func verifyInstall(r Receipt) error {
	for _, f := range r.Files {
		path := filepath.Join(r.InstallDir, f.Name)
		got, err := fileDigest(path)
		if err != nil || got != f.SHA256 {
			return integrity("install differs from receipt: %s does not match the sha256 the receipt records; rerun install.sh", path)
		}
	}
	return nil
}

// fileDigest hashes a regular, non-symlink file.
func fileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", os.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
