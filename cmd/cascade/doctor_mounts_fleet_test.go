package main

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// admissionTestNow is the injected clock's instant for these tests.
var admissionTestNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// admissionCheckOver builds the check over a canned one-shot sample, as if
// on goos.
func admissionCheckOver(goos string, snap governor.ResourceSnapshot, err error) admissionSignalCheck {
	return admissionSignalCheck{
		goos:   goos,
		clock:  runtime.NewFixedClock(admissionTestNow),
		sample: func() (governor.ResourceSnapshot, error) { return snap, err },
	}
}

// fresh returns a snapshot sampled at the injected clock's now.
func fresh(memTotal, swapTotal uint64) governor.ResourceSnapshot {
	return governor.ResourceSnapshot{MemTotalBytes: memTotal, MemUsedBytes: memTotal / 4,
		SwapTotalBytes: swapTotal, SampledAt: admissionTestNow}
}

// TestAdmissionSignalDoctorCheck: over an injected one-shot sample the
// check reports each posture through governor.ClassifySignal, reports a
// no-signal host and every sampling failure as an error, and never reports
// OK without a sample that was actually taken.
func TestAdmissionSignalDoctorCheck(t *testing.T) {
	sampleErr := cascade.New(cascade.KindUnavailable, "governor: synthetic sysctl failure")
	cases := []struct {
		name       string
		snap       governor.ResourceSnapshot
		err        error
		wantStatus doctor.Status
		wantMsg    string // exact when wantExact, else a substring
		wantExact  bool
		wantDetail string
	}{
		{name: "swap and memory", snap: fresh(16<<30, 2<<30),
			wantStatus: doctor.StatusOK, wantDetail: "posture=swap+memory"},
		{name: "memory only", snap: fresh(16<<30, 0),
			wantStatus: doctor.StatusOK, wantDetail: "posture=memory-only"},
		{name: "no signal", snap: fresh(0, 2<<30), wantStatus: doctor.StatusError,
			wantMsg: "admission refuses every request on this host: no memory signal", wantExact: true,
			wantDetail: "posture=no-signal"},
		{name: "sampling error names the cause", err: sampleErr, wantStatus: doctor.StatusError,
			wantMsg: "governor: synthetic sysctl failure"},
		{name: "sampling error with a snapshot is still an error", snap: fresh(16<<30, 2<<30), err: sampleErr,
			wantStatus: doctor.StatusError, wantMsg: "governor: synthetic sysctl failure"},
		{name: "empty sample is not ok", wantStatus: doctor.StatusError,
			wantMsg: "missing or stale", wantDetail: "posture=stale"},
		{name: "old sample is not ok", snap: governor.ResourceSnapshot{MemTotalBytes: 16 << 30, SwapTotalBytes: 1 << 30,
			SampledAt: admissionTestNow.Add(-time.Hour)},
			wantStatus: doctor.StatusError, wantMsg: "missing or stale", wantDetail: "posture=stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runCheck(t, admissionCheckOver("linux", tc.snap, tc.err))
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q (%+v), want %q", res.Status, res, tc.wantStatus)
			}
			if tc.wantExact && res.Message != tc.wantMsg {
				t.Errorf("message = %q, want exactly %q", res.Message, tc.wantMsg)
			}
			if !tc.wantExact && !strings.Contains(res.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", res.Message, tc.wantMsg)
			}
			if tc.wantDetail != "" && res.Detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", res.Detail, tc.wantDetail)
			}
		})
	}
}

// TestAdmissionSignalDoctorCheckSkipsWindows: on windows the check reports
// skipped with its reason and never samples, so it carries no posture.
func TestAdmissionSignalDoctorCheckSkipsWindows(t *testing.T) {
	check := admissionSignalCheck{goos: "windows", clock: runtime.NewFixedClock(admissionTestNow),
		sample: func() (governor.ResourceSnapshot, error) {
			t.Error("the windows check sampled the host")
			return governor.ResourceSnapshot{}, nil
		}}
	res := runCheck(t, check)
	if res.Status != doctor.StatusOK || res.Message != "skipped: no daemon admission on windows" {
		t.Fatalf("windows = %+v, want OK skipped: no daemon admission on windows", res)
	}
	if strings.Contains(res.Detail, "posture=") {
		t.Fatalf("windows detail %q names a posture without a sample", res.Detail)
	}
}

