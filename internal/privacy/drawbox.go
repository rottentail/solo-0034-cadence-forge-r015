package privacy

import (
	"fmt"
	"sort"
)

// labelRect is one frame's rectangle for one object within a placement.
type labelRect struct {
	frame int
	rect  Rect
}

// DrawboxFilters builds one flat drawbox per covered frame per object for one
// placement. Each filter is a simple, non-nested expression gated by the
// timeline option enable='eq(n,k)', which FFmpeg's graph parser accepts
// reliably (deeply nested if() expressions inside a quoted option value are
// not parsed correctly). At any frame only the matching filter paints, so the
// result is exactly one box per object per frame.
//
// The geometry comes from the same CoveragePlan the pixel gate verifies; there
// is no second geometric code path that could drift.
func DrawboxFilters(cov CoveragePlan, placementIndex int) []string {
	byLabel := map[string][]labelRect{}
	var labels []string
	for _, fr := range cov.Frames {
		if fr.Placement != placementIndex {
			continue
		}
		for label, r := range fr.Boxes {
			if _, ok := byLabel[label]; !ok {
				labels = append(labels, label)
			}
			byLabel[label] = append(byLabel[label], labelRect{frame: fr.Frame, rect: r})
		}
	}
	if len(labels) == 0 {
		return nil
	}
	sort.Strings(labels)
	var filters []string
	for _, label := range labels {
		for _, lr := range byLabel[label] {
			// The escaped comma survives the filter-graph option parser; n is
			// this trimmed segment's own frame counter.
			filters = append(filters, fmt.Sprintf(
				"drawbox=x=%d:y=%d:w=%d:h=%d:color=black:t=fill:enable='eq(n\\,%d)'",
				lr.rect.X, lr.rect.Y, lr.rect.W, lr.rect.H, lr.frame))
		}
	}
	return filters
}
