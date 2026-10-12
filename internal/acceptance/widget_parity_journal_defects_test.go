package acceptance

// Purpose (this file): the seeded-defect tables of the widget parity journal
//   tests. Each entry is ONE edit to a fresh copy of the seeded journal, the
//   locator the verifier must name and the words its message must carry.
// Constraints: tables only; the assertions live in widget_parity_journal_test.go.
// Contract: contract:widget-parity-journal (P1-WID-10).

import (
	"os"
	"strings"
	"testing"
)

// parityDefect is one seeded defect: the locator it must name, the words the
// message must carry, and the edit that introduces it.
type parityDefect struct {
	name, loc, want string
	mutate          func(s *seedTree)
}

func runParityDefects(t *testing.T, defects []parityDefect) {
	t.Helper()
	for _, d := range defects {
		t.Run(d.name, func(t *testing.T) {
			s := newSeedTree(t)
			d.mutate(s)
			s.expectRed(d.loc, d.want)
		})
	}
}

func rowDefects() []parityDefect {
	zeros := strings.Repeat("0", 64)
	return []parityDefect{
		{"missing row", "C3", "row missing", func(s *seedTree) { s.dropRow("C3") }},
		{"duplicate row", "C3", "duplicate row", func(s *seedTree) { s.dupRow("C3") }},
		{"unknown locator", "C99", "unknown locator", func(s *seedTree) {
			s.addLine("| WIDGET/C99 | passed | evidence/c1.txt | " + seedRCTag + " | " + zeros + " |")
		}},
		{"status failed", "C2", "status failed", func(s *seedTree) { s.setCell("C2", 1, parityFailed) }},
		{"disclosed on a non-C6P locator", "C5", "valid only for C6P", func(s *seedTree) { s.setCell("C5", 1, parityDisclosed) }},
		{"malformed row", "C1", "malformed row", func(s *seedTree) {
			s.editJournal(func(l []string) []string {
				l[s.rowLine(l, "C1")] = "| WIDGET/C1 | passed | evidence/c1.txt | " + seedRCTag + " |"
				return l
			})
		}},
		{"rc tag mismatch", "C10", "rc tag", func(s *seedTree) { s.setCell("C10", 3, "rc-other") }},
		{"rc tag empty", "C10", "rc tag", func(s *seedTree) { s.setCell("C10", 3, "") }},
		{"typed artifact hash differs", "C11", "artifact sha256", func(s *seedTree) { s.setCell("C11", 4, zeros) }},
		{"artifact hash empty", "C11", "artifact sha256", func(s *seedTree) { s.setCell("C11", 4, "") }},
	}
}

func evidenceDefects() []parityDefect {
	return []parityDefect{
		{"evidence missing", "C7", "does not exist", func(s *seedTree) { s.remove("evidence/c7.txt") }},
		{"evidence empty", "C8", "empty", func(s *seedTree) { s.write("evidence/c8.txt", "") }},
		{"evidence blank", "C8", "empty", func(s *seedTree) { s.write("evidence/c8.txt", " \n\t\n") }},
		{"evidence is a directory", "C1", "unreadable", func(s *seedTree) {
			s.write("evidence/dir/x.txt", "x\n")
			s.setCell("C1", 2, "evidence/dir")
		}},
		{"evidence is the journal itself", "C1", "journal itself", func(s *seedTree) { s.setCell("C1", 2, "journal.md") }},
		{"evidence is the artifact itself", "C1", "artifact itself", func(s *seedTree) { s.setCell("C1", 2, seedArtifact) }},
		{"two locators share one evidence file", "C2", "already cited by WIDGET/C1", func(s *seedTree) { s.setCell("C2", 2, "evidence/c1.txt") }},
		{"C6P without the deferral citation", "C6P", "does not cite DEF-P1-widget-project-rows", func(s *seedTree) {
			s.write("evidence/c6p.txt", "seeded disclosure that names no deferral\n")
		}},
		{"evidence path escapes", "C1", "escapes", func(s *seedTree) {
			s.write("../escape.txt", "outside\n")
			s.setCell("C1", 2, "../escape.txt")
		}},
		{"evidence path absolute", "C1", "absolute", func(s *seedTree) { s.setCell("C1", 2, s.path("evidence/c2.txt")) }},
		{"evidence symlink points outside", "C1", "outside", func(s *seedTree) {
			s.write("../escape.txt", "outside\n")
			if err := os.Symlink(s.path("../escape.txt"), s.path("evidence/link.txt")); err != nil {
				s.t.Skipf("symlinks unavailable: %v", err)
			}
			s.setCell("C1", 2, "evidence/link.txt")
		}},
	}
}

