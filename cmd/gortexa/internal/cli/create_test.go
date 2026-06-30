package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRewriteModulePath_IssueURLs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.md")
	content := "Visit https://github.com/yshengliao/gortexa/issues for help."
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteModulePath(root, "github.com/yshengliao/gortexa", "example.com/demo"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "Visit https://github.com/yshengliao/gortexa/issues for help." {
		t.Errorf("expected URL to NOT be rewritten, got %q", string(b))
	}
}

// TestRewriteModulePath_RewritesBareModuleButPreservesURL guards both directions
// of the https mask at once: the bare module path (go.mod directive, import paths)
// must still be rewritten, while an https:// link to the upstream repo must be
// preserved. A too-broad mask would fail the first assertion; a missing mask the
// second.
func TestRewriteModulePath_RewritesBareModuleButPreservesURL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mixed.go")
	content := "module github.com/yshengliao/gortexa\n" +
		"import \"github.com/yshengliao/gortexa/internal/logic\"\n" +
		"// docs: https://github.com/yshengliao/gortexa\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteModulePath(root, "github.com/yshengliao/gortexa", "example.com/demo"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "module example.com/demo\n" +
		"import \"example.com/demo/internal/logic\"\n" +
		"// docs: https://github.com/yshengliao/gortexa\n"
	if string(b) != want {
		t.Errorf("module path should rewrite while the URL is preserved\n got: %q\nwant: %q", string(b), want)
	}
}
