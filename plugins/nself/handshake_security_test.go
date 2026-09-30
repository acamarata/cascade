// Purpose (this file): regression tests for the S-103.T1 review's
//
//	handshake-side proofs: a credential-shaped project value is never
//	proposed or echoed, and `nself version --json` is decoded by its exact
//	key only ({"VERSION":"9.9.9"} must not clear the version floor).
//
// Constraints: no test here forks the real `nself` CLI.
// SPORT: plugins/nself handshake (TEST) — P1-E25-W5-S103-T1.

package nself

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestHandshakeWithholdsCredentialValues(t *testing.T) {
	dir := nselfProjectDir(t)
	script := fullScript()
	script.out["config get POSTGRES_HOST"] = []byte("postgres://admin:hunter2@db")
	origRunner := activeRunner
	activeRunner = script
	t.Cleanup(func() { activeRunner = origRunner })
	withGetenv(t, true, "")

	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	if _, ok := entryLiteral(entriesFromWire(resp.Proposed), "plugins."+handshakeOwner+".postgres_host"); ok {
		t.Fatal("a credential-shaped POSTGRES_HOST was proposed")
	}
	if len(resp.Withheld) != 1 || resp.Withheld[0] != "plugins."+handshakeOwner+".postgres_host" {
		t.Fatalf("withheld = %v", resp.Withheld)
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"hunter2", "admin:"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("response leaks %q: %s", leak, data)
		}
	}
}

func TestDecodeVersionInfoStrictKey(t *testing.T) {
	for _, in := range []string{`{"VERSION":"9.9.9"}`, `{"Version":"0"}`, `{"vErsion":"2.0.0"}`} {
		v, err := decodeVersionInfo([]byte(in))
		if err == nil && v.Version != "" {
			t.Fatalf("decodeVersionInfo(%s) = %q, want no version from a non-exact key", in, v.Version)
		}
	}
	v, err := decodeVersionInfo([]byte(`{"version":"1.3.5"}`))
	if err != nil || v.Version != "1.3.5" {
		t.Fatalf("decodeVersionInfo(exact key) = (%q, %v), want 1.3.5", v.Version, err)
	}
}

// TestHandshakeRecordsAbsoluteProjectDir: a relative --dir is recorded as an
// absolute path (the review saw `--dir crproj` recorded as "crproj").
func TestHandshakeRecordsAbsoluteProjectDir(t *testing.T) {
	dir := nselfProjectDir(t)
	t.Chdir(filepath.Dir(dir))
	origRunner := activeRunner
	activeRunner = fullScript()
	t.Cleanup(func() { activeRunner = origRunner })
	withGetenv(t, true, "")

	resp, err := runHandshake(context.Background(), handshakeModePropose, filepath.Base(dir))
	if err != nil {
		t.Fatalf("runHandshake(relative dir): %v", err)
	}
	got, _ := entryLiteral(entriesFromWire(resp.Proposed), "plugins."+handshakeOwner+".project_dir")
	if want := strconv.Quote(dir); got != want {
		t.Fatalf("project_dir = %s, want the absolute %s", got, want)
	}
}

// TestHandshakeMissingEnvCarriesDSNShape: pending-env names the missing
// env-refs and, when the Postgres DSN is one of them, the exact shape to
// export with every placeholder unfilled.
func TestHandshakeMissingEnvCarriesDSNShape(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner := activeRunner
	activeRunner = fullScript()
	t.Cleanup(func() { activeRunner = origRunner })
	withGetenv(t, true, "CASCADE_STORAGE_POSTGRES_DSN")

	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	const want = "postgres://<user>:<password>@<postgres_host>:<postgres_port>/<postgres_db>"
	if resp.Status != "pending-env" || resp.DSNShape != want {
		t.Fatalf("resp = %+v, want pending-env with dsn_shape %q", resp, want)
	}
	withGetenv(t, true, "CASCADE_STORAGE_S3_SECRET")
	resp, err = runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil || resp.DSNShape != "" {
		t.Fatalf("dsn_shape = %q (err %v), want none when the DSN env-ref is set", resp.DSNShape, err)
	}
}

// TestHandshakeResponseScrubbed: strings that reach the response from
// outside this package (here, an applier's outcome reason) pass through
// payload.go's scrub before the response is encoded.
func TestHandshakeResponseScrubbed(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner, origApplier := activeRunner, configApplier
	activeRunner = fullScript()
	configApplier = &recordingApplier{result: ConfigResult{
		Skipped: []ConfigOutcome{{Path: "plugins." + handshakeOwner + ".postgres_host", Reason: "password=hunter2"}},
	}}
	t.Cleanup(func() { activeRunner, configApplier = origRunner, origApplier })
	withGetenv(t, true, "")

	resp, err := runHandshake(context.Background(), handshakeModeApply, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0].Reason != redactedMarker {
		t.Fatalf("skipped = %+v, want the credential-shaped reason replaced by the scrub marker", resp.Skipped)
	}
}