// mixedSpctl is a rejection dressed with a stray source line and a stray
// accepted line, the shape two independent line matches would pass.
const mixedSpctl = "/x.app: rejected\nsource=Notarized Developer ID\nfoo: accepted\n"

const uuid = "5eed0000-0000-4000-8000-000000000001"

func signingDefects() []parityDefect {
	return []parityDefect{
		{"SIGNING lacks codesign.txt", "SIGNING", "codesign.txt", func(s *seedTree) { s.remove("evidence/signing/codesign.txt") }},
		{"SIGNING lacks spctl.txt", "SIGNING", "spctl.txt", func(s *seedTree) { s.remove("evidence/signing/spctl.txt") }},
		{"SIGNING lacks notarization.txt", "SIGNING", "notarization.txt", func(s *seedTree) { s.remove("evidence/signing/notarization.txt") }},
		{"SIGNING evidence is a file", "SIGNING", "SIGNING file", func(s *seedTree) { s.setCell("SIGNING", 2, "evidence/c1.txt") }},
		{"SIGNING CDHash differs", "SIGNING", "differs from the artifact's CDHash", func(s *seedTree) {
			s.replaceIn("evidence/signing/codesign.txt", "CDHash=5eed", "CDHash=aaaa")
		}},
		{"SIGNING codesign has no CDHash", "SIGNING", "no CDHash", func(s *seedTree) {
			s.replaceIn("evidence/signing/codesign.txt", "CDHash=", "CDHsh=")
		}},
		{"SIGNING artifact is ad hoc", "SIGNING", "Developer ID Application authority", func(s *seedTree) {
			s.replaceIn(seedArtifact+".codesign-probe", "Authority=Developer ID Application", "Signature=adhoc")
		}},
		{"SIGNING artifact signature unavailable", "SIGNING", "cannot recompute the artifact's signature", func(s *seedTree) {
			s.remove(seedArtifact + ".codesign-probe")
		}},
		{"SIGNING artifact spctl assessment unavailable", "SIGNING", "spctl assessment", func(s *seedTree) {
			s.remove(seedArtifact + ".spctl-probe")
		}},
		{"SIGNING artifact's own spctl is rejected", "SIGNING", "does not say accepted", func(s *seedTree) {
			s.write(seedArtifact+".spctl-probe", mixedSpctl)
		}},
		{"SIGNING spctl.txt mixes rejected and accepted lines", "SIGNING", "does not say accepted", func(s *seedTree) {
			s.write("evidence/signing/spctl.txt", mixedSpctl)
		}},
		{"SIGNING notarization says SKIP requested", "SIGNING", "reads skipped", func(s *seedTree) {
			s.write("evidence/signing/notarization.txt", "SKIP requested\n  id: "+uuid+"\n  status: Accepted\n")
		}},
		{"SIGNING spctl without Notarized Developer ID", "SIGNING", "Notarized Developer ID", func(s *seedTree) {
			s.replaceIn("evidence/signing/spctl.txt", "source=Notarized Developer ID", "source=Developer ID")
		}},
		{"SIGNING spctl says Unnotarized", "SIGNING", "Notarized Developer ID", func(s *seedTree) {
			s.replaceIn("evidence/signing/spctl.txt", "source=Notarized", "source=Unnotarized")
		}},
		{"SIGNING spctl rejected", "SIGNING", "does not say accepted", func(s *seedTree) {
			s.replaceIn("evidence/signing/spctl.txt", ": accepted", ": rejected")
		}},
		{"SIGNING notarization skipped", "SIGNING", "reads skipped", func(s *seedTree) {
			s.write("evidence/signing/notarization.txt", "notarization skipped\n  id: "+uuid+"\n  status: Accepted\n")
		}},
		{"SIGNING notarization has no id", "SIGNING", "submission id", func(s *seedTree) {
			s.write("evidence/signing/notarization.txt", "status: Accepted\n")
		}},
		{"SIGNING notarization not Accepted", "SIGNING", "status Accepted", func(s *seedTree) {
			s.write("evidence/signing/notarization.txt", "id: "+uuid+"\nstatus: Invalid\n")
		}},
	}
}

