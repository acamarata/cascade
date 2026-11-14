package main

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

// writeRoleTestFile writes <dataDir>/nodes/name at 0600, bypassing every
// backend's validation so legacy records can be planted.
func writeRoleTestFile(t *testing.T, dataDir, name, body string) {
	t.Helper()
	dir := filepath.Join(dataDir, "nodes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// completeRoleBinding is the binding a node holds after `cascade node bind`.
func completeRoleBinding() nodes.ControllerBinding {
	return nodes.ControllerBinding{
		Endpoint: "tcp://controller.invalid:7443", EnrollmentID: "enr_test", ControllerPubKeyB64: testControllerKeyB64,
	}
}

// doctorRoleJSON runs `doctor --json` over the controller_role check alone.
func doctorRoleJSON(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	out, _ := execRootDoctor(t, testDoctorDeps(t, controllerRoleCheck{paths: paths}), "--json", "doctor")
	if !strings.Contains(out, "controller_role") {
		t.Fatalf("doctor --json does not report controller_role:\n%s", out)
	}
	return out
}

// assertRoleError checks a doctor --json envelope reports controller_role
// as an error naming kind and the remedy verb.
func assertRoleError(t *testing.T, out, kind, remedy string) {
	t.Helper()
	for _, want := range []string{`"error"`, "(" + kind + ")", remedy} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor --json lacks %q for kind %s:\n%s", want, kind, out)
		}
	}
}

// TestDoctorControllerRoleConflict: both and none are errors naming the
// kind and the remedy; a designation or a complete binding is ok.
func TestDoctorControllerRoleConflict(t *testing.T) {
	t.Run("both", func(t *testing.T) {
		paths := doctorTestPaths(t)
		writeTestDesignation(t, paths.DataDir())
		writeTestBinding(t, paths.DataDir(), completeRoleBinding())
		assertRoleError(t, doctorRoleJSON(t, paths), "both", "cascade init")
	})
	t.Run("none", func(t *testing.T) {
		paths := doctorTestPaths(t)
		writeRoleTestFile(t, paths.DataDir(), "bound_once", "")
		assertRoleError(t, doctorRoleJSON(t, paths), "none", "cascade init")
	})
	t.Run("none on a pristine home warns bootstrap pending, never controller", func(t *testing.T) {
		res, err := controllerRoleCheck{paths: doctorTestPaths(t)}.Run(context.Background())
		if err != nil || res.Status != doctor.StatusWarn || strings.Contains(res.Message, string(nodes.RoleController)) {
			t.Fatalf("pristine home: %+v, %v; want a warn that grants no role", res, err)
		}
	})
	t.Run("controller and node are ok", func(t *testing.T) {
		ctrl := doctorTestPaths(t)
		writeTestDesignation(t, ctrl.DataDir())
		node := doctorTestPaths(t)
		writeTestBinding(t, node.DataDir(), completeRoleBinding())
		for want, paths := range map[nodes.Role]runtime.PathProvider{nodes.RoleController: ctrl, nodes.RoleNode: node} {
			res, err := controllerRoleCheck{paths: paths}.Run(context.Background())
			if err != nil || res.Status != doctor.StatusOK || !strings.Contains(res.Message, string(want)) {
				t.Errorf("%s home: %+v, %v; want ok naming the role", want, res, err)
			}
		}
	})
	t.Run("no path provider is an error, never controller", func(t *testing.T) {
		res, err := controllerRoleCheck{}.Run(context.Background())
		if err != nil || res.Status != doctor.StatusError {
			t.Fatalf("nil paths: %+v, %v; want error", res, err)
		}
	})
}

// TestOldNodeBindingWithoutKeyRemedy: a binding from before the
// controller key pin resolves binding-incomplete, BootstrapDesignation
// refuses to promote it, and doctor tells the operator to re-bind.
func TestOldNodeBindingWithoutKeyRemedy(t *testing.T) {
	paths := doctorTestPaths(t)
	dataDir := paths.DataDir()
	writeRoleTestFile(t, dataDir, "controller_binding.json", `{"endpoint":"tcp://controller.invalid:7443","enrollment_id":"enr_old"}`)
	binding := nodes.NewFileControllerBindingBackend(dataDir)
	if _, conflict := nodes.RoleStatus(binding); conflict == nil || conflict.Kind != "binding-incomplete" {
		t.Fatalf("RoleStatus = %+v, want binding-incomplete", conflict)
	}
	self, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	des := nodes.NewFileDesignationBackend(dataDir)
	clock := runtime.NewFixedClock(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	_, wrote, err := nodes.BootstrapDesignation(context.Background(), des, binding, dataDir, self, rand.Reader, clock)
	if wrote || err == nil {
		t.Fatalf("BootstrapDesignation on an old binding: wrote=%v err=%v; want a refusal", wrote, err)
	}
	if _, ok, _ := des.Load(); ok {
		t.Fatal("a designation was written next to an old binding")
	}
	assertRoleError(t, doctorRoleJSON(t, paths), "binding-incomplete", "cascade node bind")
}
