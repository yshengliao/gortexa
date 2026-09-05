package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// manifestFile records what a project was scaffolded from. Nothing else in a
// generated project identifies its framework layout: the CLI's own source is
// pruned and the clone's .git is discarded, so without this file neither `gen`
// nor `doctor` can tell a namespaced project from a pre-v0.28 flat one, and a
// project created without it can never be told apart after the fact.
const manifestFile = ".gortexa/project.json"

type projectManifest struct {
	CLIVersion     string `json:"cli_version"`
	ModulePath     string `json:"module_path"`
	ProtoNamespace string `json:"proto_namespace"`
	SourceRepo     string `json:"source_repo"`
	SourceRef      string `json:"source_ref"`
}

func writeManifest(root string, m projectManifest) error {
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(manifestFile)), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, manifestFile), append(b, '\n'), 0o644)
}

// readManifest returns the project's manifest and whether one was found. A
// missing manifest is not an error: projects scaffolded before v0.28 have none
// and must keep working, which is exactly what the zero value expresses.
func readManifest(root string) (projectManifest, bool) {
	b, err := os.ReadFile(filepath.Join(root, manifestFile))
	if err != nil {
		return projectManifest{}, false
	}
	var m projectManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return projectManifest{}, false
	}
	return m, true
}

// reservedNamespaces are prefixes a project's protos must never land under,
// because something else already owns that directory or package:
//
//   - gortexa — the framework's own namespace, which regen excludes from the
//     project's generate step; a project inside it never gets generated.
//   - resource — the sample's own directory, which cannot be moved into itself.
//   - google, buf — the well-known namespaces buf resolves the proto deps from.
//
// A collision is not an error the user can act on (the namespace is derived from
// their module path), so suffix out of the way instead of failing.
var reservedNamespaces = map[string]bool{
	"buf":      true,
	"google":   true,
	"gortexa":  true,
	"resource": true,
}

// protoNamespace derives a proto package prefix from a Go module path, so a
// project's API lives in its own namespace instead of the layout's. Two projects
// scaffolded from gortexa both declared resource.v1 at resource/v1/resource.proto,
// which made them impossible to link into one binary — protobuf's global registry
// is keyed on exactly those two things.
func protoNamespace(module string) string {
	last := module
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(last) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && b.Len() > 0:
			// A proto package component may not start with a digit.
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "app"
	}
	if ns := b.String(); !reservedNamespaces[ns] {
		return ns
	}
	return b.String() + "app"
}
