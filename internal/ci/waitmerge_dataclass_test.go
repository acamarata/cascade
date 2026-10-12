package ci

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestMergeOnGreen_SecretAndConfidentialDeniedUnderInternalCeiling(t *testing.T) {
	for _, tier := range []provider.SensitivityTier{provider.SensitivityLocalOnly, provider.SensitivityRestricted, provider.SensitivityInternal, provider.SensitivityPublic} {
		f, opts := grantedFixture(t)
		opts.DataClass = new(tier)
		f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
		_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
		if tier == provider.SensitivityLocalOnly || tier == provider.SensitivityRestricted {
			if err != policy.ErrDataClassDenied || err.Error() != policy.ErrDataClassDenied.Error() || len(f.caller.calls) != 0 {
				t.Fatalf("deny = %v, calls = %d", err, len(f.caller.calls))
			}
		} else if err != nil || len(f.caller.calls) != 1 {
			t.Fatalf("pass = %v, calls = %d", err, len(f.caller.calls))
		}
	}
}

func TestUnsetDataClassFailsClosed(t *testing.T) {
	if got := (MergeOptions{}).dataClass(); got == nil || *got != provider.SensitivityLocalOnly {
		t.Fatalf("unset = %v", got)
	}
	f, opts := grantedFixture(t)
	opts.DataClass = nil
	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if err != policy.ErrDataClassDenied || err.Error() != policy.ErrDataClassDenied.Error() || len(f.caller.calls) != 0 {
		t.Fatalf("unset merge = %v", err)
	}
	got := (MergeOptions{DataClass: new(provider.SensitivityRestricted)}).dataClass()
	if got == nil || *got != provider.SensitivityRestricted {
		t.Fatal("restricted class changed")
	}
}
