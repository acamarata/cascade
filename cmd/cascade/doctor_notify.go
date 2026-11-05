// Purpose: read notification drop counters through the daemon client.
// Constraints: no inbox copy, notification contents, or embedded fallback.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// notifyDropsCheck keeps the RPC caller injectable so unit tests never dial.
type notifyDropsCheck struct {
	call func(context.Context, string, any, any) error
}

// Name identifies the mounted check.
func (notifyDropsCheck) Name() string { return "notify-drops" }

// Describe describes the counter lifetime.
func (notifyDropsCheck) Describe() string { return "notification drops since daemon start" }

// Metadata declares a read-only check.
func (notifyDropsCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

// Fix refuses automatic changes to the notification queues.
func (notifyDropsCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run reads only notify.stats. Only a missing or refused socket means down;
// RPC refusals, deadlines and malformed results are failed checks.
func (c notifyDropsCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.call == nil {
		return notifyStatsFailure("caller unavailable"), nil
	}
	var raw json.RawMessage
	if err := c.call(ctx, notify.StatsMethod, nil, &raw); err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return doctor.CheckResult{Status: doctor.Status("skip"), Message: "daemon not running"}, nil
		}
		kind, _ := cascade.KindOf(err)
		return notifyStatsFailure(fmt.Sprintf("RPC error (%s, %T)", kind, err)), nil
	}
	// This is the RPC wire result, not a second notification store. Pointers
	// distinguish absent fields from a valid snapshot with zero counters.
	var stats struct {
		Dropped     map[string]uint64 `json:"dropped"`
		Accumulated *int              `json:"accumulated"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stats); err != nil {
		return notifyStatsFailure(fmt.Sprintf("decode error (%T)", err)), nil
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return notifyStatsFailure("decode error: trailing data"), nil
	}
	if stats.Dropped == nil || stats.Accumulated == nil || *stats.Accumulated < 0 {
		return notifyStatsFailure("decode error: missing or invalid counters"), nil
	}
	var reasons []string
	for reason, count := range stats.Dropped {
		if count > 0 {
			// Only the two contract reasons may enter diagnostic output.
			if reason != "queue-full" && reason != "accumulation-full" {
				return notifyStatsFailure("decode error: unknown drop reason"), nil
			}
			reasons = append(reasons, fmt.Sprintf("%s=%d", reason, count))
		}
	}
	if len(reasons) == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "no notifications dropped"}, nil
	}
	sort.Strings(reasons)
	return doctor.CheckResult{Status: doctor.StatusWarn, Message: "notifications dropped: " + strings.Join(reasons, ", ")}, nil
}

func notifyStatsFailure(reason string) doctor.CheckResult {
	return doctor.CheckResult{Status: doctor.StatusError, Message: "notify.stats: " + reason}
}

// newNotifyDropsDoctorCheck resolves the socket lazily; construction does no I/O.
func newNotifyDropsDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return notifyDropsCheck{call: func(ctx context.Context, method string, params, out any) error {
		if method != notify.StatsMethod { // the one method this check calls; a named constant keeps the rpc method gate resolvable
			return cascade.Newf(cascade.KindInternal, "doctor: the notify-drops check calls only %s, not %q", notify.StatsMethod, method)
		}
		deps := statusDeps{Paths: paths, Getenv: os.Getenv, Environ: os.Environ, DialContext: client.UnixDialer}
		settings, err := resolveStatusSocket(ctx, deps)
		if err != nil {
			return err
		}
		return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), statusDialTimeout).Do(ctx, notify.StatsMethod, params, out)
	}}
}
