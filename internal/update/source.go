package update

// Purpose: where releases come from: the GitHub releases base (or
//   CASCADE_UPDATE_BASE_URL), or a local goreleaser dist directory
//   (CASCADE_UPDATE_DIST, the test lane), and which tag is latest.
// Inputs: the two environment variables; a Fetcher.
// Outputs: a validated Source, file URLs under it, and the latest tag.
// Constraints: the base URL is https, or http only on 127.0.0.1, ::1 or
//   localhost; no credentials, query or fragment. A dist must be an
//   absolute directory. The latest tag comes from the releases/latest
//   redirect (base mode; its target must be on a pinned host or the base
//   URL host) or metadata.json's tag (dist mode), never from a file the
//   release itself names. A tag is "v" + a SemVer 2.0.0 version.
// SPORT: internal/update Source/ADDED, LatestStable/ADDED, DirFetcher/ADDED.

import (
	"context"
	"encoding/json"
	"io"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The environment variables this package reads.
const (
	EnvBaseURL = "CASCADE_UPDATE_BASE_URL"
	EnvDist    = "CASCADE_UPDATE_DIST"
)

// DefaultBaseURL is the public releases page.
const DefaultBaseURL = "https://github.com/acamarata/cascade/releases"

// maxMetadataBytes bounds metadata.json.
const maxMetadataBytes = 64 << 10

// tagRE is the release tag shape: "v" + a version; validTag then requires
// the version to be SemVer 2.0.0.
var tagRE = regexp.MustCompile(`^v[0-9]` + versionChars + `$`)

// validTag accepts exactly "v" + a SemVer 2.0.0 version.
func validTag(tag string) bool {
	if !tagRE.MatchString(tag) {
		return false
	}
	_, err := parseSemver(tag[1:])
	return err == nil
}

// Fetcher reads one release file. HTTPFetcher (transport.go) serves the
// base URL; DirFetcher serves CASCADE_UPDATE_DIST.
type Fetcher interface {
	Get(ctx context.Context, url string) (io.ReadCloser, error)
}

// Locator is the optional Fetcher half that reports a redirect target
// without following it (the releases/latest lookup).
type Locator interface {
	Locate(ctx context.Context, url string) (string, error)
}

// Source names where releases come from. Dist wins when set.
type Source struct {
	BaseURL string
	Dist    string
}

// SourceFromEnv builds and validates the Source from getenv.
func SourceFromEnv(getenv func(string) string) (Source, error) {
	s := Source{BaseURL: strings.TrimRight(getenv(EnvBaseURL), "/"), Dist: getenv(EnvDist)}
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	if _, err := baseURLHost(s.BaseURL); err != nil {
		return Source{}, err
	}
	if s.Dist != "" {
		info, err := os.Stat(s.Dist)
		if !filepath.IsAbs(s.Dist) || err != nil || !info.IsDir() {
			return Source{}, cascade.Newf(cascade.KindInvalidInput, "update: %s=%s is not an absolute directory", EnvDist, s.Dist)
		}
	}
	return s, nil
}

// BaseURLHost is the host:port of a non-default base URL, else "".
func (s Source) BaseURLHost() string {
	if s.BaseURL == "" || s.BaseURL == DefaultBaseURL {
		return ""
	}
	h, _ := baseURLHost(s.BaseURL)
	return h
}

// baseURLHost validates a base URL and returns its host:port.
func baseURLHost(raw string) (string, error) {
	u, err := neturl.Parse(raw)
	bad := func(why string) error {
		return cascade.Newf(cascade.KindInvalidInput, "update: %s %s", EnvBaseURL, why)
	}
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", bad("is not an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#") {
		return "", bad("must not carry credentials, a query or a fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return "", bad("must be https (http only on 127.0.0.1 or localhost)")
	}
	return strings.ToLower(u.Host), nil
}

func isLoopbackHost(h string) bool {
	return h == "127.0.0.1" || h == "::1" || strings.EqualFold(h, "localhost")
}

// fileURL is where release file name lives for tag.
func (s Source) fileURL(tag, name string) string {
	if s.Dist != "" {
		return name
	}
	return s.BaseURL + "/download/" + tag + "/" + name
}

// LatestStable returns the latest non-prerelease tag: the releases/latest
// redirect target in base mode, metadata.json's tag in dist mode.
func LatestStable(ctx context.Context, f Fetcher, s Source) (string, error) {
	var tag string
	if s.Dist != "" {
		rc, err := f.Get(ctx, "metadata.json")
		if err != nil {
			return "", err
		}
		data, err := readCapped(rc, maxMetadataBytes, "metadata.json")
		if err != nil {
			return "", err
		}
		var meta struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return "", cascade.New(cascade.KindIntegrity, "update: metadata.json is not valid JSON")
		}
		tag = meta.Tag
	} else {
		loc, ok := f.(Locator)
		if !ok {
			return "", cascade.New(cascade.KindInternal, "update: the fetcher cannot resolve releases/latest")
		}
		target, err := loc.Locate(ctx, s.BaseURL+"/latest")
		if err != nil {
			return "", err
		}
		if u, perr := neturl.Parse(target); perr != nil || u.User != nil || !(HTTPFetcher{BaseURL: s.BaseURL}).hostAllowed(u) {
			return "", cascade.New(cascade.KindPolicyDenied, "update: refusing a releases/latest redirect to an unpinned host")
		}
		tag = latestTagFrom(target)
	}
	if !validTag(tag) {
		return "", cascade.Newf(cascade.KindUnavailable, "update: cannot resolve the latest release tag (got %q)", tag)
	}
	return tag, nil
}

// latestTagFrom takes the tag from a ".../releases/tag/<tag>" target.
func latestTagFrom(target string) string {
	u, err := neturl.Parse(target)
	if err != nil {
		return ""
	}
	before, tag, ok := strings.Cut(u.Path, "/tag/")
	if !ok || before == "" || strings.Contains(tag, "/") {
		return ""
	}
	return tag
}

// readCapped reads at most limit bytes from rc, refusing anything longer,
// and closes it.
func readCapped(rc io.ReadCloser, limit int64, what string) ([]byte, error) {
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "update: read %s", what)
	}
	if int64(len(data)) > limit {
		return nil, integrity("%s is larger than %d bytes", what, limit)
	}
	return data, nil
}

// DirFetcher reads release files from a local dist directory. Names must
// be bare file names of regular files directly in Dir.
type DirFetcher struct{ Dir string }

// Get opens Dir/name.
func (d DirFetcher) Get(_ context.Context, name string) (io.ReadCloser, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || name == ".." {
		return nil, cascade.Newf(cascade.KindInvalidInput, "update: %q is not a bare file name", name)
	}
	p := filepath.Join(d.Dir, name)
	info, err := os.Lstat(p)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindNotFound, err, "update: %s is not in %s", name, d.Dir)
	}
	if !info.Mode().IsRegular() {
		return nil, integrity("%s in %s is not a regular file", name, d.Dir)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "update: open %s", p)
	}
	return f, nil
}
