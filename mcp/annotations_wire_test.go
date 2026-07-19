package mcp

import (
	"testing"

	aiv1 "github.com/yshengliao/gortexa/gen/gortexa/ai/v1"
)

// TestAIExtensionNumbersAreStable pins the wire numbers of the ai annotation
// extensions. The v0.27 move of the proto from ai/v1 to gortexa/ai/v1 was sold
// as wire-compatible precisely because these numbers did not change; buf's
// FILE breaking ruleset does not detect an extension renumber, so this golden
// assertion is the guard. Changing either number is a wire break for every
// descriptor already annotated with the old numbers — do not edit to match a
// renumbered proto; renumber back.
func TestAIExtensionNumbersAreStable(t *testing.T) {
	if got := aiv1.E_AiTool.TypeDescriptor().Number(); got != 50001 {
		t.Errorf("ai_tool extension number = %d, want 50001 (wire-breaking change)", got)
	}
	if got := aiv1.E_AiField.TypeDescriptor().Number(); got != 50002 {
		t.Errorf("ai_field extension number = %d, want 50002 (wire-breaking change)", got)
	}
}
