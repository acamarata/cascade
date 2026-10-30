// Purpose: the committed integration-lane registry (contract:
// integration-lane-registry) and the CI vocabulary CheckLaneCoverage checks it
// against. Every package with an integration-constrained test file is a row in
// IntegrationLanes or an entry in IntegrationExcluded with a reason; a new one
// adds its row in the same change (TestIntegrationLanesCoverTree enforces it).

package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Runner names a row may list (the ci.yml matrix values).
const (
	RunnerLinux   = "ubuntu-latest"
	RunnerMacOS   = "macos-15"
	RunnerWindows = "windows-latest"
)

// ci.yml job names the gate refers to.
const (
	LaneJob      = "integration-lanes"
	RedProofJob  = "integration-lanes-red-proof"
	RedAssertJob = "integration-lanes-red-assert"
	CIGateJob    = "ci-gate"
)

// LaneRow is one integration lane row. Job LaneJob rows are run by the
// integration-lanes job (`go test -json -count=1 -tags Tags -run RunRegex
// Package`); any other Job names an existing ci.yml job whose run step must
// contain Package and Tags (checked, RunRegex unused). SkipOn maps a runner to
// the gate message every skipped test on that runner must carry.
type LaneRow struct {
	Package, Tags, RunRegex, Job string
	OS                           []string
	SkipOn                       map[string]string
}

// SeededRedRow is the seeded failing row run by the expected-to-fail
// red-proof job (R18 M7). It is never a registry row.
var SeededRedRow = LaneRow{"./internal/build/testdata/integrationlanes/seeded", "integration seededred", "^TestSeededRed$", RedProofJob, []string{RunnerLinux}, nil}

const harnessSkip = "CASCADE_TEST_CLAUDE_BINARY not set" // the gate message of a test needing an installed harness
// Runner sets: allRunners is the matrix of the integration-lanes job.
var (
	allRunners  = []string{RunnerLinux, RunnerMacOS, RunnerWindows}
	unixRunners = []string{RunnerLinux, RunnerMacOS}
	macOnly     = []string{RunnerMacOS}
	linuxOnly   = []string{RunnerLinux}
)

// rows copies the template row once per package.
func rows(t LaneRow, pkgs ...string) []LaneRow {
	out := make([]LaneRow, len(pkgs))
	for i, p := range pkgs {
		t.Package = p
		out[i] = t
	}
	return out
}

// winSkip declares the gate message every skipped test on windows must carry.
func winSkip(msg string) map[string]string { return map[string]string{RunnerWindows: msg} }

