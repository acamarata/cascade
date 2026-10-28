// Purpose: `cascade doctor`'s admission_signal check. It names the resource
//
//	signal posture the daemon's admission controller would see on this
//	host (swap+memory, memory-only, no-signal), and says plainly that a
//	no-signal host refuses every admission request.
//
// Inputs: the platform (goos), the injected Clock, and a one-shot sample
//
//	function. Production samples locally with governor.SampleOnce over a
//	ticker of one sampler period; the check never contacts the daemon.
//
// Outputs: doctor.Check registered under "admission_signal" by
//
//	productionCheckRegistry (doctor_mounts.go).
//
// Constraints: one classifier. The sample is classified by
//
//	governor.ClassifySignal, the rule Admit applies, so doctor and the
//	daemon cannot disagree. A posture is reported OK only from a sample
//	that was actually taken: a sampling error, a missed deadline and a
//	stale or empty sample are errors. Windows runs tier-2 with no daemon
//	and no admission controller, so the check is skipped there (OK with
//	the reason, the precedent config-permissions sets) and never samples.
//
// SPORT: cmd/cascade doctor_mounts_fleet.go (ADD).
package main

import (
	"context"
	goruntime "runtime"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/runtime"
)

const (
	// admissionSignalCheckName is the check's registry and report name.
	admissionSignalCheckName = "admission_signal"
	// admissionNoSignalMessage is the exact text a no-signal host reports.
	admissionNoSignalMessage = "admission refuses every request on this host: no memory signal"
	// admissionWindowsSkipReason is why the check does not sample on Windows.
	admissionWindowsSkipReason = "no daemon admission on windows"
)

// admissionSampleFunc takes one host resource sample.
type admissionSampleFunc func() (governor.ResourceSnapshot, error)

// admissionSignalCheck implements doctor.Check for admission_signal.
// goos and sample are seams: tests construct it directly with a fixed
// platform and an injected one-shot sample.
type admissionSignalCheck struct {
	goos   string
	clock  runtime.Clock
	sample admissionSampleFunc
}

// newAdmissionSignalDoctorCheck builds the production check: it samples
// this host once per Run through governor.SampleOnce, one sampler period
// apart, with a fresh ticker each time.
func newAdmissionSignalDoctorCheck(clock runtime.Clock) doctor.Check {
	return admissionSignalCheck{
		goos:  goruntime.GOOS,
		clock: clock,
		sample: func() (governor.ResourceSnapshot, error) {
			return governor.SampleOnce(clock, runtime.NewSystemTicker(governor.PeriodForHz(0)))
		},
	}
}

func (c admissionSignalCheck) Name() string { return admissionSignalCheckName }

func (c admissionSignalCheck) Describe() string {
	return "reports the resource signal posture daemon admission decides on (swap+memory, memory-only, no-signal)"
}

func (c admissionSignalCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Run samples the host once and reports the posture. It returns as soon as
// ctx is done; a sample still in flight then finishes on its own within
// one sampler period and is discarded.
func (c admissionSignalCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.goos == "windows" {
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message: "skipped: " + admissionWindowsSkipReason, Detail: "not sampled"}, nil
	}
	if err := ctx.Err(); err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "context already done before the admission signal was sampled", Detail: err.Error()}, nil
	}
	if c.sample == nil || c.clock == nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "admission signal check has no sampler or clock wired; nothing was sampled"}, nil
	}
	type sampled struct {
		snap governor.ResourceSnapshot
		err  error
	}
	done := make(chan sampled, 1)
	go func() {
		snap, err := c.sample()
		done <- sampled{snap: snap, err: err}
	}()
	select {
	case <-ctx.Done():
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "admission signal sample did not finish before the check deadline", Detail: ctx.Err().Error()}, nil
	case s := <-done:
		return c.classify(s.snap, s.err), nil
	}
}

// classify turns one sample into the check's result through
// governor.ClassifySignal, never a rule of its own.
func (c admissionSignalCheck) classify(snap governor.ResourceSnapshot, err error) doctor.CheckResult {
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message:     "admission signal could not be sampled: " + err.Error(),
			Detail:      err.Error(),
			Remediation: "the daemon's admission controller refuses while its sampler cannot read host memory; fix the cause named above"}
	}
	posture := governor.ClassifySignal(snap, c.clock.Now(), governor.PeriodForHz(0))
	detail := "posture=" + string(posture)
	switch posture {
	case governor.PostureSwapAndMemory:
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message: "admission decides on memory and swap headroom", Detail: detail}
	case governor.PostureMemoryOnly:
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message: "admission decides on memory headroom only (no swap configured)", Detail: detail}
	case governor.PostureNoSignal:
		return doctor.CheckResult{Status: doctor.StatusError, Message: admissionNoSignalMessage, Detail: detail,
			Remediation: "the platform collector reported no memory total; admission stays closed until it does"}
	case governor.PostureStale:
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "admission signal sample is missing or stale; admission refuses every request", Detail: detail}
	}
	return doctor.CheckResult{Status: doctor.StatusError,
		Message: "admission signal posture is not one doctor knows; treating it as a refusal", Detail: detail}
}

// Fix is not implemented: the posture is a fact about the host.
func (c admissionSignalCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
