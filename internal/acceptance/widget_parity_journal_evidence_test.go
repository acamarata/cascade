package acceptance

// Purpose (this file): the evidence-file checks of the widget parity journal
//   verifier — the parts that read machine-written output instead of trusting
//   a typed cell: the artifact hash, the CDHash, the signing and notarization
//   records, the fresh-install timing and the captured v1 feed rows.
// Inputs: paths already resolved inside the journal's directory.
// Outputs: problem strings; an empty slice means the evidence holds.
// Constraints: no clock reads and no network; a missing, blank or unparsable
//   input is always a problem, never a pass.
// Contract: contract:widget-parity-journal (P1-WID-10).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// parityBundleExecutable is the file a .app artifact is hashed through.
	parityBundleExecutable = "CascadeMenuBar"
	// parityFreshInstallMax is the install-to-first-rows limit.
	parityFreshInstallMax = 30 * time.Second
)

var (
	cdhashRE         = regexp.MustCompile(`(?m)^CDHash=([0-9a-f]{40,64})\s*$`)
	notaryIDRE       = regexp.MustCompile(`(?m)^\s*id:\s*[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\s*$`)
	notaryAcceptedRE = regexp.MustCompile(`(?m)^\s*(Current )?status:\s*Accepted\s*$`)
	spctlSourceRE    = regexp.MustCompile(`(?m)^source=Notarized Developer ID\s*$`)
	notarySkipRE     = regexp.MustCompile(`(?i)\bskip`)
)

// artifactSHA256 hashes the artifact itself: a .dmg or other file as is, a
// .app bundle through its main executable.
func artifactSHA256(artifact string) (string, error) {
	path := filepath.Clean(artifact)
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("artifact %q is not readable", artifact)
	}
	if st.IsDir() {
		if !strings.HasSuffix(path, ".app") {
			return "", fmt.Errorf("artifact %q is a directory but not a .app bundle", artifact)
		}
		path = filepath.Join(path, "Contents", "MacOS", parityBundleExecutable)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("artifact file %q is not readable", path)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("artifact file %q: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parseCDHash reads the CDHash line of `codesign -d --verbose=4` output.
func parseCDHash(out string) (string, error) {
	m := cdhashRE.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("no CDHash= line")
	}
	return m[1], nil
}

// signingProblems checks the SIGNING evidence directory: codesign.txt,
// spctl.txt and notarization.txt, all present and non-blank.
func signingProblems(dir string, env parityEnv) []string {
	var out []string
	text := map[string]string{}
	for _, name := range []string{"codesign.txt", "spctl.txt", "notarization.txt"} {
		t, err := readParityText(filepath.Join(dir, name))
		if err != nil {
			out = append(out, fmt.Sprintf("SIGNING file %s: %v", name, err))
			continue
		}
		text[name] = t
	}
	if t, ok := text["codesign.txt"]; ok {
		out = append(out, codesignProblems(t, env)...)
	}
	if t, ok := text["spctl.txt"]; ok {
		out = append(out, spctlProblems("spctl.txt", t)...)
	}
	if t, ok := text["notarization.txt"]; ok {
		out = append(out, notarizationProblems(t)...)
	}
	out = append(out, spctlLiveProblems(env)...)
	return out
}

// codesignProblems compares codesign.txt with the artifact's own signature,
// recomputed by env.codesignOf: same CDHash, and a Developer ID authority
// (an ad-hoc or unsigned artifact is red however the typed file reads).
func codesignProblems(typed string, env parityEnv) []string {
	got, err := parseCDHash(typed)
	if err != nil {
		return []string{"codesign.txt: " + err.Error()}
	}
	if env.codesignOf == nil {
		return []string{"cannot recompute the artifact's signature: no codesign source"}
	}
	probe, err := env.codesignOf(env.artifact)
	if err != nil {
		return []string{"cannot recompute the artifact's signature: " + err.Error()}
	}
	want, err := parseCDHash(probe)
	if err != nil {
		return []string{"the artifact's own signature: " + err.Error()}
	}
	var out []string
	if got != want {
		out = append(out, fmt.Sprintf("codesign.txt CDHash %s differs from the artifact's CDHash %s", got, want))
	}
	if !strings.Contains(probe, "Authority=Developer ID Application") {
		out = append(out, "the artifact's own signature shows no Developer ID Application authority (ad-hoc or unsigned)")
	}
	return out
}

// spctlProblems requires the FIRST line to say accepted, no line to say
// rejected, and the source line to be Notarized Developer ID.
func spctlProblems(name, text string) []string {
	var out []string
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if !strings.HasSuffix(strings.TrimSpace(first), ": accepted") {
		out = append(out, name+" does not say accepted on its first line")
	}
	if strings.Contains(strings.ToLower(text), "rejected") {
		out = append(out, name+" says rejected")
	}
	if !spctlSourceRE.MatchString(text) {
		out = append(out, name+` does not show source "Notarized Developer ID"`)
	}
	return out
}

// spctlLiveProblems checks the Gatekeeper assessment recomputed for the artifact.
func spctlLiveProblems(env parityEnv) []string {
	if env.spctlOf == nil {
		return []string{"cannot recompute the artifact's spctl assessment: no spctl source"}
	}
	out, err := env.spctlOf(env.artifact)
	if err != nil {
		return []string{"cannot recompute the artifact's spctl assessment: " + err.Error()}
	}
	return spctlProblems("the artifact's own spctl assessment", out)
}

// notarizationProblems requires a submission id and an Accepted status, and
// refuses a record that says the step was skipped.
func notarizationProblems(text string) []string {
	if notarySkipRE.MatchString(text) {
		return []string{"notarization.txt reads skipped"}
	}
	var out []string
	if !notaryIDRE.MatchString(text) {
		out = append(out, "notarization.txt has no notarytool submission id")
	}
	if !notaryAcceptedRE.MatchString(text) {
		out = append(out, "notarization.txt does not show status Accepted")
	}
	return out
}

// parityTime reads a JSON value that must be an RFC 3339 string.
func parityTime(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("is null or not a string")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not RFC 3339", s)
	}
	return t, nil
}

