//go:build !windows

// Purpose: tests for the production codesign wrapper of the helper check:
//
//	team parsing, the requirement strings (team and identifier pinned), the
//	exact codesign argv, and the hardened-runtime and entitlement rules, all
//	through an injected runner (no real codesign signature is needed).
//
// SPORT: internal/elevation helper codesign tests/ADDED (P1-SHIP-15).
package elevation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTeamIdentifier(t *testing.T) {
	ok := "Identifier=cascade\nSignature size=9000\nAuthority=Developer ID Application: X\nTeamIdentifier=" + teamA + "\n"
	if got, err := parseTeamIdentifier(ok); err != nil || got != teamA {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, out := range map[string]string{
		"ad-hoc":    "Signature=adhoc\nTeamIdentifier=not set\n",
		"not set":   "TeamIdentifier=not set\n",
		"empty":     "",
		"injection": "TeamIdentifier=AB\" or true\n",
		"lowercase": "TeamIdentifier=abcde12345\n",
		"adhoc+id":  "Signature=adhoc\nTeamIdentifier=" + teamA + "\n",
	} {
		if team, err := parseTeamIdentifier(out); err == nil {
			t.Errorf("%s: accepted team %q", name, team)
		}
	}
}

func TestHelperRequirementPinsDeveloperIDTeamAndIdentifier(t *testing.T) {
	for name, c := range map[string]struct {
		fn func(string) (string, error)
		id string
	}{"helper": {HelperRequirement, HelperIdentifier}, "caller": {CallerRequirement, CallerIdentifier}} {
		req, err := c.fn(teamA)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"anchor apple generic", "certificate 1[field.1.2.840.113635.100.6.2.6]", "certificate leaf[field.1.2.840.113635.100.6.1.13]",
			`certificate leaf[subject.OU] = "` + teamA + `"`, `and identifier "` + c.id + `"`,
			`and !(entitlement["com.apple.security.cs.allow-dyld-environment-variables"] exists)`,
			`and !(entitlement["com.apple.security.cs.disable-library-validation"] exists)`,
			`and !(entitlement["com.apple.security.get-task-allow"] exists)`,
			`and !(entitlement["com.apple.security.cs.debugger"] exists)`} {
			if !strings.Contains(req, want) {
				t.Errorf("%s requirement lacks %q: %s", name, want, req)
			}
		}
		for _, bad := range []string{"", "abcde12345", "ABCDE1234", `ABCDE12345" or anchor apple`, "ABCDE123456"} {
			if _, err := c.fn(bad); err == nil {
				t.Errorf("%s accepted team %q", name, bad)
			}
		}
	}
	if h, _ := HelperRequirement(teamA); strings.Contains(h, `identifier "`+CallerIdentifier+`"`) {
		t.Error("the helper requirement would accept a binary signed as the caller")
	}
}

const cdHash = "0123456789abcdef0123456789abcdef01234567"

const runtimeFlags = "CodeDirectory v=20500 size=900 flags=0x10000(runtime) hashes=20+7 location=embedded\nCDHash=" + cdHash + "\nTeamIdentifier=" + teamA + "\n"

// codesignScript answers the codesign calls by their first argument.
type codesignScript struct {
	calls   [][]string
	dv, ent string
	fail    error  // returned by every call when set
	running string // when set, --verify passes only for a requirement pinned to this code hash
}

func (c *codesignScript) run(_ context.Context, args ...string) ([]byte, error) {
	c.calls = append(c.calls, args)
	switch args[0] {
	case "--verify":
		if c.running != "" && !strings.Contains(strings.Join(args, " "), `cdhash H"`+c.running+`"`) {
			return nil, errSig
		}
		return nil, c.fail
	case "-dvvv":
		return []byte(c.dv), c.fail
	}
	return nil, c.fail
}

func (c *codesignScript) verifier() codesignVerifier {
	return codesignVerifier{pid: func() int { return 99 }, run: c.run}
}

