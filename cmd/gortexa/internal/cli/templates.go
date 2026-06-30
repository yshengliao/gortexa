package cli

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// tmplData parameterizes the proto/logic code templates. Names are derived once
// (see parseTarget) so every artifact agrees on casing.
type tmplData struct {
	Module      string // go module path, e.g. github.com/acme/app
	Domain      string // proto package domain, e.g. billing
	Version     string // proto package version, e.g. v1
	GoPkg       string // generated package alias, e.g. billingv1
	Entity      string // CamelCase entity, e.g. Invoice
	Snake       string // snake_case entity, e.g. invoice
	Plural      string // CamelCase plural, e.g. Invoices
	PluralSnake string // snake_case plural, e.g. invoices
}

// renderTemplate executes an embedded template with "[[" "]]" delimiters so the
// many literal "{ }" braces in proto/Go source pass through untouched.
func renderTemplate(name string, d tmplData) ([]byte, error) {
	src, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Delims("[[", "]]").Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("execute template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}
