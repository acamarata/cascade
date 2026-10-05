package migration

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCustodyGuard_RefusesResolvedKeychain(t *testing.T) {
	var calls [][]string
	runner := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte("/fixture/login.keychain-db"), nil
	}
	if err := custodyPreflightFor("darwin", sealedEnv(t), runner); err == nil {
		t.Fatal("resolved keychain accepted")
	}
	if !reflect.DeepEqual(calls, [][]string{{"/usr/bin/security", "default-keychain", "-d", "user"}}) {
		t.Fatalf("unexpected custody calls: %v", calls)
	}
}

func TestCustodyGuard_RefusesReachableSessionBus(t *testing.T) {
	env := sealedEnv(t)
	if err := os.WriteFile(filepath.Join(epicEnv(env, "XDG_RUNTIME_DIR"), "bus"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := custodyPreflightFor("linux", env, nil); err == nil {
		t.Fatal("runtime bus accepted")
	}
	env = sealedEnv(t)
	for i := range env {
		if strings.HasPrefix(env[i], "DBUS_SESSION_BUS_ADDRESS=") {
			env[i] = "DBUS_SESSION_BUS_ADDRESS=unix:path=/fixture/bus"
		}
	}
	if err := custodyPreflightFor("linux", env, nil); err == nil {
		t.Fatal("non-disabled bus address accepted")
	}
}

func TestCustodyGuard_AcceptsSealedEnv(t *testing.T) {
	env := sealedEnv(t)
	if err := custodyPreflightFor("linux", env, nil); err != nil {
		t.Fatal(err)
	}
	// A portable child exit supplies a real nonzero ProcessState to the fake.
	cmd := exec.Command(os.Args[0], "-test.run=^TestEpicZGuardExitHelper$")
	cmd.Env = append(env, "MIGRATION_GUARD_EXIT=1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper did not fail")
	}
	if err := custodyPreflightFor("darwin", env, func(string, ...string) ([]byte, error) { return nil, err }); err != nil {
		t.Fatal(err)
	}
}

func TestCustodyGuard_RefusesEmptyRuntimeDir(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint("present=", present), func(t *testing.T) {
			var env []string
			for _, item := range sealedEnv(t) {
				if !strings.HasPrefix(item, "XDG_RUNTIME_DIR=") {
					env = append(env, item)
				}
			}
			if present {
				env = append(env, "XDG_RUNTIME_DIR=")
			}
			if err := custodyPreflightFor("linux", env, nil); err == nil {
				t.Fatal("empty runtime directory accepted")
			}
		})
	}
}

func TestEpicZGuardExitHelper(_ *testing.T) {
	if os.Getenv("MIGRATION_GUARD_EXIT") == "1" {
		os.Exit(1)
	}
}

func TestEpicZMigration_GuardFailureRunsNoStep(t *testing.T) {
	want := errors.New("custody reachable")
	var steps []string
	err := runEpicZ(sealedEnv(t), func([]string) error { return want }, []func() error{
		func() error { steps = append(steps, "dry run"); return nil },
		func() error { steps = append(steps, "full run"); return nil },
	})
	if err != want || len(steps) != 0 {
		t.Fatalf("err=%v steps=%v; want guard error and zero steps", err, steps)
	}
}

func TestEpicZEnv_SealsHome(t *testing.T) {
	env := sealedEnv(t)
	base := filepath.Dir(epicEnv(env, "HOME"))
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "TMPDIR"} {
		value := epicEnv(env, key)
		if !strings.HasPrefix(value, base+string(filepath.Separator)) {
			t.Errorf("%s escapes temporary home: %q", key, value)
		}
	}
	if epicEnv(env, "DBUS_SESSION_BUS_ADDRESS") != "disabled:" {
		t.Fatal("bus not disabled")
	}
	if home := os.Getenv("HOME"); home != "" && strings.Contains(strings.Join(env, "\n"), home) {
		t.Fatal("real HOME appears in child environment")
	}
}

func TestEpicZDoctorOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		rc           int
		wantError    bool
	}{
		{"completion-gate-hooks", "warn", 5, false},
		{"hook-events", "warn", 5, false},
		{"provider_health", "warn", 0, false},
		{"subsystem_census", "warn", 5, false},
		{"retrieval_index", "ok", 0, false},
		{"unknown", "warn", 5, true},
		{"hook-events", "fail", 5, true},
		{"hook-events", "error", 5, true},
		{"hook-events", "unknown", 0, true},
		{"hook-events", "ok", 5, true},
		{"hook-events", "warn", 13, true},
	} {
		t.Run(tc.name+tc.status+fmt.Sprint(tc.rc), func(t *testing.T) {
			data := fmt.Appendf(nil, `{"data":{"entries":[{"name":%q,"result":{"status":%q}},{"name":"secrets/keychain-reachable","result":{"status":"ok","message":"custody backend file-vault"}}]}}`, tc.name, tc.status)
			if err := epicDoctorReport(data, tc.rc); (err != nil) != tc.wantError {
				t.Fatalf("report error=%v wantError=%t", err, tc.wantError)
			}
		})
	}
	for _, data := range []string{`{}`, `{"data":{"entries":[]}}`, `invalid`} {
		if err := epicDoctorReport([]byte(data), 0); err == nil {
			t.Fatalf("empty or invalid report accepted: %s", data)
		}
	}
	testEpicZDoctorCustody(t)
}

func testEpicZDoctorCustody(t *testing.T) {
	t.Helper()
	for _, tc := range []struct{ name, entry string }{
		{"missing-custody", `{"name":"retrieval_index","result":{"status":"ok"}}`},
		{"platform-custody", `{"name":"secrets/keychain-reachable","result":{"status":"ok","message":"custody backend keychain"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := fmt.Appendf(nil, `{"data":{"entries":[%s]}}`, tc.entry)
			if err := epicDoctorReport(data, 0); err == nil {
				t.Fatal("doctor accepted report without file-vault custody")
			}
		})
	}
}
