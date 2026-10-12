// Purpose: shape gates for the promotion workflows (P1-SHIP-11,
// docs/developer/promotion.md). They parse .github/workflows/pages-keys.yml
// and prove each check with a seeded violation that must turn it red; the
// key-equality gate runs the same check the workflow runs. No network.
package build

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/release/promotion"
	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	pagesWorkflowPath = ".github/workflows/pages-keys.yml"
	pagesKeyCheck     = "go run ./internal/tools/promotion check-key --pub docs/keys/minisign.pub --embedded internal/minisign/release.pub"
	pagesStagedCheck  = `go run ./internal/tools/promotion check-key --pub "${RUNNER_TEMP}/site/keys/minisign.pub" --embedded internal/minisign/release.pub`
	shaPinnedUses     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
)

// pagesKeysViolations lists every way pages-keys.yml drifts from the contract.
func pagesKeysViolations(t *testing.T, text string) []string {
	var c relCheck
	wf := relDecode[relWorkflow](t, text)
	c.bad(slices.Equal(slices.Sorted(maps.Keys(wf.On)), []string{"workflow_dispatch"}), "pages-keys triggers are not exactly workflow_dispatch")
	want := map[string]string{"contents": "read", "pages": "write", "id-token": "write"}
	c.bad(maps.Equal(wf.Permissions, want), "pages-keys permissions are not exactly contents read, pages write, id-token write")
	c.bad(!strings.Contains(text, "secrets.") && !strings.Contains(text, "vars."), "pages-keys reads a secret or a variable")
	c.bad(len(wf.Jobs) == 1, "pages-keys does not have exactly one job")
	for name, job := range wf.Jobs {
		c.bad(job.Permissions == nil, "pages-keys job "+name+" sets its own permissions")
		pagesStepViolations(job.Steps, &c)
	}
	return c
}

// pagesStepViolations checks the step order: only checkout and setup-go
// precede the key check; staging re-checks; upload then deploy come last.
func pagesStepViolations(steps []relStep, c *relCheck) {
	idx := func(f func(relStep) bool) int { return slices.IndexFunc(steps, f) }
	check := idx(func(s relStep) bool { return strings.TrimSpace(s.Run) == pagesKeyCheck })
	staged := idx(func(s relStep) bool { return strings.Contains("\n"+s.Run+"\n", "\n"+pagesStagedCheck+"\n") })
	upload := idx(func(s relStep) bool { return strings.HasPrefix(s.Uses, "actions/upload-pages-artifact@") })
	deploy := idx(func(s relStep) bool { return strings.HasPrefix(s.Uses, "actions/deploy-pages@") })
	c.bad(check >= 0, "pages-keys has no docs/keys key check")
	for i, s := range steps {
		uses := strings.Fields(s.Uses)
		c.bad(s.Uses == "" || (len(uses) == 1 && shaPinnedUses.MatchString(uses[0])), "pages-keys step uses an unpinned action "+s.Uses)
		pre := strings.HasPrefix(s.Uses, "actions/checkout@") || strings.HasPrefix(s.Uses, "actions/setup-go@")
		c.bad(check < 0 || i >= check || pre, "pages-keys runs a step before the key check")
	}
	c.bad(check >= 0 && check < staged && staged < upload, "the key check and the staged re-check do not precede the upload")
	c.bad(upload >= 0 && deploy == upload+1 && deploy == len(steps)-1, "upload-pages-artifact and deploy-pages are not the last two steps")
	if upload >= 0 {
		c.bad(steps[upload].With["path"] == "${{ runner.temp }}/site", "upload-pages-artifact does not upload the checked staging dir")
	}
}

func TestPagesKeysWorkflowShape(t *testing.T) {
	upload := "      - uses: actions/upload-pages-artifact@fc324d3547104276b827a68afc52ff2a11cc49c9 # v5.0.0\n        with:\n          path: ${{ runner.temp }}/site\n"
	relSeeds(t, pagesWorkflowPath, pagesKeysViolations, map[string][2]string{
		"triggers are not":          {"on:\n  workflow_dispatch:\n", "on:\n  workflow_dispatch:\n  push:\n    branches: [main]\n"},
		"permissions are not":       {"  id-token: write\n", "  id-token: write\n  actions: write\n"},
		"sets its own permissions":  {"    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    permissions:\n      contents: write\n"},
		"no docs/keys key check":    {pagesKeyCheck, "echo skipped"},
		"before the key check":      {"      - name: Refuse unless", "      - run: echo hello\n      - name: Refuse unless"},
		"do not precede the upload": {upload, ""},
		"unpinned action":           {"actions/deploy-pages@368f82528645a54fb793d4d04e342629a3f51346", "actions/deploy-pages@v5"},
		"not the last two steps":    {"      - id: deploy\n", "      - run: echo after\n      - id: deploy\n"},
		"checked staging dir":       {"path: ${{ runner.temp }}/site", "path: docs"},
		"reads a secret":            {"cache: false\n", "cache: false\n          token: ${{ secrets.GITHUB_TOKEN }}\n"},
	})
}