// TestAdmissionSignalDoctorCheckProductionWindows is the GOOS-gated leg: on
// a windows host the production constructor reports the skip.
func TestAdmissionSignalDoctorCheckProductionWindows(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("windows-only: the production check skips there")
	}
	res := runCheck(t, newAdmissionSignalDoctorCheck(runtime.NewFixedClock(admissionTestNow)))
	if res.Status != doctor.StatusOK || res.Message != "skipped: "+admissionWindowsSkipReason {
		t.Fatalf("production check on windows = %+v, want the skip", res)
	}
}

// TestAdmissionSignalDoctorCheckProductionSample drives the production
// constructor over this host's real collector and real one-period ticker:
// a host doctor runs on has a memory signal, so the check reports OK with
// a posture, which a constructor that sampled nothing could not produce.
func TestAdmissionSignalDoctorCheckProductionSample(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows skips sampling; TestAdmissionSignalDoctorCheckProductionWindows covers it")
	}
	// A clock far past the real one: a sample stamped by any clock other
	// than the injected one is decades older than its now, so classifies
	// stale and the check errors.
	future := runtime.NewFixedClock(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	res := runCheck(t, newAdmissionSignalDoctorCheck(future))
	if res.Status != doctor.StatusOK || (res.Detail != "posture=swap+memory" && res.Detail != "posture=memory-only") {
		t.Fatalf("production check = %+v, want OK with a swap+memory or memory-only posture", res)
	}
}

// TestAdmissionSignalDoctorCheckDeadline: a sample that outlives the
// check's context is an error, not a wait past the deadline.
func TestAdmissionSignalDoctorCheckDeadline(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	check := admissionSignalCheck{goos: "linux", clock: runtime.NewFixedClock(admissionTestNow),
		sample: func() (governor.ResourceSnapshot, error) {
			close(started)
			<-release
			return fresh(16<<30, 2<<30), nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	resCh := make(chan doctor.CheckResult, 1)
	go func() {
		res, _ := check.Run(ctx)
		resCh <- res
	}()
	<-started // the sample is in flight and will not finish until release
	cancel()
	res := <-resCh
	if res.Status != doctor.StatusError || !strings.Contains(res.Message, "did not finish before the check deadline") {
		t.Fatalf("cancelled run = %+v, want an error naming the deadline", res)
	}
}

// TestAdmissionSignalDoctorCheckDoneContext: a context already done before
// Run is an error and nothing is sampled.
func TestAdmissionSignalDoctorCheckDoneContext(t *testing.T) {
	check := admissionSignalCheck{goos: "linux", clock: runtime.NewFixedClock(admissionTestNow),
		sample: func() (governor.ResourceSnapshot, error) {
			t.Error("sampled under a context that was already done")
			return fresh(16<<30, 2<<30), nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := check.Run(ctx)
	if err != nil || res.Status != doctor.StatusError || !strings.Contains(res.Message, "context already done") {
		t.Fatalf("done-context run = (%+v, %v), want an error naming the done context", res, err)
	}
}

// TestAdmissionSignalDoctorCheckUnwired: a check with no sampler reports an
// error, never OK.
func TestAdmissionSignalDoctorCheckUnwired(t *testing.T) {
	res := runCheck(t, admissionSignalCheck{goos: "linux", clock: runtime.NewFixedClock(admissionTestNow)})
	if res.Status != doctor.StatusError {
		t.Fatalf("unwired = %+v, want StatusError", res)
	}
	if _, err := (admissionSignalCheck{}).Fix(context.Background()); err != doctor.ErrCheckNotFixable { //nolint:errorlint // sentinel identity
		t.Fatalf("Fix = %v, want ErrCheckNotFixable", err)
	}
}

// TestDoctorRegistersAdmissionSignal drives the REAL productionCheckRegistry
// and asserts admission_signal is mounted on it.
func TestDoctorRegistersAdmissionSignal(t *testing.T) {
	useTempCustody(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	for _, check := range reg.List() {
		if check.Name() == admissionSignalCheckName {
			if check.Metadata().Fixable {
				t.Error("admission_signal declares itself fixable")
			}
			return
		}
	}
	t.Fatal("admission_signal is not registered on the real productionCheckRegistry")
}
