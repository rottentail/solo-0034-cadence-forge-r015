package domain

import "time"

// MaskKeyframe pins one object box at one source time. Boxes are expressed in
// source pixel coordinates; the privacy planner interpolates between pins and
// adds motion-derived padding. Coordinates are never expressed at proxy
// resolution so review evidence always reflects the full-resolution picture.
type MaskKeyframe struct {
	AtMS   int64 `json:"at_ms"`
	X      int   `json:"x"`
	Y      int   `json:"y"`
	Width  int   `json:"width"`
	Height int   `json:"height"`
}

// MaskObject is one privacy target on a source. The label is an opaque review
// token, never a descriptive name, so delivery records cannot reveal what was
// hidden. ActiveMS bounds every appearance of the target; a render span that
// only partially intersects the interval is rejected rather than risking a
// leak at a cut edge.
type MaskObject struct {
	Label     string         `json:"label"`
	ActiveMS  [2]int64       `json:"active_ms"`
	Keyframes []MaskKeyframe `json:"keyframes"`
}

// Mask is an independently versioned, source-scoped privacy edit. It is stored
// and frozen separately from sheets and recipes: editing a clip never mutates
// a mask, and renders pin an explicit immutable revision.
type Mask struct {
	ID        string       `json:"id"`
	Revision  int          `json:"revision"`
	Name      string       `json:"name"`
	SourceID  string       `json:"source_id"`
	Objects   []MaskObject `json:"objects"`
	CreatedAt time.Time    `json:"created_at"`
}

// MaskSelection pins one mask revision at submit time.
type MaskSelection struct {
	MaskID       string `json:"mask_id"`
	MaskRevision int    `json:"mask_revision"`
}

// BoxResult is the full-resolution pixel gate outcome for one required object
// on one output frame.
type BoxResult struct {
	Label     string `json:"label"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	WorstLuma int    `json:"worst_luma"`
	WorstX    int    `json:"worst_x"`
	WorstY    int    `json:"worst_y"`
}

// FrameGate is the per-frame evidence row. Every output frame of every source
// appearance is evaluated at output (full delivered) resolution, never at a
// downscaled proxy size.
type FrameGate struct {
	Placement int         `json:"placement"`
	Frame     int         `json:"frame"`
	SourceMS  int64       `json:"source_ms"`
	OutputMS  int64       `json:"output_ms"`
	CutIn     bool        `json:"cut_in"`
	Boxes     []BoxResult `json:"boxes"`
}

// SpanCoverage confirms each object's active interval fully covers each source
// appearance (no partial cut-edge intersection).
type SpanCoverage struct {
	Placement     int      `json:"placement"`
	SourceID      string   `json:"source_id"`
	SourceInMS    int64    `json:"source_in_ms"`
	SourceOutMS   int64    `json:"source_out_ms"`
	Frames        int      `json:"frames"`
	CoveredLabels []string `json:"covered_labels"`
}

// PrivacyInspection is the durable, reviewable coverage fact committed with a
// render. It contains geometry and pixel evidence only; no descriptive target
// information is stored here.
type PrivacyInspection struct {
	Passed            bool           `json:"passed"`
	GateLumaThreshold int            `json:"gate_luma_threshold"`
	InteriorErodePX   int            `json:"interior_erode_px"`
	Width             int            `json:"width"`
	Height            int            `json:"height"`
	TotalFrames       int            `json:"total_frames"`
	PassedFrames      int            `json:"passed_frames"`
	FailedFrames      int            `json:"failed_frames"`
	SpanCoverage      []SpanCoverage `json:"span_coverage"`
	Frames            []FrameGate    `json:"frames"`
	EvidenceFiles     []string       `json:"evidence_files"`
	ContactSheet      string         `json:"contact_sheet"`
}
