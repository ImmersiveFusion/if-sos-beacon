// Package prompts holds the language-neutral classifier prompt and its output
// schema, embedded into the binary. Every AI adapter shares these files, so the
// rubric is identical regardless of which model backend a beacon points at.
package prompts

import (
	_ "embed"
	"strings"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

//go:embed classifier.prompt.md
var promptTemplate string

//go:embed classifier.schema.json
var schema string

// Schema is the Verdict JSON schema, exposed so adapters can reference it
// (e.g. in a response_format hint) without re-reading the file.
var Schema = schema

// BuildSystem renders the system prompt for one beacon by substituting the
// beacon's context, its bucket list, and the output schema into the template.
// Buckets render as `- name (emoji): definition`.
func BuildSystem(bc core.BeaconContext) string {
	var b strings.Builder
	for _, bk := range bc.Buckets {
		b.WriteString("- ")
		b.WriteString(bk.Name)
		if bk.Emoji != "" {
			b.WriteString(" (")
			b.WriteString(bk.Emoji)
			b.WriteString(")")
		}
		b.WriteString(": ")
		b.WriteString(bk.Definition)
		b.WriteString("\n")
	}

	s := promptTemplate
	s = strings.ReplaceAll(s, "{{CONTEXT}}", strings.TrimSpace(bc.Context))
	s = strings.ReplaceAll(s, "{{BUCKETS}}", strings.TrimSpace(b.String()))
	s = strings.ReplaceAll(s, "{{SCHEMA}}", strings.TrimSpace(schema))
	return s
}
