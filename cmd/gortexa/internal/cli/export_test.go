package cli

import "testing"

func TestExportRejectsUnknownFormat(t *testing.T) {
	cmd := newExportCmd()
	cmd.SetArgs([]string{"--format", "yaml"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want an error for an unknown --format")
	}
}
