// Package build (this file): the doctor mount gate's own tests. One leg
// asserts the real tree, three assert the predicates against seeded
// fixtures so a green real-tree run means the gate works rather than that
// it found nothing.
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorChecksAreMounted is the gate: every constructor returning a
// doctor.Check is registered in the CLI's productionCheckRegistry, or
// carries an exemption naming the precondition that retires it.
func TestDoctorChecksAreMounted(t *testing.T) {
	root := archModuleRoot(t)
	constructors, err := ScanDoctorCheckConstructors(root)
	if err != nil {
		t.Fatalf("scanning for doctor.Check constructors: %v", err)
	}
	if len(constructors) == 0 {
		t.Fatal("no doctor.Check constructor was found at all; the scan is not looking where the checks are")
	}
	body, err := ReadDoctorRegistryBody(root)
	if err != nil {
		t.Fatalf("reading the doctor composition root: %v", err)
	}
	unmounted := UnmountedDoctorChecks(constructors, body, DoctorMountExemptions)
	if len(unmounted) > 0 {
		names := make([]string, 0, len(unmounted))
		for _, c := range unmounted {
			names = append(names, c.String())
		}
		t.Fatalf("these doctor.Check constructors have no production registration: %s\n"+
			"register each in productionCheckRegistry, or add it to DoctorMountExemptions with the reason "+
			"and the named change that mounts it. A check nothing registers is a diagnostic no user can run.",
			strings.Join(names, ", "))
	}
}

// TestDoctorMountExemptionsAreLive asserts the exemption list carries no
// entry for a constructor that is gone or already mounted.
func TestDoctorMountExemptionsAreLive(t *testing.T) {
	root := archModuleRoot(t)
	constructors, err := ScanDoctorCheckConstructors(root)
	if err != nil {
		t.Fatalf("scanning for doctor.Check constructors: %v", err)
	}
	body, err := ReadDoctorRegistryBody(root)
	if err != nil {
		t.Fatalf("reading the doctor composition root: %v", err)
	}
	if stale := StaleDoctorMountExemptions(constructors, body, DoctorMountExemptions); len(stale) > 0 {
		t.Fatalf("stale doctor mount exemptions: %s", strings.Join(stale, ", "))
	}
	for name, reason := range DoctorMountExemptions {
		if len(strings.TrimSpace(reason)) < 40 {
			t.Fatalf("the exemption for %s carries no usable reason", name)
		}
	}
}

// TestDoctorMountGateDetectsAnUnmountedCheck is the seeded-violation
// half: a constructor absent from the registry body must be reported.
func TestDoctorMountGateDetectsAnUnmountedCheck(t *testing.T) {
	constructors := []DoctorCheckConstructor{
		{Name: "NewMountedCheck", Dir: filepath.Join("internal", "doctor")},
		{Name: "NewForgottenCheck", Dir: filepath.Join("internal", "doctor")},
	}
	body := "{ reg.Register(NewMountedCheck(deps)) }"
	unmounted := UnmountedDoctorChecks(constructors, body, nil)
	if len(unmounted) != 1 || unmounted[0].Name != "NewForgottenCheck" {
		t.Fatalf("UnmountedDoctorChecks = %v, want exactly NewForgottenCheck", unmounted)
	}
	if got := unmounted[0].String(); got != filepath.Join("internal", "doctor")+".NewForgottenCheck" {
		t.Fatalf("String() = %q", got)
	}
	// An exemption silences it, and only it.
	if left := UnmountedDoctorChecks(constructors, body, map[string]string{"NewForgottenCheck": "why"}); len(left) != 0 {
		t.Fatalf("an exempted constructor was still reported: %v", left)
	}
}

// TestDoctorMountGateScansRealSignatures drives the parser over a fixture
// carrying both result forms and one function that returns neither.
func TestDoctorMountGateScansRealSignatures(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "fixture")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the fixture package: %v", err)
	}
	src := "package fixture\n\n" +
		"type Check interface{ Name() string }\n" +
		"func NewInPackageCheck() Check { return nil }\n" +
		"func NewQualifiedCheck() doctor.Check { return nil }\n" +
		"func NewNotACheck() error { return nil }\n" +
		"func newUnexportedCheck() Check { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	// A _test.go sibling must be ignored: a constructor a test calls is
	// still unmounted, and one a test DECLARES is not shipped at all.
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"),
		[]byte("package fixture\n\nfunc NewTestOnlyCheck() Check { return nil }\n"), 0o600); err != nil {
		t.Fatalf("writing the test fixture: %v", err)
	}
	found, err := ScanDoctorCheckConstructors(root)
	if err != nil {
		t.Fatalf("ScanDoctorCheckConstructors: %v", err)
	}
	got := map[string]bool{}
	for _, c := range found {
		got[c.Name] = true
	}
	for _, want := range []string{"NewInPackageCheck", "NewQualifiedCheck"} {
		if !got[want] {
			t.Fatalf("the scan missed %s; found %v", want, got)
		}
	}
	for _, unwanted := range []string{"NewNotACheck", "newUnexportedCheck", "NewTestOnlyCheck"} {
		if got[unwanted] {
			t.Fatalf("the scan wrongly reported %s", unwanted)
		}
	}
}

// TestDoctorRegistryBodyFailsClosed asserts the gate errors rather than
// reporting a clean tree when the mounting point is not where it expects.
func TestDoctorRegistryBodyFailsClosed(t *testing.T) {
	if _, err := ReadDoctorRegistryBody(t.TempDir()); err == nil {
		t.Fatal("ReadDoctorRegistryBody accepted a tree with no doctor command file")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "cmd", "cascade")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the fixture command dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doctor_mounts.go"),
		[]byte("package main\n\nfunc somethingElse() {}\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	_, err := ReadDoctorRegistryBody(root)
	if err == nil || !strings.Contains(err.Error(), "mounting point moved") {
		t.Fatalf("ReadDoctorRegistryBody = %v, want a moved-mounting-point refusal", err)
	}
}