// IntegrationLanes is the committed registry (see the file comment). A lane
// regex of "." runs the whole package under the integration tags.
var IntegrationLanes = slices.Concat(
	[]LaneRow{
		{"./cmd/cascade", "integration", "^(TestMCPSocketServeAcceptsOwnerPeer|TestMCPSocketServeRefusesOriginHeader|TestFleetCapacityTopicOwnerOnly)$", LaneJob, unixRunners, nil},
		{"./internal/client", "integration", "^TestCLIClientPassesLocalRequestGuard$", LaneJob, unixRunners, nil},
	},
	rows(LaneRow{Tags: "integration", RunRegex: ".", Job: LaneJob, OS: unixRunners}, "./internal/backup",
		"./internal/backup/targets", "./internal/conductor", "./internal/elevation", "./internal/fleet", "./internal/fleet/journal",
		"./internal/fleet/sessions", "./internal/hooks/egress", "./internal/minisign", "./internal/plugins/host",
		"./internal/plugins/registryfetch", "./internal/plugins/remote", "./internal/providers/agents/egressproxy",
		"./internal/providers/registry", "./internal/retrieval", "./internal/retrieval/recall", "./internal/rpc", "./internal/runtime", "./internal/runtime/init"),
	rows(LaneRow{Tags: "integration", RunRegex: ".", Job: LaneJob, OS: allRunners, SkipOn: winSkip("daemon unix socket: Windows tier-2")},
		"./internal/memory"),
	rows(LaneRow{Tags: "integration", RunRegex: ".", Job: LaneJob, OS: allRunners, SkipOn: winSkip("skip: ")}, "./providers/anthropic",
		"./providers/embeddings", "./providers/gemini", "./providers/ollama", "./providers/openai", "./providers/transport"),
	rows(LaneRow{Tags: "integration", RunRegex: ".", Job: LaneJob, OS: macOnly}, "./internal/migration"),
	// acceptance is linux-only: its elevation drill stores a key in the Keychain, which an unsigned test binary cannot do (OSStatus -34018).
	rows(LaneRow{Tags: "integration", RunRegex: ".", Job: LaneJob, OS: linuxOnly}, "./internal/acceptance"),
	rows(LaneRow{Tags: "integration", RunRegex: ".", Job: LaneJob, OS: unixRunners, SkipOn: map[string]string{RunnerLinux: harnessSkip, RunnerMacOS: harnessSkip}}, "./providers/agents/claude"),
	// the driver spawn proxy tests carry the security tag as well; a bare integration build compiles none of them
	[]LaneRow{{"./internal/providers/agents", "integration security", "^(TestProxiedDialEffectRecordsIntentConfirm|TestSpawnProxyCloseDuringDialFailsTheEffect|TestSpawnProxyAbsentTableDeniesEveryDestination|TestRedTeamNetworkRefusedByBroker|TestSpawnProxyCredentialsDifferPerSpawn|TestSpawnProxyClosedAtReleaseRefusesConnect)$", LaneJob, unixRunners, nil}},
	rows(LaneRow{Tags: "integration", Job: "build-test", OS: unixRunners}, "./internal/conversation", "./internal/daemon",
		"./internal/integration", "./internal/plugins", "./internal/secrets"),
	rows(LaneRow{Tags: "integration", Job: "node-dispatch-real-git", OS: linuxOnly}, "./internal/nodes"),
	rows(LaneRow{Tags: "integration", Job: "sync-merge-real-git", OS: linuxOnly}, "./internal/sync"),
	rows(LaneRow{Tags: "integration", Job: "plugin-integration", OS: linuxOnly}, "./plugins/cascade-pa",
		"./plugins/cascade-pa/telegram", "./plugins/github", "./plugins/github/tools", "./plugins/pbd"),
	rows(LaneRow{Tags: "postgres integration", Job: "storetest-under-docker", OS: linuxOnly}, "./providers/postgres"),
	rows(LaneRow{Tags: "postgres integration", Job: "pgvector-storetest-under-docker", OS: linuxOnly}, "./providers/pgvector"),
	rows(LaneRow{Tags: "integration", Job: "redis-storetest-under-docker", OS: linuxOnly}, "./providers/redis"),
	rows(LaneRow{Tags: "integration", Job: "s3-storetest-under-docker", OS: linuxOnly}, "./providers/s3"),
)

// ciWorkflow is the slice of a workflow file the gate reads.
type ciWorkflow struct {
	Jobs map[string]ciJob `yaml:"jobs"`
}

// ciJob is one workflow job; ContinueOnError is any because a workflow may
// write a bool or an expression.
type ciJob struct {
	Needs           ciNeeds  `yaml:"needs"`
	If              string   `yaml:"if"`
	ContinueOnError any      `yaml:"continue-on-error"`
	Steps           []ciStep `yaml:"steps"`
}

// ciStep is one job step.
type ciStep struct {
	Name string `yaml:"name"`
	If   string `yaml:"if"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

// ciNeeds is a job's `needs`, written as a scalar or a list.
type ciNeeds []string

// UnmarshalYAML accepts both shapes of `needs`.
func (n *ciNeeds) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = ciNeeds{v.Value}
		return nil
	}
	var l []string
	if err := v.Decode(&l); err != nil {
		return err
	}
	*n = l
	return nil
}

// softFail reports whether the job may fail without failing the workflow.
func softFail(j ciJob) bool {
	switch v := j.ContinueOnError.(type) {
	case nil:
		return false
	case bool:
		return v
	}
	return true
}

var tagsFlag = regexp.MustCompile(`-tags[= ]\s*("[^"]*"|'[^']*'|[^\s"']+)`)

// runsPackage reports whether one step of j names the row's package (exactly
// or under a ./dir/... pattern) and a -tags flag carrying every row tag. A
// step that `if:`-excludes windows does not satisfy a row listing it.
func runsPackage(j ciJob, r LaneRow) bool {
	for _, s := range j.Steps {
		if !stepHasPackage(s.Run, r.Package) || !stepHasTags(s.Run, r.Tags) {
			continue
		}
		if slices.Contains(r.OS, RunnerWindows) && strings.Contains(s.If, "!= 'windows'") {
			continue
		}
		return true
	}
	return false
}

// stepHasPackage matches pkg against the tokens of a run script.
func stepHasPackage(run, pkg string) bool {
	for _, t := range strings.FieldsFunc(run, func(c rune) bool { return strings.ContainsRune(" \t\n\\\"'", c) }) {
		t = strings.TrimSuffix(t, "/")
		if t == pkg || (strings.HasSuffix(t, "/...") && strings.HasPrefix(pkg+"/", strings.TrimSuffix(t, "...")) && t != "./...") {
			return true
		}
	}
	return false
}

// stepHasTags reports whether some -tags flag in run carries every tag.
func stepHasTags(run, tags string) bool {
	for _, m := range tagsFlag.FindAllStringSubmatch(run, -1) {
		have := strings.FieldsFunc(strings.Trim(m[1], `"'`), func(c rune) bool { return c == ' ' || c == ',' })
		if !slices.ContainsFunc(strings.Fields(tags), func(t string) bool { return !slices.Contains(have, t) }) {
			return true
		}
	}
	return false
}

// alwaysIf reports whether an `if:` is the bare always() condition.
func alwaysIf(cond string) bool {
	c := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(cond), "${{"), "}}"))
	return c == "always()"
}

