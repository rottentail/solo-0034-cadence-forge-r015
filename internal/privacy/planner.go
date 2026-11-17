// Package privacy turns versioned mask revisions into deterministic per-frame
// cover geometry and verifies that geometry against real encoded pixels.
//
// The design deliberately avoids two failure modes:
//   - A fixed oversized box is safe but destroys usable picture.
//   - A handful of interpolated key points can miss a target between pins.
//
// Instead every output frame gets an explicit rectangle computed from dense
// pins, enlarged by a margin derived from measured inter-pin displacement
// (fast movement and zoom grow the margin), plus constant slack for cut-in
// frames and the filter/pixel gate boundary. The same computed rectangles are
// burned into the picture and checked on the delivered artifact, so the gate
// cannot pass on geometry that was never applied.
package privacy

import (
	"cadenceforge/internal/domain"
	"math"
	"sort"
)

const (
	// GateLumaThreshold is the inclusive maximum luma inside an eroded cover
	// box. Measured worst case for a correctly filled box under CRF23 H.264 is
	// 5; a real exposure measures 150+, so 40 keeps a wide margin both ways.
	GateLumaThreshold = 40
	// GateErodePX is the inset applied on every side before pixel sampling.
	// H.264 4:2:0 subsampling only distorts the outermost rows/columns.
	GateErodePX = 2
	// BaseMarginPX is constant slack in output pixels: annotation/rounding
	// slack plus the filter-chain boundary. The motion component is added on
	// top, so stationary targets keep a tight box instead of a fixed giant one.
	BaseMarginPX = 4
	// CutInMarginPX adds protection on the first frame of every source
	// appearance, where a fresh segment starts and PTS alignment can differ by
	// one source frame.
	CutInMarginPX = 4
	// MotionPadFraction grows the box by this share of measured per-pin
	// displacement per axis, covering interpolation lag for non-linear motion.
	MotionPadFraction = 0.5
)

// Rect is one frame's cover rectangle in output pixel coordinates.
type Rect struct {
	X, Y, W, H int
}

type pin struct {
	atMS       int64
	x, y, w, h float64
}

type track struct {
	label    string
	activeLo int64
	activeHi int64
	pins     []pin
}

// dims records one source's native frame size.
type dims struct {
	width  int
	height int
}

// mapping is the letterbox transform from source pixels to output pixels,
// matching the filter graph's scale=decrease plus centered pad.
type mapping struct {
	scale            float64
	offsetX, offsetY int
}

// Planner holds resolved tracks for one render in source coordinates.
type Planner struct {
	OutputWidth  int
	OutputHeight int
	FPS          int
	dims         map[string]dims
	tracks       map[string][]track // source id -> tracks
}

// NewPlanner resolves the frozen mask revisions selected by a render into
// interpolation tracks. All masks must reference sources present in the sheet.
func NewPlanner(masks []domain.Mask, sources map[string]domain.Source, outputW, outputH, fps int) (*Planner, error) {
	p := &Planner{OutputWidth: outputW, OutputHeight: outputH, FPS: fps, dims: map[string]dims{}, tracks: map[string][]track{}}
	for _, m := range masks {
		source, ok := sources[m.SourceID]
		if !ok {
			return nil, domain.Invalid("mask %s references a source absent from the sheet", m.ID)
		}
		if !source.Media.Video {
			return nil, domain.Invalid("mask %s requires a video source", m.ID)
		}
		p.dims[m.SourceID] = dims{width: source.Media.Width, height: source.Media.Height}
		for _, obj := range m.Objects {
			tr := track{label: obj.Label, activeLo: obj.ActiveMS[0], activeHi: obj.ActiveMS[1]}
			for _, kf := range obj.Keyframes {
				tr.pins = append(tr.pins, pin{atMS: kf.AtMS, x: float64(kf.X), y: float64(kf.Y), w: float64(kf.Width), h: float64(kf.Height)})
			}
			sort.Slice(tr.pins, func(i, j int) bool { return tr.pins[i].atMS < tr.pins[j].atMS })
			p.tracks[m.SourceID] = append(p.tracks[m.SourceID], tr)
		}
	}
	return p, nil
}

