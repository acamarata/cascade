package repo

// Purpose: the compiled-in templates the agent, skill and harness workflow
//   generators read (contract:compiled-templates). The tree is embedded at build time, so
//   no template is ever read from a target repository, a config path, a
//   plugin or the network.
// Inputs: none at run time.
// Outputs: TemplateFS, the read-only embedded tree, and TemplateHashes,
//   the sha256 of every embedded file.
// Constraints: only templates/agents, templates/skills and
//   templates/workflows are embedded; templates/wiki-sync-workflow.yml
//   stays out. A later generator appends its own directory to the embed
//   line.
// SPORT: context-engine/compiled-templates (ADD, P1-GEN-08; workflows
//   P1-GEN-09).

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
)

//go:embed templates/agents templates/skills templates/workflows
var templateFS embed.FS

// TemplateFS returns the embedded template tree. Paths are slash-separated
// and start with "templates/".
func TemplateFS() fs.FS { return templateFS }

// TemplateHashes maps every embedded file's path to the lowercase hex
// sha256 of its bytes. It holds every embedded file and nothing else.
func TemplateHashes() map[string]string {
	return hashTree(templateFS)
}

// hashTree hashes every regular file of fsys. An embed.FS walk cannot fail
// on its own tree, so a walk error yields the files hashed so far.
func hashTree(fsys fs.FS) map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[p] = hex.EncodeToString(sum[:])
		return nil
	})
	return out
}
