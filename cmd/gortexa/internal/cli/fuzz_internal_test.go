package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParseTarget fuzzes the domain/version+entity parser feeding directly
// into filepath.Join(root, "proto", d.Domain, d.Version, d.Snake+".proto") and
// filepath.Join(root, "internal", "logic", d.Snake+".go") in gen.go with no
// further sanitization -- a successful parse must never let Domain/Version/
// Snake/GoPkg carry a path separator or a ".."/"." traversal segment.
func FuzzParseTarget(f *testing.F) {
	seeds := []struct{ target, entity string }{
		{"billing/v1", ""}, {"billing/v1", "Invoice"}, {"../etc/v1", ""}, {"billing/../v1", ""},
		{"billing/v1/extra", ""}, {"billing", ""}, {"/v1", ""}, {"billing/v1", "../Invoice"},
		{"billing/v1", "Invoice/../../etc"}, {"billing/v1", "S3bucket"}, {".", "v1"},
		{"billing/v1", strings.Repeat("A", 5000)},
	}
	for _, s := range seeds {
		f.Add(s.target, s.entity)
	}

	f.Fuzz(func(t *testing.T, target, entity string) {
		d, err := parseTarget(target, entity)
		if err != nil {
			return
		}
		for _, field := range []struct{ name, val string }{
			{"Domain", d.Domain}, {"Version", d.Version}, {"Snake", d.Snake}, {"GoPkg", d.GoPkg},
		} {
			if strings.ContainsAny(field.val, `/\`) {
				t.Fatalf("parseTarget(%q,%q) produced %s=%q containing a path separator", target, entity, field.name, field.val)
			}
			if field.val == "." || field.val == ".." {
				t.Fatalf("parseTarget(%q,%q) produced %s=%q, a traversal segment", target, entity, field.name, field.val)
			}
			joined := filepath.Join("/root", field.val)
			if !strings.HasPrefix(joined, "/root") {
				t.Fatalf("parseTarget(%q,%q) produced %s=%q that escapes the join root: %q", target, entity, field.name, field.val, joined)
			}
		}
	})
}

// FuzzValidModulePath pins the guard that runs before a module path is
// rewritten across every file in a freshly scaffolded project. An accepted
// path must be safe to embed in generated source and in a go.mod line: no
// separator-adjacent traversal segments, no whitespace, no scheme separator.
func FuzzValidModulePath(f *testing.F) {
	for _, s := range []string{
		"", ".", "..", "github.com/me/myapp", "github.com/me/my-app_v2~x",
		"./relative", "../escape", "a//b", "a/./b", "a/../b", "has space",
		"has\ttab", "scheme://host/path", "user@host/path", "a/b/",
		"UPPER/Case123", strings.Repeat("a", 4096),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, module string) {
		if !validModulePath(module) {
			return
		}
		if module == "" {
			t.Fatal("validModulePath accepted the empty module path")
		}
		if strings.ContainsAny(module, " \t\n:@") {
			t.Fatalf("validModulePath accepted whitespace or a scheme separator: %q", module)
		}
		for seg := range strings.SplitSeq(module, "/") {
			switch seg {
			case "":
				t.Fatalf("validModulePath accepted an empty segment: %q", module)
			case ".", "..":
				t.Fatalf("validModulePath accepted a traversal segment: %q", module)
			}
			for _, r := range seg {
				switch {
				case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				case r == '.' || r == '-' || r == '_' || r == '~':
				default:
					t.Fatalf("validModulePath accepted disallowed rune %q in %q", r, module)
				}
			}
		}
	})
}