// mappingFor reproduces scale=W:H:force_original_aspect_ratio=decrease followed
// by pad=W:H centered, so source coordinates land on the same output pixels the
// encoder actually emits.
func (p *Planner) mappingFor(sourceID string) mapping {
	d := p.dims[sourceID]
	s := math.Min(float64(p.OutputWidth)/float64(d.width), float64(p.OutputHeight)/float64(d.height))
	contentW := int(math.Round(float64(d.width) * s))
	contentH := int(math.Round(float64(d.height) * s))
	return mapping{scale: s, offsetX: (p.OutputWidth - contentW) / 2, offsetY: (p.OutputHeight - contentH) / 2}
}

func interpolate(pins []pin, tMS int64) (x, y, w, h float64) {
	if tMS <= pins[0].atMS {
		q := pins[0]
		return q.x, q.y, q.w, q.h
	}
	last := pins[len(pins)-1]
	if tMS >= last.atMS {
		q := last
		return q.x, q.y, q.w, q.h
	}
	for i := 1; i < len(pins); i++ {
		a, b := pins[i-1], pins[i]
		if tMS >= a.atMS && tMS <= b.atMS {
			f := float64(tMS-a.atMS) / float64(b.atMS-a.atMS)
			return a.x + (b.x-a.x)*f, a.y + (b.y-a.y)*f, a.w + (b.w-a.w)*f, a.h + (b.h-a.h)*f
		}
	}
	q := last
	return q.x, q.y, q.w, q.h
}

// motionMargin measures the largest center displacement between adjacent pins
// across the whole track and converts it to output pixels with the pad
// fraction. The margin grows with both center displacement (translation) and
// per-pin box size change (zoom/growth), so a fast-moving or enlarging target
// enlarges its own cover instead of every frame paying for a fixed giant box.
func motionMargin(t track, s float64) (mx, my int) {
	var dx, dy float64
	for i := 1; i < len(t.pins); i++ {
		a, b := t.pins[i-1], t.pins[i]
		cax, cay := a.x+a.w/2, a.y+a.h/2
		cbx, cby := b.x+b.w/2, b.y+b.h/2
		dx = math.Max(dx, math.Abs(cbx-cax))
		dy = math.Max(dy, math.Abs(cby-cay))
		// Half the size delta bounds how far an edge can outrun the linearly
		// interpolated box when the target zooms between pins.
		dx = math.Max(dx, math.Abs(b.w-a.w)/2)
		dy = math.Max(dy, math.Abs(b.h-a.h)/2)
	}
	return int(math.Ceil(dx * s * MotionPadFraction)), int(math.Ceil(dy * s * MotionPadFraction))
}

// clamp keeps the rectangle inside the output frame while guaranteeing a
// gateable interior; tracks were validated in source space so clamping only
// removes margin, never the interpolated target box.
func clamp(r Rect, w, h int) Rect {
	if r.W > w {
		r.W = w
	}
	if r.H > h {
		r.H = h
	}
	if r.X < 0 {
		r.X = 0
	}
	if r.Y < 0 {
		r.Y = 0
	}
	if r.X+r.W > w {
		r.X = w - r.W
	}
	if r.Y+r.H > h {
		r.Y = h - r.H
	}
	return r
}

// BoxAt returns the cover rectangle for one source time on one placement.
// cutIn marks the first frame of a source appearance and adds cut-in slack.
// It returns false when no track is active at that time.
func (p *Planner) BoxAt(sourceID string, sourceMS int64, cutIn bool) (map[string]Rect, bool) {
	tracks := p.tracks[sourceID]
	if len(tracks) == 0 {
		return nil, false
	}
	mp := p.mappingFor(sourceID)
	out := map[string]Rect{}
	for _, t := range tracks {
		// Half-open active interval; whole-span containment is enforced in
		// Build, so this never silently uncovers an edge frame.
		if sourceMS < t.activeLo || sourceMS >= t.activeHi {
			continue
		}
		x, y, w, h := interpolate(t.pins, sourceMS)
		mx, my := motionMargin(t, mp.scale)
		marginX := BaseMarginPX + mx
		marginY := BaseMarginPX + my
		if cutIn {
			marginX += CutInMarginPX
			marginY += CutInMarginPX
		}
		left := mp.offsetX + int(math.Floor(x*mp.scale))
		top := mp.offsetY + int(math.Floor(y*mp.scale))
		right := mp.offsetX + int(math.Ceil((x+w)*mp.scale))
		bottom := mp.offsetY + int(math.Ceil((y+h)*mp.scale))
		r := Rect{X: left - marginX, Y: top - marginY, W: (right - left) + 2*marginX, H: (bottom - top) + 2*marginY}
		out[t.label] = clamp(r, p.OutputWidth, p.OutputHeight)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
