package review

import (
	"context"
	"reflect"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the seam --pr fetches a pull-request diff through.
// plugins/review may not import internal/** (Art.10.2), so the host
// (internal/plugins/review_prdiff_wiring.go) installs the real fetcher at
// init. Until it does, every --pr refuses typed; there is no other path.
//
// SPORT: plugins/review:prdiff (ADD) — P1-PLG-05.

// PRDiffFetcher fetches one pull request's unified diff.
type PRDiffFetcher interface {
	FetchPRDiff(ctx context.Context, owner, repo string, number int) ([]byte, error)
}

// prDiffFetcher is the installed fetcher; the default refuses.
var prDiffFetcher PRDiffFetcher = unwiredPRDiffFetcher{}

// SetPRDiffFetcher installs f. A nil f (or a typed nil inside the
// interface) is KindInvalidInput and the current fetcher is kept.
func SetPRDiffFetcher(f PRDiffFetcher) error {
	if isNilFetcher(f) {
		return cascade.New(cascade.KindInvalidInput, "cascade review: SetPRDiffFetcher needs a non-nil fetcher")
	}
	prDiffFetcher = f
	return nil
}

// isNilFetcher also catches a typed nil pointer, which would otherwise
// pass the == nil check and panic on the first --pr.
func isNilFetcher(f PRDiffFetcher) bool {
	if f == nil {
		return true
	}
	v := reflect.ValueOf(f)
	switch v.Kind() { //nolint:exhaustive // only nil-able kinds can hold a typed nil; every other kind is non-nil
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// unwiredPRDiffFetcher is the default: a build without the host wiring.
type unwiredPRDiffFetcher struct{}

func (unwiredPRDiffFetcher) FetchPRDiff(context.Context, string, string, int) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "PR diff fetch is not wired in this build")
}