func TestCodesignVerifierCommands(t *testing.T) {
	c := &codesignScript{dv: runtimeFlags, ent: "<plist><dict><key>keychain-access-groups</key></dict></plist>"}
	v := c.verifier()
	if team, err := v.SelfTeam(); err != nil || team != teamA {
		t.Fatalf("SelfTeam = %q, %v", team, err)
	}
	if len(c.calls) != 2 || c.calls[0][0] != "-dvvv" || c.calls[1][0] != "--verify" ||
		c.calls[0][len(c.calls[0])-1] != "99" || c.calls[1][len(c.calls[1])-1] != "99" ||
		c.calls[1][3] != `=cdhash H"`+cdHash+`"` {
		t.Fatalf("SelfTeam must read the running process's signature (pid 99) first and verify it pinned to that code hash last: %v", c.calls)
	}
	wantReq, _ := HelperRequirement(teamA)
	c.calls = nil
	if err := v.VerifyPath("/x/helper", teamA); err != nil {
		t.Fatal(err)
	}
	if err := v.VerifyPID(4242, teamA); err != nil {
		t.Fatal(err)
	}
	pidCalls := c.calls[len(c.calls)-2:]
	pidVerify := pidCalls[1]
	if pidCalls[0][0] != "-dvvv" || strings.Join(pidCalls[0], " ") != "-dvvv 4242" || pidVerify[0] != "--verify" || len(pidVerify) != 5 ||
		pidVerify[1] != "--strict" || pidVerify[2] != "-R" || pidVerify[3] != "="+wantReq+` and cdhash H"`+cdHash+`"` || pidVerify[4] != "4242" {
		t.Fatalf("pid checks must read the flag and hash in one display, then verify pinned to that hash LAST: %v", pidCalls)
	}
	if err := v.VerifyPID(0, teamA); err == nil {
		t.Fatal("pid 0 accepted")
	}
	if err := v.VerifyPath("/x/helper", "bad"); err == nil {
		t.Fatal("malformed team accepted")
	}
	c.fail = errSig
	if _, err := v.SelfTeam(); err == nil {
		t.Fatal("codesign failure accepted as a team")
	}
	if err := v.VerifyPath("/x/helper", teamA); err == nil {
		t.Fatal("codesign failure accepted as a pass")
	}
	if err := v.VerifyPID(7, teamA); err == nil {
		t.Fatal("codesign failure accepted for a pid")
	}
}

func TestCodesignVerifierRequiresTheHardenedRuntime(t *testing.T) {
	cases := map[string]struct {
		dv, ent string
		ok      bool
	}{
		"runtime and clean entitlements": {runtimeFlags, "", true},
		"no runtime flag":                {"CodeDirectory v=20400 size=900 flags=0x0(none) hashes=20+7\nCDHash=" + cdHash + "\n", "", false},
		"ad-hoc linker flags":            {"CodeDirectory v=20400 size=900 flags=0x20002(adhoc,linker-signed) hashes=20+7\nCDHash=" + cdHash + "\n", "", false},
		"no flags line":                  {"TeamIdentifier=" + teamA + "\nCDHash=" + cdHash + "\n", "", false},
		"no code hash":                   {"CodeDirectory v=20500 size=900 flags=0x10000(runtime) hashes=20+7\n", "", false},
		"malformed code hash":            {"CodeDirectory v=20500 size=900 flags=0x10000(runtime)\nCDHash=xyz\" or true\n", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &codesignScript{dv: tc.dv, ent: tc.ent}
			v := c.verifier()
			for _, err := range []error{v.VerifyPath("/x/helper", teamA), v.VerifyPID(4242, teamA)} {
				if (err == nil) != tc.ok {
					t.Fatalf("err = %v, want ok=%v", err, tc.ok)
				}
			}
		})
	}
}

