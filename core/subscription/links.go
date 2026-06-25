package subscription

import (
	"strings"

	"github.com/Divaaaan/tenebra/core/model"
)

// ParseLinks parses many raw share links into nodes, the way a user pastes a
// block of links or hands over a .txt list. It is the batch sibling of ParseLink
// and deliberately reuses it per line so the per-protocol logic lives in exactly
// one place.
//
// Each entry is split on newlines first (so a single multi-line string and a
// pre-split slice behave identically), then for every line:
//
//   - surrounding whitespace is trimmed;
//   - blank lines and comments (a line starting with '#' or '//') are ignored —
//     they are neither parsed nor counted as skipped, matching how
//     ParseSubscription treats a link-list body;
//   - an exact-duplicate link (same trimmed text) is collapsed to one node, so
//     pasting the same server twice doesn't create two entries;
//   - a line that fails to parse increments skipped instead of aborting, so one
//     bad line never costs the user the good ones.
//
// skipped counts only lines that looked like a link but didn't parse, never the
// ignored blanks/comments/duplicates. Order is preserved (first occurrence wins
// for duplicates), which keeps the resulting profile's server list stable and
// predictable.
func ParseLinks(raws []string) (nodes []model.Node, skipped int) {
	seen := make(map[string]struct{})
	for _, raw := range raws {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
				continue
			}
			if _, dup := seen[line]; dup {
				continue
			}
			seen[line] = struct{}{}
			node, err := ParseLink(line)
			if err != nil {
				skipped++
				continue
			}
			nodes = append(nodes, node)
		}
	}
	return nodes, skipped
}
