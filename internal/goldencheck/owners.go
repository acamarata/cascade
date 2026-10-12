package goldencheck

// Row is one golden-parity lane row: the golden root it guards, the package
// whose tests read that root, and the `go test -run` regex that selects the
// owner tests. Root is a module-relative slash path.
type Row struct {
	Root     string `json:"root"`
	Package  string `json:"package"`
	RunRegex string `json:"run_regex"`
}

// tripwirePackage and tripwireRegex are shared by the four migration
// sub-roots: one checksum test covers all of them, one row per root.
const (
	tripwirePackage = "./internal/migration/tripwire"
	tripwireRegex   = "^TestTripwireStable$"
)

// Owners is the committed owner table (contract: golden-owner-table). A new
// golden root anywhere in the tree adds its row here in the same change; the
// unmapped-root failure in Resolve enforces it. Every regex names the owner
// tests exactly, so a renamed owner test fails the lane instead of vanishing.
var Owners = []Row{
	{"internal/context/testdata/v1-goldens", "./internal/context", "^(TestDiscoverGolden|TestMergeGolden|TestCCGolden|TestHarnessWritersMatchTheProvenancedGoldens)$"},
	{"internal/conversation/testdata/v1-goldens", "./internal/conversation", "^(TestScrub_RealRPCPath_DetectsVaultsRewrites|TestScrub_RealRPCPath_MultiSpan)$"},
	{"internal/daemon/testdata/v1-goldens", "./internal/fleet/capacity", "^TestWidgetComposeV1GoldenParity$"},
	{"internal/mcp/coretools/testdata/v1-goldens", "./internal/mcp/coretools", "^TestMCPToolV1GoldenParity$"},
	{"internal/mcp/testdata/goldens/claude-code", "./internal/mcp", "^(TestTheRealClientsSessionReplays|TestTheBaselineFailureIsStillRecorded)$"},
	{"internal/memory/testdata/v1-goldens", "./internal/memory", "^(TestGolden_Decode|TestGolden_Encode)$"},
	{"internal/memory/testdata/v1-goldens/migration", tripwirePackage, tripwireRegex},
	{"internal/providers/registry/testdata/v1-goldens/migration", tripwirePackage, tripwireRegex},
	{"internal/retrieval/eval/testdata/v1-goldens", "./internal/retrieval/eval", "^(TestLoadBaseline_Real|TestV1August2026Baseline)$"},
	{"internal/retrieval/rrf/testdata/v1-goldens", "./internal/retrieval/rrf", "^TestFuse_V1Goldens$"},
	{"internal/retrieval/testdata/v1-goldens", "./internal/retrieval", "^(TestGoldenGo|TestGoldenMarkdown|TestGoldenOtherLang|TestGoldenFTS5|TestGoldenFTS5IsDeterministic|TestDemoteSupersededAndExpired)$"},
	{"internal/runtime/testdata/v1-goldens/migration", tripwirePackage, tripwireRegex},
	{"internal/secrets/testdata/v1-goldens/migration", tripwirePackage, tripwireRegex},
	{"plugins/claude/testdata/v1-goldens", "./plugins/claude", "^TestCascadeClaudeMCPRegistration$"},
	{"plugins/pbd/internal/pews/testdata/v1-goldens", "./plugins/pbd", "^TestArchivedValidatorFixtureParity$"},
}

// Excluded lists the golden roots that no test reads, each with its reason.
// A root listed here needs no Owners row.
var Excluded = map[string]string{
	"internal/migration/v1/testdata/v1-goldens": "harvest input evidence pinned by INPUTS.sha256",
	"internal/conductor/testdata/v1-goldens":    "corroboration record no test reads (its README)",
}