// redProofProblems checks the R18 M7 shape: the lane job and the assert job
// block ci-gate; the red-proof job is expected to fail, so it is never in
// ci-gate needs and never soft-fails, and its report upload runs even then.
func redProofProblems(wf ciWorkflow) []string {
	var out []string
	add := func(m string) { out = append(out, "red proof: "+m) }
	gate := wf.Jobs[CIGateJob].Needs
	if lane, ok := wf.Jobs[LaneJob]; !ok || !slices.Contains(gate, LaneJob) || softFail(lane) {
		add(LaneJob + " must exist, be in ci-gate needs and not have continue-on-error")
	}
	if proof, ok := wf.Jobs[RedProofJob]; !ok {
		add(RedProofJob + " job is missing")
	} else {
		uploads := 0
		for _, s := range proof.Steps {
			if strings.HasPrefix(s.Uses, "actions/upload-artifact") {
				uploads++
				if !alwaysIf(s.If) {
					add(RedProofJob + " upload step lacks if: always()")
				}
			}
		}
		if uploads == 0 {
			add(RedProofJob + " has no report upload step")
		}
		if softFail(proof) {
			add(RedProofJob + " has continue-on-error")
		}
		if slices.Contains(gate, RedProofJob) {
			add(RedProofJob + " must not be in ci-gate needs (it is expected to fail)")
		}
	}
	assert, ok := wf.Jobs[RedAssertJob]
	switch {
	case !ok:
		add(RedAssertJob + " job is missing")
	case !slices.Contains(assert.Needs, RedProofJob):
		add(RedAssertJob + " does not need " + RedProofJob)
	case !alwaysIf(assert.If):
		add(RedAssertJob + " lacks if: always()")
	}
	if ok && !slices.Contains(gate, RedAssertJob) {
		add(RedAssertJob + " is not in ci-gate needs")
	}
	return out
}

// redReport is the slice of the lane report AssertRedProof reads (the JSON the
// lane tool writes from goldencheck.Aggregate).
type redReport struct {
	Rows []struct {
		Package     string   `json:"package"`
		Run         int      `json:"run"`
		Failed      int      `json:"failed"`
		FailedTests []string `json:"failed_tests"`
		OK          bool     `json:"ok"`
		Reason      string   `json:"reason"`
	} `json:"rows"`
}

// AssertRedProof accepts a red-proof report only when the seeded row is the
// single row, ran at least one test, and failed through TestSeededRed alone.
// A pass, a build failure, a tag or regex mismatch (zero run events), a
// different failing test or any further row is refused, so the expected-to-fail
// job cannot go green for the wrong reason.
func AssertRedProof(data []byte) error {
	var rep redReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return fmt.Errorf("red-proof report does not parse: %w", err)
	}
	failing := 0
	for _, r := range rep.Rows {
		if !r.OK {
			failing++
		}
	}
	switch {
	case len(rep.Rows) == 0:
		return errors.New("red-proof report has no rows")
	case failing > 1:
		return fmt.Errorf("red-proof report has %d failing rows, want only the seeded one", failing)
	case len(rep.Rows) > 1:
		return errors.New("red-proof report holds rows besides the seeded row")
	}
	r := rep.Rows[0]
	switch {
	case r.Package != SeededRedRow.Package:
		return fmt.Errorf("red-proof report row is %s, want %s", r.Package, SeededRedRow.Package)
	case r.OK:
		return errors.New("seeded row passed, so the red proof did not fail")
	case r.Run == 0:
		return fmt.Errorf("seeded row ran no tests (build failure, tag or regex mismatch): %s", r.Reason)
	case r.Failed != 1 || len(r.FailedTests) != 1 || r.FailedTests[0] != "TestSeededRed":
		return fmt.Errorf("seeded row failed, but not through TestSeededRed alone: %s", r.Reason)
	}
	return nil
}
