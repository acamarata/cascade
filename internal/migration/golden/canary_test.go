package main

// Purpose: TestHarvest_CanaryNeverReachesFixture. One canary string is
// planted in every place an input can carry an unclassified value; three
// scratch input sets cover the places the importers accept and the two
// they refuse. No output byte, and no stdout or stderr byte, may hold the
// canary or the planted absolute path, while every redaction form appears.
// The canary is never printed by a failure message.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canary is the planted marker. It is a valid vault entry name, so it can
// sit in a vault name as well as a value.
const canary = "CANARY_ZQX7_LEAK"

// plantedPath is the absolute path planted in a mapped config value.
const plantedPath = "/Users/canary-home/run/daemon.sock"

// canaryAccounts is a schema-1 accounts input whose notes carry the canary;
// extra is spliced into the account object.
func canaryAccounts(extra string) string {
	return `{"schema_version":1,"updated_at":"2026-09-12T19:28:30+00:00","accounts":[{` +
		`"id":"acct-one","family":"openai","subscription":"pro","access_methods":["codex-cli"],` +
		`"role":"pooled","exhaustion_priority":5,"models":["gpt-5.5"],"cli_available":false,` +
		`"key_count":0,"notes":"` + canary + `"` + extra + `}],"model_matrix":[]}` + "\n"
}

// canaryCase is one scratch input set and the exit code it must produce.
type canaryCase struct {
	name  string
	files map[string]string
	want  int
}

func canaryCases() []canaryCase {
	return []canaryCase{
		{name: "accepted", want: exitOK, files: map[string]string{
			"vault/vault.env":        canary + "=" + canary + "\nOTHER_FIXTURE_KEY=REDACTED\n",
			"accounts/accounts.json": canaryAccounts(""),
			"config/known.toml":      "[daemon]\nsocket_path = \"" + plantedPath + "\"\n",
			"config/unknown.toml":    "[daemon]\nlog_level = \"info\"\n\n[org]\nnote = \"" + canary + "\"\n",
		}},
		{name: "accounts-unknown-field", want: exitRefused, files: map[string]string{
			"accounts/accounts.json": canaryAccounts(`,"` + canary + `":"` + canary + `"`),
		}},
		{name: "memory-unknown-frontmatter", want: exitRefused, files: map[string]string{
			"memory/feedback-canary.md": "---\nname: x\ndescription: y\ntype: feedback\n" + canary + ": " + canary + "\n---\nbody\n",
		}},
	}
}

func TestHarvest_CanaryNeverReachesFixture(t *testing.T) {
	var all bytes.Buffer
	for _, tc := range canaryCases() {
		root := newModule(t)
		for rel := range tc.files {
			if strings.HasPrefix(rel, "config/") {
				removeConfigInputs(t, root)
				break
			}
		}
		for rel, content := range tc.files {
			writeTestFile(t, filepath.Join(pinnedOf(root), filepath.FromSlash(rel)), []byte(content))
		}
		rewriteManifest(t, root)
		code, stdout, stderr := runHarvest(t)
		if code != tc.want {
			t.Fatalf("%s: exit %d, want %d", tc.name, code, tc.want)
		}
		all.WriteString(stdout + stderr)
		outputs := readOutputs(t, root)
		if tc.want == exitRefused && len(outputs) != 0 {
			t.Fatalf("%s: %d files written after a refusal", tc.name, len(outputs))
		}
		for _, file := range outputs {
			all.Write(file.data)
		}
	}
	needles := map[string]string{"canary": canary, "planted path": plantedPath, "planted home": "canary-home"}
	for label, needle := range needles {
		if bytes.Contains(all.Bytes(), []byte(needle)) {
			t.Errorf("the %s reached the output", label)
		}
	}
	for _, form := range []string{`"REDACTED"`, `"NAME-1"`, `"ACCOUNT-1"`, `"REDACTED-PATH"`} {
		if !bytes.Contains(all.Bytes(), []byte(form)) {
			t.Errorf("redaction form %s never appeared", form)
		}
	}
}

// removeConfigInputs deletes the committed config inputs so a case's own
// config files are the only ones harvested.
func removeConfigInputs(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"daemon-known.toml", "daemon-integration.toml"} {
		if err := os.RemoveAll(filepath.Join(pinnedOf(root), "config", name)); err != nil {
			t.Fatal(err)
		}
	}
}

// socketForms are daemon.socket values that are not absolute paths yet still
// name a location. Each carries the planted home marker; none may reach an
// output byte.
var socketForms = []string{
	`%USERPROFILE%\canary-home\d.sock`,
	`$HOME/canary-home/d.sock`,
	`Users/canary-home/d.sock`,
	`\canary-home\u\d.sock`,
}

// TestHarvest_SocketPathFormsNeverReachFixture plants each socketForms value
// in its own config input and a bare file name in one more: the forms are
// REDACTED-PATH, the bare name is the only socket value that survives.
func TestHarvest_SocketPathFormsNeverReachFixture(t *testing.T) {
	root := newModule(t)
	removeConfigInputs(t, root)
	for i, form := range append(append([]string(nil), socketForms...), "d.sock") {
		body := "[daemon]\nsocket_path = '" + form + "'\n"
		writeTestFile(t, filepath.Join(pinnedOf(root), "config", "form-"+string(rune('a'+i))+".toml"), []byte(body))
	}
	rewriteManifest(t, root)
	code, stdout, stderr := runHarvest(t)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var all bytes.Buffer
	all.WriteString(stdout + stderr)
	for _, file := range readOutputs(t, root) {
		all.Write(file.data)
	}
	if bytes.Contains(all.Bytes(), []byte("canary-home")) {
		t.Error("a path-shaped socket value reached the output")
	}
	if got := bytes.Count(all.Bytes(), []byte(`"daemon.socket": "REDACTED-PATH"`)); got != len(socketForms) {
		t.Errorf("%d socket values were REDACTED-PATH, want %d", got, len(socketForms))
	}
	if got := bytes.Count(all.Bytes(), []byte(`"daemon.socket": "d.sock"`)); got != 1 {
		t.Errorf("%d bare socket names survived, want 1", got)
	}
}