func TestExecCodesignFailsClosedAndBoundsOutput(t *testing.T) {
	if _, err := execCodesign(context.Background(), "-dv", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("codesign of a missing file reported success")
	}
	var b limitedBuffer
	big := make([]byte, codesignMaxOutput+100)
	if n, _ := b.Write(big); n != len(big) || len(b.Bytes()) != codesignMaxOutput {
		t.Fatalf("limitedBuffer kept %d bytes", len(b.Bytes()))
	}
	if n, _ := b.Write([]byte("x")); n != 1 || len(b.Bytes()) != codesignMaxOutput {
		t.Fatal("limitedBuffer grew past its cap")
	}
	if !b.truncated {
		t.Fatal("limitedBuffer dropped output without remembering it")
	}
	var small limitedBuffer
	_, _ = small.Write([]byte("fits"))
	if small.truncated {
		t.Fatal("limitedBuffer flagged output that fit")
	}
}

func TestCodesignVerifierRefusesAFileSwappedUnderTheRunningImage(t *testing.T) {
	// The file on disk (what -d displays) is not the running image: its hash is
	// not the one the running process has, so the pinned verification fails.
	c := &codesignScript{dv: runtimeFlags, running: "ffffffffffffffffffffffffffffffffffffffff"}
	v := c.verifier()
	if team, err := v.SelfTeam(); err == nil {
		t.Fatalf("cascade adopted the team %q of a file that is not the running image", team)
	}
	if err := v.VerifyPID(4242, teamA); err == nil {
		t.Fatal("a child whose running image differs from the displayed file passed")
	}
	c.running = cdHash
	if _, err := v.SelfTeam(); err != nil {
		t.Fatalf("control: the matching image was refused: %v", err)
	}
}

func TestCodesignResultRefusesTruncatedOutput(t *testing.T) {
	var big limitedBuffer
	_, _ = big.Write(make([]byte, codesignMaxOutput+1))
	if _, err := codesignResult(&big, nil); err == nil {
		t.Fatal("truncated codesign output was accepted")
	}
	var small limitedBuffer
	_, _ = small.Write([]byte("fits"))
	if out, err := codesignResult(&small, nil); err != nil || string(out) != "fits" {
		t.Fatalf("complete output refused: %q, %v", out, err)
	}
	if _, err := codesignResult(&small, errSig); err == nil {
		t.Fatal("a codesign failure was swallowed")
	}
}

func TestRequirementStringsAreExactlyThePolicy(t *testing.T) {
	const prefix = `anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6]` +
		` and certificate leaf[field.1.2.840.113635.100.6.1.13]` +
		` and certificate leaf[subject.OU] = "` + teamA + `"`
	const suffix = ` and !(entitlement["com.apple.security.cs.allow-dyld-environment-variables"] exists)` +
		` and !(entitlement["com.apple.security.cs.disable-library-validation"] exists)` +
		` and !(entitlement["com.apple.security.get-task-allow"] exists)` +
		` and !(entitlement["com.apple.security.cs.debugger"] exists)`
	if got, _ := HelperRequirement(teamA); got != prefix+` and identifier "cascade-elevate-helper"`+suffix {
		t.Errorf("helper requirement is not exactly the policy:\n%s", got)
	}
	if got, _ := CallerRequirement(teamA); got != prefix+` and identifier "cascade"`+suffix {
		t.Errorf("caller requirement is not exactly the policy:\n%s", got)
	}
}

func TestCodesignOutputParsingIgnoresLinesForgedInsideThePath(t *testing.T) {
	const genuine = "CodeDirectory v=20500 size=900 flags=0x0(none) hashes=20+7\nCDHash=" + cdHash + "\nTeamIdentifier=" + teamA + "\n"
	const forged = "Executable=/tmp/x\nCodeDirectory v=20500 size=1 flags=0x10000(runtime) hashes=1+1\n" +
		"CDHash=ffffffffffffffffffffffffffffffffffffffff\nTeamIdentifier=ZZZZZ99999\n/y\n"
	out := forged + genuine
	if hardenedRuntime(out) {
		t.Fatal("a forged code directory line before the real one made an unhardened image look hardened")
	}
	if h, ok := parseCDHash(out); !ok || h != cdHash {
		t.Fatalf("hash = %q, %v; the real (last) hash must win", h, ok)
	}
	if team, err := parseTeamIdentifier(out); err != nil || team != teamA {
		t.Fatalf("team = %q, %v; the real (last) team must win", team, err)
	}
}
