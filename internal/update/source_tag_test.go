package update

import (
	"context"
	"io"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedLocator answers every releases/latest lookup with target.
type fixedLocator struct{ target string }

func (fixedLocator) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, cascade.New(cascade.KindInternal, "not served")
}

func (l fixedLocator) Locate(context.Context, string) (string, error) { return l.target, nil }

// TestLatestStableRestrictsTagAndHost: the releases/latest target must be
// on a pinned host (or the base URL host) and name a "v" + SemVer tag.
func TestLatestStableRestrictsTagAndHost(t *testing.T) {
	gh := Source{BaseURL: DefaultBaseURL}
	mirror := Source{BaseURL: "https://mirror.example/r"}
	for _, tc := range []struct {
		src    Source
		target string
		want   string
	}{
		{gh, "https://github.com/acamarata/cascade/releases/tag/v2.0.1", "v2.0.1"},
		{gh, "https://github.com/acamarata/cascade/releases/tag/v2.0.1-rc.1", "v2.0.1-rc.1"},
		{mirror, "https://mirror.example/r/tag/v2.0.1", "v2.0.1"},
	} {
		got, err := LatestStable(context.Background(), fixedLocator{tc.target}, tc.src)
		if err != nil || got != tc.want {
			t.Fatalf("latest via %s = %q, %v; want %q", tc.target, got, err, tc.want)
		}
	}
	for _, target := range []string{
		"https://evil.example/acamarata/cascade/releases/tag/v2.0.1",
		"http://github.com/acamarata/cascade/releases/tag/v2.0.1",
		"https://user@github.com/acamarata/cascade/releases/tag/v2.0.1",
		"https://mirror.example/r/tag/v2.0.1",
		"%zz",
	} {
		_, err := LatestStable(context.Background(), fixedLocator{target}, gh)
		wantKind(t, err, cascade.KindPolicyDenied, "releases/latest redirect to an unpinned host")
	}
	for _, tag := range []string{"..", "2.0.1", "v2.0", "vv2.0.1", "v2.0.1-rc.01", "v.2.0.1", "v2.0.1+", "v01.0.0"} {
		_, err := LatestStable(context.Background(), fixedLocator{"https://github.com/acamarata/cascade/releases/tag/" + tag}, gh)
		wantKind(t, err, cascade.KindUnavailable, "cannot resolve the latest release tag")
	}
}
