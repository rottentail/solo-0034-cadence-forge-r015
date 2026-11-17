package privacy

import (
	"cadenceforge/internal/domain"
	"fmt"
	"sort"
)

// FrameBox is the required cover for one output frame.
type FrameBox struct {
	Placement int
	Frame     int
	SourceID  string
	SourceMS  int64
	OutputMS  int64
	CutIn     bool
	Boxes     map[string]Rect
}

// CoveragePlan is the full per-frame requirement for one render. It is the
// single source of truth shared by the encoder (what to burn) and the pixel
// gate (what must be black), so a mismatch between applied and checked cover
// is structurally impossible.
type CoveragePlan struct {
	Frames       []FrameBox
	SpanCoverage []domain.SpanCoverage
}

// Build expands every placement into output frames at the recipe frame rate
// and resolves the required boxes. It enforces the central delivery invariant:
// each active object must cover the WHOLE span in which it appears. A partial
// intersection (object disappears mid-cut or appears after the cut starts) is
// rejected because those few edge frames would otherwise expose the target.
func Build(planner *Planner, plan domain.Plan) (CoveragePlan, error) {
	out := CoveragePlan{Frames: []FrameBox{}, SpanCoverage: []domain.SpanCoverage{}}
	for pi, placement := range plan.Placements {
		durationMS := placement.SourceOutMS - placement.SourceInMS
		frameCount := int(domain.RoundFrames(durationMS, planner.FPS))
		labels := map[string]bool{}
		for f := 0; f < frameCount; f++ {
			// Source time of this output frame, mapped through the trim.
			sourceMS := placement.SourceInMS + int64(int64(f)*1000/int64(planner.FPS))
			if sourceMS >= placement.SourceOutMS {
				sourceMS = placement.SourceOutMS - 1
			}
			outputMS := placement.OutputInMS + int64(int64(f)*1000/int64(planner.FPS))
			boxes, active := planner.BoxAt(placement.SourceID, sourceMS, f == 0)
			fb := FrameBox{Placement: pi, Frame: f, SourceID: placement.SourceID, SourceMS: sourceMS, OutputMS: outputMS, CutIn: f == 0}
			if active {
				fb.Boxes = boxes
				for label := range boxes {
					labels[label] = true
				}
			}
			out.Frames = append(out.Frames, fb)
		}
		// Whole-span containment: any track active at any point of the span
		// must bracket the entire span interval.
		for _, t := range planner.tracks[placement.SourceID] {
			intersects := t.activeHi > placement.SourceInMS && t.activeLo < placement.SourceOutMS
			covers := t.activeLo <= placement.SourceInMS && t.activeHi >= placement.SourceOutMS
			if intersects && !covers {
				return out, domain.Invalid("object %s only partially covers placement %d (%d..%d ms); extend its active interval over the whole source appearance", t.label, pi, placement.SourceInMS, placement.SourceOutMS)
			}
		}
		covered := make([]string, 0, len(labels))
		for label := range labels {
			covered = append(covered, label)
		}
		sort.Strings(covered)
		out.SpanCoverage = append(out.SpanCoverage, domain.SpanCoverage{Placement: pi, SourceID: placement.SourceID, SourceInMS: placement.SourceInMS, SourceOutMS: placement.SourceOutMS, Frames: frameCount, CoveredLabels: covered})
	}
	return out, nil
}

// String renders a deterministic frame list for diagnostics without any
// descriptive target content; labels are opaque.
func (c CoveragePlan) String() string {
	return fmt.Sprintf("coverage plan: %d frames, %d placements", len(c.Frames), len(c.SpanCoverage))
}
