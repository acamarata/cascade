// Purpose: the exact per-job allowlist for release.yml (P1-BF-R221): the
// helpers workflowViolations calls and the test that proves each refusal with a
// seeded violation. No network.
package build

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// relPromotionJobs is the exact per-job allowlist for release.yml (P1-BF-R221):
// the only jobs that may set job-level permissions or hold the staging secret
// in step env, each with its dispatch gate on record_json.
var relPromotionJobs = map[string]struct {
	If    string
	Perms map[string]string
}{
	"rc-publish": {"${{ github.event_name == 'workflow_dispatch' && inputs.record_json == '' }}", map[string]string{"contents": "read", "packages": "write"}},
	"promote": {"${{ github.event_name == 'workflow_dispatch' && inputs.record_json != '' }}",
		map[string]string{"contents": "write", "packages": "write", "id-token": "write", "attestations": "write"}},
}

// relPermsExact: the job sets exactly its allowlisted permissions (none for any other job).
func relPermsExact(name string, job relJob) bool {
	want := relPromotionJobs[name].Perms
	return (job.Permissions == nil) == (want == nil) && maps.Equal(job.Permissions, want)
}

// relStagingHolders counts the steps of the allowlisted jobs whose env holds the staging secret.
func relStagingHolders(wf relWorkflow) (n int) {
	for name := range relPromotionJobs {
		for _, s := range wf.Jobs[name].Steps {
			if s.Env["CASCADE_STAGING_REMOTE"] == relStagingSecret {
				n++
			}
		}
	}
	return n
}

// relLooseKeys covers keys relJob and relStep do not decode: any job that calls
// a reusable workflow or passes secrets, and in promote any job or step
// level if or continue-on-error that could skip or soften a gate.
func relLooseKeys(t *testing.T, text string) []string {
	type step struct {
		If   string `yaml:"if"`
		Soft any    `yaml:"continue-on-error"`
	}
	var c relCheck
	wf := relDecode[struct {
		Env      any            `yaml:"env"`
		Defaults any            `yaml:"defaults"`
		Conc     map[string]any `yaml:"concurrency"`
		Jobs     map[string]struct {
			Uses    string `yaml:"uses"`
			Secrets any    `yaml:"secrets"`
			Soft    any    `yaml:"continue-on-error"`
			Steps   []step `yaml:"steps"`
		} `yaml:"jobs"`
	}](t, text)
	c.bad(wf.Conc["cancel-in-progress"] == "${{ github.event_name == 'push' }}" && wf.Conc["group"] == "release-${{ github.event_name }}-${{ github.ref }}", "workflow concurrency can cancel a running promote")
	c.bad(wf.Env == nil && wf.Defaults == nil, "the workflow sets env or defaults for every job")
	for name, job := range wf.Jobs {
		c.bad(job.Uses == "" && job.Secrets == nil, "job "+name+" calls a reusable workflow or passes secrets")
		c.bad(name != "promote" || job.Soft == nil, "promote sets continue-on-error on the job")
		for i, s := range job.Steps {
			c.bad(name != "promote" || (s.If == "" && s.Soft == nil), fmt.Sprint("promote step ", i, " sets if or continue-on-error"))
		}
	}
	return c
}