func TestPagesKeysChecksEmbeddedKey(t *testing.T) {
	published, embedded := []byte(relRead(t, "docs/keys/minisign.pub")), []byte(relRead(t, "internal/minisign/release.pub"))
	trusted, err := minisign.ReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if err = promotion.CheckKey(published, embedded, trusted); err != nil {
		t.Fatalf("the committed key must pass the pages-keys check: %v", err)
	}
	for i, side := range []string{"published", "embedded"} {
		pub, emb := slices.Clone(published), slices.Clone(embedded)
		target := [][]byte{pub, emb}[i]
		target[len(target)-2] ^= 1 // one byte inside the key line
		err = promotion.CheckKey(pub, emb, trusted)
		if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindIntegrity || !strings.Contains(err.Error(), "promotion: refused") {
			t.Errorf("seeded one-byte difference on the %s side stayed green: %v", side, err)
		}
	}
	if !strings.Contains(relRead(t, pagesWorkflowPath), "        run: "+pagesKeyCheck+"\n") {
		t.Fatal("pages-keys does not run the check-key gate this test exercises")
	}
}

const (
	promoteCopy = `oras cp --recursive "ghcr.io/${CASCADE_STAGING_REMOTE}@${index}" "ghcr.io/acamarata/cascade:${FINAL_TAG#v}"`
	promoteSign = `cosign sign --yes "ghcr.io/acamarata/cascade@${digest}"`
	promoteGate = `go run ./internal/tools/promotion verify-signed --dir "${RUNNER_TEMP}/dist" --pubkey docs/keys/minisign.pub --final-tag "$FINAL_TAG"`
	promoteTool = `go run ./internal/tools/promotion images --dir "${RUNNER_TEMP}/dist" --pubkey docs/keys/minisign.pub --final-tag "$FINAL_TAG"`
)

func relFind(steps []relStep, sub string) int {
	return slices.IndexFunc(steps, func(s relStep) bool { return strings.Contains(s.Uses+"\n"+s.Run, sub) })
}

// promoteShapeViolations lists every way the promote job drifts from the contract.
func promoteShapeViolations(t *testing.T, text string) []string {
	var c relCheck
	steps, has := relDecode[relWorkflow](t, text).Jobs["promote"].Steps, strings.Contains
	refuse, checkout := relFind(steps, "record_json is larger than 64 KiB"), relFind(steps, "actions/checkout@")
	c.bad(len(steps) > 0, "there is no promote job")
	c.bad(refuse >= 0 && refuse < checkout, "promote refusal step does not precede checkout")
	for _, s := range steps {
		uses := strings.Fields(s.Uses)
		c.bad(s.Uses == "" || (len(uses) == 1 && shaPinnedUses.MatchString(uses[0])), "promote step uses an unpinned action "+s.Uses)
		c.bad(!has(s.Run, "${{"), "promote interpolates an expression into a script")
		c.bad(s.Env["CASCADE_STAGING_REMOTE"] == relStagingSecret || (s.Env["CASCADE_STAGING_REMOTE"] == nil && !has(s.Run, "CASCADE_STAGING_REMOTE")), "promote staging remote is not from the secret")
		for _, rebuild := range []string{"goreleaser", "docker ", "go build", "oras push", "git push"} {
			c.bad(!has(s.Uses+s.Run, rebuild), "promote rebuilds or pushes: "+rebuild)
		}
		c.bad(!has(s.Uses, "actions/setup-go@") || s.With["cache"] == false, "promote leaves the action cache on")
	}
	c.bad(checkout >= 0 && steps[checkout].With["ref"] == "${{ inputs.commit_sha }}" && steps[checkout].With["persist-credentials"] == false, "promote does not check out commit_sha without credentials")
	for _, sub := range []string{`id="${COMMIT_SHA:0:12}"`, `oras pull "ghcr.io/${CASCADE_STAGING_REMOTE}:dist-${id}" -o "${RUNNER_TEMP}/dist"`, `promote-copy.log`} {
		c.bad(relFind(steps, sub) >= 0, "promote lacks the fixed step text "+sub)
	}
	cp, sign, rel := relFind(steps, "oras cp"), relFind(steps, "cosign sign"), relFind(steps, "gh release create")
	c.bad(cp >= 0 && has(steps[cp].Run, promoteCopy) && has(steps[cp].Run, `index="$(`+promoteTool+` --name cascade)"`), "promote oras cp is not by the signed index digest, recursive")
	c.bad(sign >= 0 && has(steps[sign].Run, promoteSign) && has(steps[sign].Run, promoteTool+` > "${RUNNER_TEMP}/signed-images.txt"`), "promote cosign signs a digest not read from the signed cascade_images.txt")
	c.bad(rel >= 0 && has(steps[rel].Run, `--target "$COMMIT_SHA"`) && !has(steps[rel].Run, "--draft") && has(steps[rel].Run, `"${RUNNER_TEMP}"/release/*`), "promote release is a draft, untargeted or not exactly the verified files")
	for _, i := range []int{cp, sign} {
		c.bad(i >= 0 && !has(steps[i].Run, "record"), "promote image step reads the record")
	}
	promoteGateViolations(steps, &c)
	return c
}

