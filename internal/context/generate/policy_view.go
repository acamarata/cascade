package generate

// Purpose: the generation policy view (contract:generation-policy-view).
//   BuildView projects the stored inventory and the ledger's accepted facts
//   into an inert, per-scope value the rules renderers read as data.
// Inputs: a repo.Inventory, the repository root and a FactSource whose
//   AcceptedFacts is the only fact read.
// Outputs: a View; KindInvalidInput for a nil source or an inventory with
//   no repository id; the source's own error otherwise.
// Constraints: the view carries only the seven closed fact subjects as
//   values and has no field that could govern gates, risk, capabilities,
//   egress or autonomy. Each accepted fact passes repo.ProjectPolicy again
//   here, so a source that hands back a denylisted fact still lands it in
//   Denied and never in a scope. P1 has exactly one scope, the root.
// SPORT: context-engine/generation-policy-view (ADD, P1-GEN-06).

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// FactSource is the one fact read the view makes: accepted facts only,
// with the ids the projection refused.
type FactSource interface {
	AcceptedFacts(ctx context.Context, repoID string) ([]repo.AcceptedFact, []string, error)
}

// LanguageView is one detected language and its commands, from the
// detector inventory only.
type LanguageView struct {
	Language, Build, Test, Lint string
}

// FactView is one accepted fact as an inert value.
type FactView struct {
	Subject repo.FactSubject
	Value   string
	Truth   repo.FactTruth
}

// ScopeView is one path scope: its slug, its repository-relative path
// ("." for the root), its languages and its facts.
type ScopeView struct {
	Slug, Rel string
	Languages []LanguageView
	Facts     []FactView
}

// View is the whole inert policy view of one repository.
type View struct {
	RepoRoot, RepositoryID string
	CI                     repo.CIFacts
	Harness                repo.HarnessFacts
	Scopes                 []ScopeView
	Denied                 []string
}

// slugRun matches each run of characters a slug may not hold.
var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

// ScopeSlug turns a repository-relative path into a rules file slug:
// lowercase, every run of [^a-z0-9] collapsed to "-", trimmed of "-".
// "." and "" are "root".
func ScopeSlug(rel string) string {
	if rel == "." || rel == "" {
		return "root"
	}
	s := strings.Trim(slugRun.ReplaceAllString(strings.ToLower(rel), "-"), "-")
	if s == "" {
		return "root"
	}
	return s
}

// BuildView builds the View for inv. Facts appear only for subjects the
// inventory leaves empty, sorted by subject; Denied is sorted and unique.
func BuildView(ctx context.Context, inv repo.Inventory, repoRoot string, facts FactSource) (View, error) {
	if facts == nil {
		return View{}, cascade.New(cascade.KindInvalidInput, "generate: nil fact source")
	}
	id := inv.Repository.ID
	if id == "" {
		return View{}, cascade.New(cascade.KindInvalidInput, "generate: inventory has no repository id")
	}
	if err := ctx.Err(); err != nil {
		return View{}, cascade.Wrap(cascade.KindCanceled, err, "generate: build view canceled")
	}
	accepted, denied, err := facts.AcceptedFacts(ctx, id)
	if err != nil {
		return View{}, err
	}
	fvs, refused, err := projectFacts(inv, accepted)
	if err != nil {
		return View{}, err
	}
	root := ScopeView{Slug: ScopeSlug("."), Rel: ".", Languages: languageViews(inv), Facts: fvs}
	return View{
		RepoRoot: repoRoot, RepositoryID: id, CI: inv.CI, Harness: inv.Harness,
		Scopes: []ScopeView{root}, Denied: uniqueSorted(append(append([]string(nil), denied...), refused...)),
	}, nil
}

// projectFacts keeps the accepted facts for subjects inv leaves empty. A
// fact outside the closed enum or refused by repo.ProjectPolicy goes into
// refused by id.
func projectFacts(inv repo.Inventory, accepted []repo.AcceptedFact) ([]FactView, []string, error) {
	var out []FactView
	var refused []string
	for _, f := range accepted {
		if !f.Subject.Valid() {
			refused = append(refused, f.ID)
			continue
		}
		if _, err := repo.ProjectPolicy([]repo.InferredFact{asInferred(inv.Repository.ID, f)}, inv.Repository.ID); err != nil {
			if k, ok := cascade.KindOf(err); ok && k == cascade.KindPolicyDenied {
				refused = append(refused, f.ID)
				continue
			}
			return nil, nil, err
		}
		if covered(inv, f.Subject) {
			continue
		}
		out = append(out, FactView{Subject: f.Subject, Value: f.Fact, Truth: f.Truth})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Value < out[j].Value
	})
	return out, refused, nil
}

// asInferred restates an accepted fact as the ledger row ProjectPolicy
// judges, so the view applies the same denylist the ledger did.
func asInferred(repoID string, f repo.AcceptedFact) repo.InferredFact {
	src := string(f.Provenance)
	if src == "" {
		src = string(provider.ProvenanceUntrustedSource)
	}
	version := f.Version
	if version < 1 {
		version = 1
	}
	return repo.InferredFact{
		ID: f.ID, RepositoryID: repoID, Subject: f.Subject, Fact: f.Fact, Source: src,
		Confidence: 1, Version: version, AcceptedBy: f.AcceptedBy, State: repo.FactAccepted,
	}
}

// covered reports whether the detector inventory already holds subject s.
func covered(inv repo.Inventory, s repo.FactSubject) bool {
	switch s {
	case repo.SubjectLanguages:
		return anyLang(inv, func(repo.LanguageFacts) bool { return true })
	case repo.SubjectBuildCmd:
		return anyLang(inv, func(l repo.LanguageFacts) bool { return l.Commands.Build != "" })
	case repo.SubjectTestCmd:
		return anyLang(inv, func(l repo.LanguageFacts) bool { return l.Commands.Test != "" })
	case repo.SubjectLintCmd:
		return anyLang(inv, func(l repo.LanguageFacts) bool { return l.Commands.Lint != "" })
	case repo.SubjectLayout:
		return inv.Layout.FileCount > 0
	case repo.SubjectCIPresence:
		return inv.CI.GitHubActions || inv.CI.GitLabCI || inv.CI.CircleCI
	case repo.SubjectHarnessFiles:
		return inv.Harness.ClaudeMD || inv.Harness.AgentsMD || inv.Harness.ClaudeDir
	}
	return false
}

// anyLang reports whether a detected language satisfies ok.
func anyLang(inv repo.Inventory, ok func(repo.LanguageFacts) bool) bool {
	for _, l := range inv.Languages {
		if l.Detected && ok(l) {
			return true
		}
	}
	return false
}

// languageViews lists the detected languages, sorted by name.
func languageViews(inv repo.Inventory) []LanguageView {
	var out []LanguageView
	for _, l := range inv.Languages {
		if l.Detected {
			out = append(out, LanguageView{Language: string(l.Language), Build: l.Commands.Build, Test: l.Commands.Test, Lint: l.Commands.Lint})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}

// uniqueSorted sorts ids and drops duplicates and empties.
func uniqueSorted(ids []string) []string {
	sort.Strings(ids)
	var out []string
	for _, id := range ids {
		if id != "" && (len(out) == 0 || out[len(out)-1] != id) {
			out = append(out, id)
		}
	}
	return out
}