// TestReleaseWorkflowPerJobAllowlist: the per-job allowlist admits exactly the
// two jobs it names and refuses every other job, permission, input and secret place.
func TestReleaseWorkflowPerJobAllowlist(t *testing.T) {
	verify := "      - name: Verify the signed set (first gate)\n"
	steps, gate := "      contents: write\n      packages: write\n      id-token: write\n      attestations: write\n    steps:\n", "    if: ${{ github.event_name == 'workflow_dispatch' && inputs.record_json"
	relSeeds(t, relWorkflowPath, workflowViolations, map[string][2]string{
		"sets if or continue-on-error":               {verify, verify + "        continue-on-error: true\n"},
		"if or continue-on-error":                    {"      - name: Create the public release with exactly the verified files\n", "      - name: Create the public release with exactly the verified files\n        if: always()\n"},
		"job promote widens":                         {"      attestations: write\n", "      attestations: write\n      checks: write\n"},
		"job promote widens ":                        {"      attestations: write\n", ""},
		"job snapshot widens":                        {"    name: goreleaser release --snapshot (signed, SBOM'd)\n", "    name: goreleaser release --snapshot (signed, SBOM'd)\n    permissions:\n      id-token: write\n"},
		"job install-e2e widens":                     {"    name: install.sh end to end (${{ matrix.os }})\n", "    name: install.sh end to end (${{ matrix.os }})\n    permissions:\n      packages: write\n"},
		"permissions are not":                        {"      contents: write\n      packages: write\n", "      contents: read\n      packages: write\n"},
		"dispatch inputs":                            {"      notes:\n", "      extra:\n        type: string\n      notes:\n"},
		"reusable workflow or":                       {"  promote:\n    name: promote", "  leak:\n    uses: ./.github/workflows/pages-keys.yml\n    secrets: inherit\n\n  promote:\n    name: promote"},
		"dispatch inputs ":                           {"      record_json:\n", "      record_jsonx:\n"},
		"env holds a staging":                        {steps, strings.Replace(steps, "    steps:\n", "    env:\n      CASCADE_STAGING_REMOTE: ${{ secrets.CASCADE_STAGING_REMOTE }}\n    steps:\n", 1)},
		"other than rc-publish and promote step env": {"      - name: Create the public release with exactly the verified files\n        env:\n", "      - name: Create the public release with exactly the verified files\n        env:\n          X: ${{ secrets.CASCADE_STAGING_REMOTE }}\n"},
		"job verify step env holds":                  {"      - name: goreleaser check\n        run: goreleaser check\n", "      - name: goreleaser check\n        env:\n          X: ${{ secrets.CASCADE_STAGING_REMOTE }}\n        run: goreleaser check\n"},
		"rc-publish is not":                          {gate + " == '' }}", "    if: ${{ github.event_name == 'workflow_dispatch' }}"},
		"promote is not":                             {gate + " != '' }}\n", ""},
		"a push of tags":                             {"      - p1-integration\n", "      - p1-integration\n    tags:\n      - 'v*'\n"},
		"a step runs git push":                       {"gh release create \"$FINAL_TAG\"", "git push origin HEAD && gh release create \"$FINAL_TAG\""},
		"vars. or ::add-mask::":                      {"NOTES: ${{ inputs.notes }}", "NOTES: ${{ vars.NOTES }}"},
	})
}

// promoteGolden pins the whole promote job (every step, key, env and script).
// Any edit to the job changes the re-marshalled text and turns this red: read
// the printed lines, then regenerate the file from the new job on purpose.
const promoteGolden = "internal/build/testdata/promote-job.golden.yaml"

func promoteGoldenText(t *testing.T, text string) string {
	b, err := yaml.Marshal(relDecode[struct{ Jobs map[string]any }](t, text).Jobs["promote"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func promoteGoldenViolations(t *testing.T, text string) []string {
	var c relCheck
	got, want := promoteGoldenText(t, text), relRead(t, promoteGolden)
	diff := func(a, b string) (out []string) {
		for _, l := range strings.Split(a, "\n") {
			if !strings.Contains("\n"+b, "\n"+l+"\n") {
				out = append(out, l)
			}
		}
		return out
	}
	c.bad(got == want, fmt.Sprintf("promote job differs from %s: added %q removed %q", promoteGolden, diff(got, want), diff(want, got)))
	return c
}

func TestPromoteJobMatchesGolden(t *testing.T) {
	anchor := "      # Audit consistency only"
	relSeeds(t, relWorkflowPath, promoteGoldenViolations, map[string][2]string{
		"minisign.pub":     {anchor, "      - run: cp \"${RUNNER_TEMP}/k.pub\" docs/keys/minisign.pub\n" + anchor},
		"curl -sSfL":       {anchor, "      - run: curl -sSfL -o \"${RUNNER_TEMP}/release/x.tar.gz\" https://example.invalid/x\n" + anchor},
		"shell: true {0}":  {"      - name: Verify the signed set (first gate)\n", "      - name: Verify the signed set (first gate)\n        shell: true {0}\n"},
		"GOFLAGS":          {"      - name: Verify the signed set (first gate)\n        env:\n", "      - name: Verify the signed set (first gate)\n        env:\n          GOFLAGS: -exec=true\n"},
		"defaults":         {"    permissions:\n      contents: write\n", "    defaults:\n      run:\n        shell: true {0}\n    permissions:\n      contents: write\n"},
		"sort > /dev/null": {`| sort | cmp - "${RUNNER_TEMP}/record-images.txt"`, `| sort > /dev/null`},
		"rev >&3":          {`echo "promote: signed image index copied" >&3`, `echo "promote: signed image index copied" >&3; echo "$CASCADE_STAGING_REMOTE" | rev >&3`},
	})
}

func TestReleaseWorkflowRefusesWorkflowEnv(t *testing.T) {
	relSeeds(t, relWorkflowPath, workflowViolations, map[string][2]string{
		"can cancel a running promote": {"cancel-in-progress: ${{ github.event_name == 'push' }}", "cancel-in-progress: true"},
		"concurrency can cancel":       {"group: release-${{ github.event_name }}-${{ github.ref }}", "group: release"},
		"sets env or defaults":         {"permissions:\n  contents: read\n\nconcurrency:", "permissions:\n  contents: read\n\nenv:\n  GOFLAGS: -exec=true\n\nconcurrency:"},
	})
}
