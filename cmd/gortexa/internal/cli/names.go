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
