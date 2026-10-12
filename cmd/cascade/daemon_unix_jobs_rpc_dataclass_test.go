//go:build !windows

package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestJobsRPCAdapterDataClassText(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CASCADE_HOME", root)
	_, s := setupJobsRPC(t)
	a := jobStoreAdapter{store: s}
	for _, tc := range []struct {
		word string
		tier provider.SensitivityTier
	}{
		{"public", provider.SensitivityPublic}, {"internal", provider.SensitivityInternal},
		{"confidential", provider.SensitivityRestricted}, {"secret", provider.SensitivityLocalOnly},
	} {
		j := jobs.Job{ID: tc.word, State: jobs.JobStatePending, ConsequenceClass: jobs.ConsequenceNormal, DataClass: new(tc.tier)}
		rec := jobRecordFrom(j)
		if rec.DataClass != tc.word {
			t.Fatalf("record = %q", rec.DataClass)
		}
		retry, err := a.RetryJob(context.Background(), rec, tc.word+"-retry", 1, 1)
		if err != nil || retry.DataClass != tc.word {
			t.Fatalf("retry = %+v: %v", retry, err)
		}
		got, ok, err := s.GetJob(context.Background(), retry.ID)
		if err != nil || !ok || got.DataClass == nil || *got.DataClass != tc.tier {
			t.Fatalf("stored = %+v: %v", got, err)
		}
		rec.DataClass = "local-only"
		if _, err := a.RetryJob(context.Background(), rec, tc.word+"-bad", 1, 1); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("invalid word: %v", err)
		}
		if _, ok, err := s.GetJob(context.Background(), tc.word+"-bad"); err != nil || ok {
			t.Fatalf("invalid row: %v, %v", ok, err)
		}
	}
}
