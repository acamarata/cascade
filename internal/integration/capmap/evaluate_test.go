//go:build capmap

package capmap

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The six columns of the capability-map table, in order. The header row
// must match them exactly.
var tableHeader = []string{"capability", "entrypoint", "production_path", "classification", "owner", "evidence"}

// Classification values. verified needs a probe that passed in the same
// run; every other class needs an owner.
const (
	classVerified    = "verified"
	classUnverified  = "present-unverified"
	classMissing     = "missing"
	classRefusedSpec = "policy-refused-as-specified"
)

var knownClasses = map[string]bool{
	classVerified: true, classUnverified: true, classMissing: true, classRefusedSpec: true,
}

// ownerPattern accepts a planning ticket id (P<phase>-<EPIC>-<NN>) or a
// request for a new one (NEW:P1-<CODE>). "-" means "no owner" and never
// matches.
var ownerPattern = regexp.MustCompile(`^(NEW:P1-[A-Z][A-Z0-9]*|P[0-9]+-[A-Z][A-Z0-9]*-[0-9]+)$`)

// probeRefPattern finds probe names inside an evidence cell. \b keeps a
// longer name such as TestCapmap_A_b from yielding the prefix TestCapmap_A.
var probeRefPattern = regexp.MustCompile(`\bTestCapmap_[A-Za-z0-9]+\b`)

var separatorCell = regexp.MustCompile(`^:?-{3,}:?$`)

// findingCode names one kind of table defect.
type findingCode string

const (
	findNoTable               findingCode = "no-table"
	findSecondTable           findingCode = "second-table"
	findBadHeader             findingCode = "bad-header"
	findBadSeparator          findingCode = "bad-separator"
	findBadRow                findingCode = "bad-row"
	findEmptyCell             findingCode = "empty-cell"
	findNoCapabilityIDs       findingCode = "no-capability-ids"
	findUnknownCapability     findingCode = "unknown-capability"
	findMissingCapability     findingCode = "missing-capability"
	findUnknownClassification findingCode = "unknown-classification"
	findNoProbeNamed          findingCode = "no-probe-named"
	findProbeNotPassed        findingCode = "probe-not-passed"
	findMissingOwner          findingCode = "missing-owner"
)

// finding is one defect. Line is the 1-based table line, 0 when the defect
// is about the table as a whole.
type finding struct {
	Code       findingCode
	Line       int
	Capability string
	Detail     string
}

// String renders the finding for a failure message.
func (f finding) String() string {
	where := "table"
	if f.Line > 0 {
		where = fmt.Sprintf("line %d", f.Line)
	}
	return fmt.Sprintf("%s (%s): %s %s", f.Code, where, f.Capability, f.Detail)
}

// row is one parsed table row.
type row struct {
	line                                                           int
	capability, entrypoint, productionPath, class, owner, evidence string
}

// findingsError turns findings into one error of the invalid-input kind,
// or nil for none.
func findingsError(fs []finding) error {
	if len(fs) == 0 {
		return nil
	}
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.String()
	}
	return cascade.Newf(cascade.KindInvalidInput, "capability map: %d finding(s):\n  %s",
		len(fs), strings.Join(parts, "\n  "))
}

// evaluate checks the capability-map table text against the capability ids
// and the set of probe names that passed in this run. It is pure: no file,
// network or registry access. It returns every finding, sorted; none means
// the table is complete and honest.
func evaluate(table string, capabilityIDs []string, passed map[string]bool) []finding {
	rows, fs := parseTable(table)
	fs = append(fs, checkCapabilities(rows, capabilityIDs)...)
	for _, r := range rows {
		fs = append(fs, checkRow(r, passed)...)
	}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Capability+a.Detail < b.Capability+b.Detail
	})
	return fs
}

// checkCapabilities reports capability cells that are not in the list and
// listed ids that no row covers.
func checkCapabilities(rows []row, ids []string) []finding {
	if len(ids) == 0 {
		return []finding{{Code: findNoCapabilityIDs, Detail: "the capability id list is empty"}}
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	covered := map[string]bool{}
	var fs []finding
	for _, r := range rows {
		covered[r.capability] = true
		if r.capability != "" && !known[r.capability] {
			fs = append(fs, finding{Code: findUnknownCapability, Line: r.line, Capability: r.capability,
				Detail: "is not in the capability id list"})
		}
	}
	for _, id := range ids {
		if !covered[id] {
			fs = append(fs, finding{Code: findMissingCapability, Capability: id, Detail: "has no row"})
		}
	}
	return fs
}

// checkRow applies the classification rules to one row: a known class, a
// passed probe for verified, an owner for everything else.
func checkRow(r row, passed map[string]bool) []finding {
	if r.class == "" {
		return nil // the empty cell is already reported by parseTable
	}
	if !knownClasses[r.class] {
		return []finding{{Code: findUnknownClassification, Line: r.line, Capability: r.capability, Detail: r.class}}
	}
	if r.class != classVerified {
		if ownerPattern.MatchString(r.owner) {
			return nil
		}
		return []finding{{Code: findMissingOwner, Line: r.line, Capability: r.capability,
			Detail: fmt.Sprintf("%s row needs an owner id, got %q", r.class, r.owner)}}
	}
	names := probeNames(r.evidence)
	if len(names) == 0 {
		return []finding{{Code: findNoProbeNamed, Line: r.line, Capability: r.capability,
			Detail: "verified row names no TestCapmap_ probe in evidence"}}
	}
	var fs []finding
	for _, n := range names {
		if !passed[n] {
			fs = append(fs, finding{Code: findProbeNotPassed, Line: r.line, Capability: r.capability,
				Detail: n + " did not pass (unregistered, failed or skipped)"})
		}
	}
	return fs
}

// probeNames returns the distinct TestCapmap_ names in an evidence cell,
// sorted.
func probeNames(evidence string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range probeRefPattern.FindAllString(evidence, -1) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// verifiedProbeNames returns the distinct probe names that verified rows
// name, sorted. These are the probes TestCapmapTable_Evaluates runs.
func verifiedProbeNames(rows []row) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.class != classVerified {
			continue
		}
		for _, n := range probeNames(r.evidence) {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}