// promoteGateViolations pins the first gate itself: its exact command, the one
// staging pull before it, the tag guard and the checks that make the image path
// and the record agree with the signed set.
func promoteGateViolations(steps []relStep, c *relCheck) {
	gate, tagGuard, cosign := relFind(steps, "promotion verify-signed"), relFind(steps, "git ls-remote origin"), relFind(steps, "cosign sign")
	pulls := 0
	for _, s := range steps {
		pulls += strings.Count(s.Run, "oras pull")
	}
	c.bad(gate >= 0 && strings.TrimSpace(steps[gate].Run) == promoteGate, "promote verify-signed is not the exact first-gate command")
	c.bad(pulls == 1 && relFind(steps, "oras pull") < gate, "promote pulls more than once or after the first gate")
	c.bad(tagGuard >= 0 && tagGuard < relFind(steps, "attest-build-provenance") && strings.Contains(steps[tagGuard].Run, "already names another commit"), "promote has no tag guard before the attestation")
	c.bad(relFind(steps, `| sort | cmp - "${RUNNER_TEMP}/record-images.txt"`) >= 0, "promote does not compare the record images with the signed images")
	c.bad(cosign >= 0 && strings.Contains(steps[cosign].Run, `${digest}" < /dev/null`) && strings.Contains(steps[cosign].Run, `-eq "$(grep -c .`), "promote cosign loop shares stdin or does not count signatures")
}

// promoteOrderViolations: verify-signed is the first gate, then record check,
// attestation, release, image copy and image signing, in that order; attest and
// cosign exist in no other job.
func promoteOrderViolations(t *testing.T, text string) []string {
	var c relCheck
	wf := relDecode[relWorkflow](t, text)
	steps, has := wf.Jobs["promote"].Steps, strings.Contains
	gate, pull := relFind(steps, "promotion verify-signed"), relFind(steps, "oras pull")
	c.bad(pull >= 0 && pull < gate, "verify-signed does not follow the staging pull")
	order := []string{"promotion verify --record", "attest-build-provenance", "gh release create", "oras cp", "cosign sign"}
	last := gate
	for _, sub := range order {
		at := relFind(steps, sub)
		c.bad(gate >= 0 && at > last, "promote runs "+sub+" before verify-signed or out of order")
		last = max(last, at)
	}
	for i, s := range steps {
		for _, sub := range []string{"attest-build-provenance", "cosign", "gh release", "oras cp", "promotion verify --record"} {
			c.bad(gate >= 0 && (i > gate || !has(s.Uses+s.Run, sub)), "promote runs "+sub+" before verify-signed")
		}
	}
	for name, job := range wf.Jobs {
		for _, s := range job.Steps {
			c.bad(name == "promote" || !has(s.Uses+s.Run, "attest-build-provenance") && !has(s.Uses+s.Run, "cosign"), "job "+name+" attests or signs outside promote")
		}
	}
	c.bad(!slices.ContainsFunc(steps, func(s relStep) bool { return has(s.Run, "oras cp") && !has(s.Run, "oras cp --recursive") }), "promote oras cp lacks --recursive")
	return c
}

