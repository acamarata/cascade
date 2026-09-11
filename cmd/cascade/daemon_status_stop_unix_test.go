//go:build !windows

// Purpose: split from daemon_test.go (Windows parity pass 3):
//
//	`cascade daemon status --json`'s envelope shape and `daemon stop`'s
//	idempotency against nothing running, on the platforms with a real
//	daemon. The Windows mirror lives in daemon_status_stop_windows_test.go.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates), windows-parity-pass-3.
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDaemonStatusCmd_NotRunning_JSONEnvelope(t *testing.T) {
	home := t.TempDir()
	out, err := execDaemon(t, home, "daemon", "status", "--json")
	if err != nil {
		t.Fatalf("daemon status: %v (%s)", err, out)
	}

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Running     bool    `json:"running"`
			PID         int     `json:"pid"`
			UptimeS     float64 `json:"uptime_s"`
			Connections int     `json:"connections"`
			Detail      string  `json:"detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, out)
	}
	if envelope.Data.Running {
		t.Errorf("fresh CASCADE_HOME reports a running daemon: %+v", envelope.Data)
	}
}

func TestDaemonStopCmd_NothingRunning_IsIdempotent(t *testing.T) {
	home := t.TempDir()
	out, err := execDaemon(t, home, "daemon", "stop")
	if err != nil {
		t.Fatalf("daemon stop against nothing running: %v (%s)", err, out)
	}
}