func freshInstallDefects() []parityDefect {
	const row = `"first_rows_at":"2026-10-12T09:01:12Z"`
	const dir = "evidence/fresh-install/"
	return []parityDefect{
		{"FRESH-INSTALL 31 s", "FRESH-INSTALL", "limit is 30s", func(s *seedTree) {
			s.replaceIn(dir+"widget-status.json", "09:01:12Z", "09:01:31Z")
		}},
		{"FRESH-INSTALL null first_rows_at", "FRESH-INSTALL", "first_rows_at", func(s *seedTree) {
			s.replaceIn(dir+"widget-status.json", row, `"first_rows_at":null`)
		}},
		{"FRESH-INSTALL first rows before install", "FRESH-INSTALL", "precedes", func(s *seedTree) {
			s.replaceIn(dir+"widget-status.json", "09:01:12Z", "09:00:59Z")
		}},
		{"FRESH-INSTALL missing install record", "FRESH-INSTALL", "install.json", func(s *seedTree) { s.remove(dir + "install.json") }},
		{"FRESH-INSTALL install record without installed_at", "FRESH-INSTALL", "installed_at", func(s *seedTree) {
			s.write(dir+"install.json", `{"version":1,"ok":true,"data":{"action":"installed"}}`+"\n")
		}},
	}
}

func statusDefects() []parityDefect {
	return []parityDefect{
		{"PAR-A failed", "PAR-A", "status failed", func(s *seedTree) { s.setCell("PAR-A", 1, parityFailed) }},
		{"PAR-A missing", "PAR-A", "row missing", func(s *seedTree) { s.dropRow("PAR-A") }},
		{"PAR-A deferred-P2", "PAR-A", "valid only for PAR-CO", func(s *seedTree) { s.setCell("PAR-A", 1, parityDeferred) }},
		{"PAR-A disclosed-not-passed", "PAR-A", "valid only for C6P", func(s *seedTree) { s.setCell("PAR-A", 1, parityDisclosed) }},
		{"deferred-P2 on C1", "C1", "valid only for PAR-CO", func(s *seedTree) { s.setCell("C1", 1, parityDeferred) }},
		{"deferred-P2 on C6P", "C6P", "valid only for PAR-CO", func(s *seedTree) { s.setCell("C6P", 1, parityDeferred) }},
		{"deferred-P2 on SB1", "SB1", "valid only for PAR-CO", func(s *seedTree) { s.setCell("SB1", 1, parityDeferred) }},
		{"deferred-P2 on FRESH-INSTALL", "FRESH-INSTALL", "valid only for PAR-CO", func(s *seedTree) { s.setCell("FRESH-INSTALL", 1, parityDeferred) }},
		{"deferred-P2 on SIGNING", "SIGNING", "valid only for PAR-CO", func(s *seedTree) { s.setCell("SIGNING", 1, parityDeferred) }},
		{"PAR-CO failed is never downgraded", "PAR-CO", "status failed", func(s *seedTree) { s.setCell("PAR-CO", 1, parityFailed) }},
		{"unknown status", "PAR-GP", "unknown status", func(s *seedTree) { s.setCell("PAR-GP", 1, "pending") }},
		{"status case is exact", "PAR-GP", "unknown status", func(s *seedTree) { s.setCell("PAR-GP", 1, "Deferred-P2") }},
	}
}

func feedRowDefects() []parityDefect {
	const f = "evidence/par-za.json"
	drop := func(old, why string) parityDefect {
		return parityDefect{"feed row without " + why, "PAR-ZA", why, func(s *seedTree) { s.replaceIn(f, old, "") }}
	}
	return []parityDefect{
		{"evidence is the bare label", "PAR-CO", "not a captured v1 feed row", func(s *seedTree) { s.write("evidence/par-co.json", "CO\n") }},
		{"evidence is another label's row", "PAR-OC", `label "GP", want "OC"`, func(s *seedTree) {
			s.write("evidence/par-oc.json", s.read("evidence/par-gp.json"))
		}},
		drop("  \"five_hour_pct\": 12,\n", "five_hour_pct"),
		drop("  \"weekly_pct\": null,\n", "weekly_pct"),
		drop("  \"five_hour_reset\": \"2026-10-12T14:00:00Z\",\n", "five_hour_reset"),
		drop("  \"weekly_reset\": null,\n", "weekly_reset"),
		drop(",\n  \"captured_at\": \"2026-10-12T09:00:00Z\"", "captured_at"),
		{"feed row captured_at not a timestamp", "PAR-ZA", "captured_at", func(s *seedTree) {
			s.replaceIn(f, "2026-10-12T09:00:00Z", "yesterday")
		}},
		{"feed row percentage is negative", "PAR-ZA", "must not be negative", func(s *seedTree) { s.replaceIn(f, "12,", "-5,") }},
		{"feed row reset is blank", "PAR-ZA", "usable weekly_reset", func(s *seedTree) { s.replaceIn(f, `"weekly_reset": null`, `"weekly_reset": ""`) }},
		{"feed row percentage is text", "PAR-ZA", "number or null", func(s *seedTree) { s.replaceIn(f, "12,", `"12%",`) }},
		{"feed row evidence empty", "PAR-ZA", "empty", func(s *seedTree) { s.write(f, "") }},
	}
}
