// Package filter is the cheap, deterministic pre-classifier net (funnel stage 2).
package filter

import (
	"strings"

	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

// Match is the Phase 0 keyword-OR gate: a signal passes if any keyword appears
// in its text or its source-native tags. The full AND/OR/NOT boolean grammar
// (the `expr` argument) is Phase 1; expr is accepted now so the signature stays
// stable. With no keywords the gate is open (the source already queried by
// keyword upstream).
//
// Tags are part of the haystack because firehose sources carry most of their
// topicality there, not in the text: on Lobsters, roughly 9 in 10 stories are
// link posts with an EMPTY description, so text matching alone is title-only and
// almost nothing survives. Curated tags (performance, devops, practices) are the
// signal those posts actually ship with, and folding them in here means one
// operator lever (keywords) covers text and taxonomy both.
func Match(s core.Signal, keywords []string, expr string) bool {
	_ = expr // Phase 1: parse and evaluate the boolean expression
	if len(keywords) == 0 {
		return true
	}
	hay := strings.ToLower(s.Title + " " + s.Body + " " +
		strings.Join(s.Tags, " ") + " " + strings.Join(s.TopComments, " "))
	for _, k := range keywords {
		if k == "" {
			continue
		}
		if strings.Contains(hay, strings.ToLower(k)) {
			return true
		}
	}
	return false
}
