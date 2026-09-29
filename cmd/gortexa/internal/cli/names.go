package cli

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	domainRe  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	versionRe = regexp.MustCompile(`^v[0-9]+$`)
	entityRe  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// parseTarget parses a "<domain>/<version>" target plus an optional CamelCase
// entity into tmplData. Module is filled by the caller from go.mod. When entity
// is empty it defaults to the title-cased domain.
func parseTarget(target, entity string) (tmplData, error) {
	parts := strings.Split(target, "/")
	if len(parts) != 2 {
		return tmplData{}, fmt.Errorf("target must be <domain>/<version>, e.g. billing/v1 (got %q)", target)
	}
	domain, version := parts[0], parts[1]
	if !domainRe.MatchString(domain) {
		return tmplData{}, fmt.Errorf("invalid domain %q: must match [a-z][a-z0-9]*", domain)
	}
	if goSpecialDirs[domain] {
		return tmplData{}, fmt.Errorf("invalid domain %q: the Go toolchain gives that directory name special meaning, so the generated package could not be built or imported", domain)
	}
	if !versionRe.MatchString(version) {
		return tmplData{}, fmt.Errorf("invalid version %q: must match v[0-9]+", version)
	}
	if entity == "" {
		entity = strings.ToUpper(domain[:1]) + domain[1:]
	}
	if !entityRe.MatchString(entity) {
		return tmplData{}, fmt.Errorf("invalid entity %q: must be CamelCase ([A-Z][A-Za-z0-9]*)", entity)
	}
	if i := digitThenLowerIndex(entity); i >= 0 {
		return tmplData{}, fmt.Errorf("invalid entity %q: a digit followed by a lowercase letter (at %q) would be renamed by proto/Go camelization, breaking the generated identifiers — capitalize it (e.g. S3bucket → S3Bucket)", entity, entity[i:i+2])
	}
	snake := camelToSnake(entity)
	if strings.HasSuffix(snake, "_test") {
		return tmplData{}, fmt.Errorf("invalid entity %q: its snake_case form %q ends in _test, which would create a Go test file (internal/logic/%s.go)", entity, snake, snake)
	}
	if i := strings.LastIndexByte(snake, '_'); i >= 0 && (knownOS[snake[i+1:]] || knownArch[snake[i+1:]]) {
		return tmplData{}, fmt.Errorf("invalid entity %q: its snake_case form %q ends in a GOOS/GOARCH suffix, so Go would build internal/logic/%s.go and its generated files only for that platform", entity, snake, snake)
	}
	return tmplData{
		Domain:      domain,
		Version:     version,
		GoPkg:       domain + version,
		Entity:      entity,
		Snake:       snake,
		Plural:      entity + "s",
		PluralSnake: snake + "s",
	}, nil
}

// goSpecialDirs are directory names the Go toolchain treats specially: a
// gen/.../internal package is unimportable from cmd/server, and vendor and
// testdata directories are skipped by ./... patterns.
var goSpecialDirs = map[string]bool{"internal": true, "testdata": true, "vendor": true}

// knownOS and knownArch mirror go/build's filename constraint lists: a file
// named *_<GOOS>.go or *_<GOARCH>.go is compiled only for that platform.
var (
	knownOS = map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
		"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
		"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true,
	}
	knownArch = map[string]bool{
		"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true,
		"arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true,
		"mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
		"ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true,
		"sparc": true, "sparc64": true, "wasm": true,
	}
)

// digitThenLowerIndex returns the index of the first digit that is immediately
// followed by a lowercase ASCII letter, or -1. Such a pair does not survive
// proto/Go camelization unchanged (S3bucket → S3Bucket), so the generated type
// name would no longer match the template's [[.Entity]] reference.
func digitThenLowerIndex(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' && s[i+1] >= 'a' && s[i+1] <= 'z' {
			return i
		}
	}
	return -1
}

// camelToSnake converts CamelCase to snake_case (Invoice→invoice,
// InvoiceItem→invoice_item).
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
