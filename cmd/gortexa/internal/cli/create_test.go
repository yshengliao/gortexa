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