func TestPromoteJobShape(t *testing.T) {
	verify := "      - name: Verify the signed set (first gate)\n"
	check := func(t *testing.T, text string) []string {
		return append(promoteShapeViolations(t, text), promoteOrderViolations(t, text)...)
	}
	relSeeds(t, relWorkflowPath, check, map[string][2]string{
		"before verify-signed":     {verify, "      - run: gh release create x\n" + verify},
		"exact first-gate":         {`--final-tag "$FINAL_TAG"` + "\n\n      # Audit", `--final-tag "$FINAL_TAG" || true` + "\n\n      # Audit"},
		"exact first-gate ":        {`verify-signed --dir "${RUNNER_TEMP}/dist" --pubkey docs/keys/minisign.pub`, `verify-signed --dir "${RUNNER_TEMP}/dist" --pubkey "${RUNNER_TEMP}/k.pub"`},
		"pulls more than once":     {"      # Audit consistency only", "      - run: oras pull x -o \"${RUNNER_TEMP}/dist\"\n      # Audit consistency only"},
		"no tag guard":             {`git ls-remote origin "refs/tags/${FINAL_TAG}"`, `true "refs/tags/${FINAL_TAG}"`},
		"record images":            {`| sort | cmp - "${RUNNER_TEMP}/record-images.txt"`, `| sort > /dev/null`},
		"shares stdin":             {`${digest}" < /dev/null`, `${digest}"`},
		"unpinned action":          {"oras-project/setup-oras@005458ad77f1c8facd38a094e4af2e69e5607ff4 # v2.0.2\n\n      # gh ignores", "oras-project/setup-oras@v2\n\n      # gh ignores"},
		"expression into a":        {`printf '%s' "$NOTES"`, `printf '%s' "${{ inputs.notes }}"`},
		"staging remote is not":    {"          CASCADE_STAGING_REMOTE: ${{ secrets.CASCADE_STAGING_REMOTE }}\n          COMMIT_SHA: ${{ inputs.commit_sha }}\n          REGISTRY_TOKEN", "          CASCADE_STAGING_REMOTE: zz-owner/zz-repo\n          COMMIT_SHA: ${{ inputs.commit_sha }}\n          REGISTRY_TOKEN"},
		"rebuilds or pushes":       {verify, "      - run: git push origin HEAD\n" + verify},
		"leaves the action cache":  {"          go-version-file: go.mod\n          cache: false\n\n      - name: Set up oras (promote job)", "          go-version-file: go.mod\n\n      - name: Set up oras (promote job)"},
		"refusal step does not":    {"      # Precedes checkout.", "      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0\n      # Precedes checkout."},
		"fixed step text":          {`oras pull "ghcr.io/${CASCADE_STAGING_REMOTE}:dist-${id}"`, `oras pull "ghcr.io/${CASCADE_STAGING_REMOTE}:build-${id}"`},
		"not by the signed index":  {`@${index}" "ghcr.io/acamarata/cascade:`, `:build-${index}" "ghcr.io/acamarata/cascade:`},
		"not read from the signed": {` > "${RUNNER_TEMP}/signed-images.txt"`, ` > /dev/null; jq -r '.images | to_entries[] | .value + " x"' "${RUNNER_TEMP}/record.json" > "${RUNNER_TEMP}/signed-images.txt"`},
		"draft, untargeted":        {` --target "$COMMIT_SHA"`, " --draft"},
		"without credentials":      {"          ref: ${{ inputs.commit_sha }}\n          persist-credentials: false\n\n      # No action cache in this job: nothing it handles", "          ref: ${{ inputs.commit_sha }}\n\n      # No action cache in this job: nothing it handles"},
	})
}

func TestPromoteJobVerifiesMinisign(t *testing.T) {
	verify, attest := "      - name: Verify the signed set (first gate)\n", "      - name: Attest build provenance over the archives and SBOMs\n"
	relSeeds(t, relWorkflowPath, promoteOrderViolations, map[string][2]string{
		"gh release before": {verify, "      - run: gh release create x\n" + verify},
		"attest-build-pro":  {verify, "      - uses: actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2\n" + verify},
		"cosign before":     {verify, "      - run: cosign version\n" + verify},
		"oras cp before":    {verify, "      - run: oras cp --recursive a b\n" + verify},
		"out of order":      {attest, "      - run: gh release create x\n" + attest},
		"lacks --recursive": {"oras cp --recursive \"ghcr.io/${CASCADE_STAGING_REMOTE}@", "oras cp \"ghcr.io/${CASCADE_STAGING_REMOTE}@"},
		"outside promote":   {"      - name: goreleaser check\n        run: goreleaser check\n", "      - name: goreleaser check\n        run: goreleaser check\n      - run: cosign version\n"},
	})
}
