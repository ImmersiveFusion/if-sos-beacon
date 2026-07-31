package delivery

import "strings"

// This file holds the presentation facts every Sink shares, so they are defined
// once rather than per adapter. Discord is only the first sink; Slack, email and
// the generic webhook render the same finding, and a channel convention that
// disagreed between them would be a real defect (a reader reacting with the
// emoji one sink advertised and another ignored).

// The channel's two-reaction convention, in the wording an embed carries. These
// are text prompts only: a webhook cannot add its own reactions (OD-4), so the
// emoji here must match what the channel actually reacts with. Common to every
// source and every beacon: the harvest platform never changes how a human claims
// or skips a thread.
const (
	claimEmoji = "🙋" // raise a hand to take the thread
	skipEmoji  = "🙅" // arms crossed: do not respond, spam or not worth it
)

// sourceBrand is how one harvest source presents itself in a delivered pointer.
type sourceBrand struct {
	Label    string // short text prefix, e.g. "HN"
	Platform string // the platform's full name, spelled out in the attribution line
	UserTag  string // how that platform writes a username, e.g. "u/" on Reddit
	Color    int    // accent color where the sink supports one; 0 = none
}

// sourceBrands are the per-source labels stamped on a delivered finding so a
// reader can tell at a glance which platform it came from. Text, not logos: an
// icon would mean hosting third-party marks from a public Apache-2.0 repo, and
// the platform's own color already does the fast visual sorting.
//
// A source with no entry falls back to its upper-cased registry name, so a new
// adapter is never silently unlabeled.
var sourceBrands = map[string]sourceBrand{
	"hn":       {Label: "HN", Platform: "Hacker News", Color: 0xFF6600},
	"lobsters": {Label: "LB", Platform: "Lobsters", Color: 0xAC130D},
	"reddit":   {Label: "RD", Platform: "Reddit", UserTag: "u/", Color: 0xFF4500},
}

// attribution renders "u/alice on Reddit": who wrote the thing, and where it
// came from, spelled out rather than abbreviated.
//
// For Reddit this is a requirement, not presentation. Developer Terms S5.2
// obliges anyone using the Data API to link back to the content, cite the
// username, and make clear it came from Reddit. The embed URL covers the
// link-back; this covers the other two. IF does not run that source (see the
// README), but the adapter ships, so an operator who enables it inherits a sink
// that already complies rather than one they have to fix. Every other source
// gets the same line, because knowing who is speaking is useful regardless.
//
// Returns "" when a source gave us no author, so the caller omits the segment
// instead of rendering a dangling "on Reddit".
func attribution(source, author string) string {
	if author == "" {
		return ""
	}
	b, known := sourceBrands[source]
	platform := b.Platform
	if !known || platform == "" {
		platform = source
	}
	if platform == "" {
		return b.UserTag + author
	}
	return b.UserTag + author + " on " + platform
}

// sourceLabel returns the display prefix and accent color for a source.
func sourceLabel(source string) (string, int) {
	if b, ok := sourceBrands[source]; ok {
		return b.Label, b.Color
	}
	if source == "" {
		return "??", 0
	}
	return strings.ToUpper(source), 0
}
