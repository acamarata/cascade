package build

import "regexp"

// claimMarkerRe matches the case-sensitive stand-in markers.
var claimMarkerRe = regexp.MustCompile(`\b(TODO|FIXME|TBD|XXX|PLACEHOLDER)\b`)

// claimPhraseRe matches the case-insensitive stale-forward-claim phrases.
var claimPhraseRe = regexp.MustCompile(`(?i)(will later|not yet (implemented|wired|mounted|available|landed|built|shipped)|awaiting (its|their) owning|until (that|this) (ticket )?lands|is the seed of|coming soon)`)

// checkClaims runs the claim rule over content: every marker/phrase match
// outside fenced code and inline-code spans is one finding.
func checkClaims(page, content string, k *keyer) []DocFinding {
	var findings []DocFinding
	for i, dl := range parseDocLines(content) {
		if dl.fenced {
			continue
		}
		stripped, _ := stripInlineCode(dl.text)
		for _, m := range claimMarkerRe.FindAllString(stripped, -1) {
			findings = append(findings, mkFinding(k, page, i+1, DocRuleClaim, "stale marker: "+m, dl.text))
		}
		for _, m := range claimPhraseRe.FindAllString(stripped, -1) {
			findings = append(findings, mkFinding(k, page, i+1, DocRuleClaim, "stale-claim phrase: "+m, dl.text))
		}
	}
	return findings
}
