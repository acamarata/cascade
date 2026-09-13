// Purpose: unit coverage for resolveKeyPassphrase's §5.8 branches
//
//	(--passphrase-env success/empty, CASCADE_NO_INPUT structured refusal,
//	and the interactive prompt's success/empty-input paths), which
//	shipped with no direct test of its own.
//
// SPORT: cmd.cascade.backup-key/TEST (P1-E19-W4-S42-T6).
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResolveKeyPassphrase_EnvVarSuccess(t *testing.T) {
	deps := backupDeps{Getenv: func(k string) string {
		if k == "TEST_PASS" {
			return "hunter2"
		}
		return ""
	}}
	got, err := resolveKeyPassphrase(&cobra.Command{}, deps, "TEST_PASS")
	if err != nil {
		t.Fatalf("resolveKeyPassphrase: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("resolveKeyPassphrase = %q, want \"hunter2\"", got)
	}
}

func TestResolveKeyPassphrase_EnvVarEmptyRefuses(t *testing.T) {
	deps := backupDeps{Getenv: func(string) string { return "" }}
	_, err := resolveKeyPassphrase(&cobra.Command{}, deps, "TEST_PASS")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("resolveKeyPassphrase(empty env) = %v, want KindInvalidInput", err)
	}
}

func TestResolveKeyPassphrase_NoInputRefusesWithoutPrompting(t *testing.T) {
	deps := backupDeps{Getenv: func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetErr(&out)
	_, err := resolveKeyPassphrase(cmd, deps, "")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("resolveKeyPassphrase(CASCADE_NO_INPUT=1) = %v, want KindElevationRequired", err)
	}
	if out.Len() != 0 {
		t.Errorf("resolveKeyPassphrase wrote a prompt despite CASCADE_NO_INPUT=1: %q", out.String())
	}
}

func TestResolveKeyPassphrase_InteractivePromptSuccess(t *testing.T) {
	deps := backupDeps{Getenv: func(string) string { return "" }}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("hunter2\n"))
	got, err := resolveKeyPassphrase(cmd, deps, "")
	if err != nil {
		t.Fatalf("resolveKeyPassphrase: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("resolveKeyPassphrase = %q, want \"hunter2\"", got)
	}
	if !strings.Contains(out.String(), "passphrase") {
		t.Errorf("resolveKeyPassphrase did not write the prompt: %q", out.String())
	}
}

func TestResolveKeyPassphrase_InteractiveEmptyInputRefuses(t *testing.T) {
	deps := backupDeps{Getenv: func(string) string { return "" }}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("\n"))
	_, err := resolveKeyPassphrase(cmd, deps, "")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("resolveKeyPassphrase(empty input) = %v, want KindInvalidInput", err)
	}
}