// readParityJSON reads a non-blank evidence file holding one JSON object.
func readParityJSON(path string) (map[string]any, error) {
	text, err := readParityText(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %v", filepath.Base(path), err)
	}
	return obj, nil
}

// freshInstallProblems checks install.json (the saved `cascade widget install
// --json` output) against widget-status.json: first rows within 30 s of the
// install, never before it.
func freshInstallProblems(dir string) []string {
	inst, err := readParityJSON(filepath.Join(dir, "install.json"))
	if err != nil {
		return []string{"FRESH-INSTALL install.json: " + err.Error()}
	}
	status, err := readParityJSON(filepath.Join(dir, "widget-status.json"))
	if err != nil {
		return []string{"FRESH-INSTALL widget-status.json: " + err.Error()}
	}
	installed := inst["installed_at"]
	if data, ok := inst["data"].(map[string]any); ok {
		installed = data["installed_at"]
	}
	at, err := parityTime(installed)
	if err != nil {
		return []string{"FRESH-INSTALL installed_at " + err.Error()}
	}
	first, err := parityTime(status["first_rows_at"])
	if err != nil {
		return []string{"FRESH-INSTALL first_rows_at " + err.Error()}
	}
	switch delta := first.Sub(at); {
	case delta < 0:
		return []string{"FRESH-INSTALL first_rows_at precedes installed_at (a stale status file)"}
	case delta > parityFreshInstallMax:
		return []string{fmt.Sprintf("FRESH-INSTALL first rows came %s after install; the limit is %s", delta, parityFreshInstallMax)}
	}
	return nil
}

// feedRowProblems checks that evidence is one JSON object copied from the v1
// feed for label: the label, both window percentages (number or null), both
// reset fields and the capture timestamp. A bare label is not evidence.
func feedRowProblems(text, label string) []string {
	var row map[string]any
	if err := json.Unmarshal([]byte(text), &row); err != nil {
		return []string{"evidence is not a captured v1 feed row (one JSON object)"}
	}
	var out []string
	if got, _ := row["label"].(string); got != label {
		out = append(out, fmt.Sprintf("feed row label %q, want %q", got, label))
	}
	for _, k := range []string{"five_hour_pct", "weekly_pct"} {
		v, ok := row[k]
		n, isNum := v.(float64)
		switch {
		case !ok:
			out = append(out, "feed row lacks "+k)
		case v != nil && !isNum:
			out = append(out, "feed row "+k+" must be a number or null")
		case isNum && n < 0:
			out = append(out, "feed row "+k+" must not be negative")
		}
	}
	for _, k := range []string{"five_hour_reset", "weekly_reset"} {
		v, ok := row[k]
		if s, isStr := v.(string); !ok || (isStr && strings.TrimSpace(s) == "") {
			out = append(out, "feed row lacks a usable "+k+" (null, or the feed's value)")
		}
	}
	if _, err := parityTime(row["captured_at"]); err != nil {
		out = append(out, "feed row captured_at "+err.Error())
	}
	return out
}
