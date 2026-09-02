// Purpose: `cascade config edit` CLI tests — split out of config_test.go
//   per R-14.117/Art.10.3 (300-line file cap): the edit verb needs its
//   own fake-$EDITOR harness (fakeEditorScript/newTestRootWithEnv),
//   which is a cohesive, separable seam from the rest of the command
//   tree's tests.
package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

func fakeEditorScript(t *testing.T, appendLine string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-editor.sh")
	content := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(appendLine) + " >> \"$1\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestConfigCLI_Edit_ValidSaveApplies(t *testing.T) {
	dir := t.TempDir()
	editor := fakeEditorScript(t, `[logging]`+"\n"+`level = "debug"`)
	root, _, _ := newTestRootWithEnv(t, dir, map[string]string{"EDITOR": editor})

	if _, _, err := run(t, root, "config", "edit"); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `level = "debug"`) {
		t.Fatalf("got:\n%s", data)
	}
}

func TestConfigCLI_Edit_InvalidSaveNotApplied(t *testing.T) {
	dir := t.TempDir()
	editor := fakeEditorScript(t, "not valid toml {{{")
	root, _, _ := newTestRootWithEnv(t, dir, map[string]string{"EDITOR": editor})

	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[logging]\nlevel = \"info\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, root, "config", "edit"); err == nil {
		t.Fatal("expected edit to fail validation")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[logging]\nlevel = \"info\"\n" {
		t.Fatalf("disk changed on invalid edit:\n%s", data)
	}
}

// newTestRootWithEnv is newTestRoot plus an injected extra-env map (for
// EDITOR), since Deps.Getenv only special-cases CASCADE_HOME by default.
func newTestRootWithEnv(t *testing.T, homeDir string, env map[string]string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{Use: "cascade"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().String("profile", "", "")
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().BoolP("quiet", "q", false, "")
	root.PersistentFlags().BoolP("verbose", "v", false, "")
	root.PersistentFlags().Bool("no-color", false, "")

	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return homeDir
		}
		if v, ok := env[k]; ok {
			return v
		}
		return ""
	}
	paths, err := runtime.NewPathProvider(getenv, func() (string, error) { return homeDir, nil })
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Paths:   paths,
		Getenv:  getenv,
		Clock:   runtime.NewFixedClock(time.Unix(0, 0)),
		Environ: func() []string { return nil },
	}
	root.AddCommand(NewConfigCmd(deps))

	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	return root, &out, &errOut
}
