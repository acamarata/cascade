package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestOpenCodeEnvAllowlist(t *testing.T) {
	k := newKit(t)
	t.Setenv("OPENCODE_PARENT_CANARY", "host-only")
	s := k.spec()
	s.Env.Driver = append(s.Env.Driver, "UNLISTED=fixture")
	_, err := k.driver().Spawn(context.Background(), s)
	wantError(t, err, ErrEnvNotAllowed)
	if len(k.d.jobs) != 0 {
		t.Fatal("spawned despite invalid env")
	}
	if err := k.d.checkEnv(k.env); err != nil {
		t.Fatal(err)
	}
	_, c, err := k.run(k.spec())
	if err != nil || c.Output != "ready" {
		t.Fatalf("exact child env: %+v %v", c, err)
	}
}

func TestOpencodeDriverRejectsEmptySpawnEnv(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.Env.Driver = nil
	_, err := k.driver().Spawn(context.Background(), s)
	wantError(t, err, ErrEmptySpawnEnv)
}

func TestOpenCodeEntitlementFalseRefuses(t *testing.T) {
	k := newKit(t)
	k.cfg.Posture.ProgrammaticEntitlement = false
	_, err := k.driver().Spawn(context.Background(), k.spec())
	wantError(t, err, provider.ErrEntitlement)
}

func TestOpenCodeRefusesEmptyWorktreeRoot(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.WorktreeRoot = ""
	_, err := k.driver().Spawn(context.Background(), s)
	wantError(t, err, ErrEmptyWorktreeRoot)
}

func TestOpenCodeNilFilterRefuses(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.Filter = nil
	_, err := k.driver().Spawn(context.Background(), s)
	wantError(t, err, provider.ErrEgressSubstitutionFailed)
}

func TestOpenCodeProtocolPinRefusal(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	_, err := d.Negotiate(context.Background(), provider.ProtocolRange{Min: "9", Max: "9"})
	wantError(t, err, provider.ErrHarnessIncompatible)
	v, err := d.Negotiate(context.Background(), d.SupportedProtocols())
	if err != nil || v != ProtocolRevision {
		t.Fatal(v, err)
	}
}

func TestOpenCodeDataClassJoined(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.DataClass = provider.DataClassPublic
	s.Envelope.Sensitivity = provider.SensitivityRestricted
	r, c, err := k.run(s)
	if err != nil || c.DataClass != provider.DataClassRestricted {
		t.Fatalf("%+v %v", c, err)
	}
	e, _ := k.d.Events(r.JobID)
	for _, v := range e {
		if v.DataClass != provider.DataClassRestricted {
			t.Fatal("lowered event tier")
		}
	}
}

func TestOpenCodeDowngradeRejected(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.DataClass = provider.DataClassRestricted
	_, err := s.WithDataClass(provider.DataClassPublic)
	wantError(t, err, provider.ErrDataClassDowngrade)
}

func TestOpenCodePreSpawnScanAborts(t *testing.T) {
	k := newKit(t)
	k.cfg.ContentMatch = func(b []byte) (string, bool) { return "fixture", strings.Contains(string(b), "blocked") }
	if err := os.WriteFile(filepath.Join(k.root, "input"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := k.driver().Spawn(context.Background(), k.spec())
	wantError(t, err, provider.ErrPreSpawnSecretFound)
	if len(k.d.jobs) != 0 {
		t.Fatal("spawned despite secret scan")
	}
}

func TestOpenCodeCredentialRowRecorded(t *testing.T) {
	b, err := os.ReadFile("testdata/README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), KeyVarCompatKey) {
		t.Fatal("credential provenance missing")
	}
	k := newKit(t)
	s := k.spec()
	s.Env.Driver = s.Env.Driver[:2]
	_, err = k.driver().Spawn(context.Background(), s)
	wantError(t, err, ErrCredentialMissing)
}

func TestOpenCodeOutsideEnvelopeRefused(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.WorktreeRoot = t.TempDir()
	if _, err := k.driver().Spawn(context.Background(), s); err == nil {
		t.Fatal("outside root accepted")
	}
}
