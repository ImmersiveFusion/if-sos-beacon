package prompts

import (
	"strings"
	"testing"

	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

func TestBuildSystem(t *testing.T) {
	bc := core.BeaconContext{
		Beacon:  "sos-apm",
		Context: "DeepCube watches observability pain.",
		Buckets: []core.Bucket{
			{Name: "seeker", Emoji: "🙋", Definition: "asking for a tool"},
			{Name: "rage", Definition: "furious at a vendor"}, // no emoji
		},
	}
	got := BuildSystem(bc)

	if strings.Contains(got, "{{CONTEXT}}") || strings.Contains(got, "{{BUCKETS}}") || strings.Contains(got, "{{SCHEMA}}") {
		t.Errorf("unrendered placeholder remains:\n%s", got)
	}
	if !strings.Contains(got, "DeepCube watches observability pain.") {
		t.Error("context not injected")
	}
	if !strings.Contains(got, "- seeker (🙋): asking for a tool") {
		t.Error("bucket with emoji not rendered as `- name (emoji): definition`")
	}
	if !strings.Contains(got, "- rage: furious at a vendor") {
		t.Error("bucket without emoji should render without empty parens")
	}
	if !strings.Contains(got, "\"bucket\"") {
		t.Error("schema should be embedded in the system prompt")
	}
}

func TestSchemaExported(t *testing.T) {
	if !strings.Contains(Schema, "\"fit\"") {
		t.Error("Schema var should expose the Verdict JSON schema")
	}
}
