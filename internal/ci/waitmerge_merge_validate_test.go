// Purpose: MergeOptions/MergeDeps input validation -- every required field
// refused one at a time, and the fully-populated value accepted. Split from
// waitmerge_merge_test.go under Art.10.3's 300-line cap.
//
// SPORT: internal.ci.MergeOptions/TESTED, internal.ci.MergeDeps/TESTED
//
//	(P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestMergeOptions_Validate(t *testing.T) {
	valid := baseMergeOptions()
	cases := []MergeOptions{
		{},
		func() MergeOptions { o := valid; o.Owner = ""; return o }(),
		func() MergeOptions { o := valid; o.PR = 0; return o }(),
		func() MergeOptions { o := valid; o.Ref = ""; return o }(),
		func() MergeOptions { o := valid; o.Subject = policy.Subject{}; return o }(),
	}
	for i, o := range cases {
		if err := o.validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("case %d (%+v): err = %v, want KindInvalidInput", i, o, err)
		}
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("a fully-populated MergeOptions: err = %v, want nil", err)
	}
}

func TestMergeDeps_Validate(t *testing.T) {
	full := MergeDeps{
		Engine: &policy.Engine{}, Caller: &fakeMergeCaller{}, Audit: audit.New(nil, nil, nil),
		HeadSHA: func(context.Context, string, string, string) (string, error) { return "", nil },
	}
	cases := []MergeDeps{
		{},
		func() MergeDeps { d := full; d.Engine = nil; return d }(),
		func() MergeDeps { d := full; d.Caller = nil; return d }(),
		func() MergeDeps { d := full; d.HeadSHA = nil; return d }(),
		func() MergeDeps { d := full; d.Audit = nil; return d }(),
	}
	for i, d := range cases {
		if err := d.validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("case %d: err = %v, want KindInvalidInput", i, err)
		}
	}
	if err := full.validate(); err != nil {
		t.Fatalf("a fully-populated MergeDeps: err = %v, want nil", err)
	}
}
