package privacy

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// maxEvidenceFrames bounds how many full-resolution evidence PNGs are retained
// per render. The numeric gate still evaluates every frame; only the image
// set is sampled so the artifact directory stays bounded.
const maxEvidenceFrames = 8

// Gate decodes the delivered MP4 to raw luma planes and verifies every
// required box on every output frame at the full delivered resolution. It does
// not use a downscaled proxy: a leak that only appears at full resolution is
// therefore impossible to hide.
type Gate struct {
	FFmpeg string
}

// Run executes the pixel gate and writes review evidence into stageDir. The
// returned inspection is durable evidence (geometry + pixel measurements),
// never a descriptive record of what was hidden.
func (g Gate) Run(ctx context.Context, render domain.Render, cov CoveragePlan, artifactPath, stageDir string) (domain.PrivacyInspection, error) {
	width, height := render.Recipe.Width, render.Recipe.Height
	inspection := domain.PrivacyInspection{
		GateLumaThreshold: GateLumaThreshold,
		InteriorErodePX:   GateErodePX,
		Width:             width,
		Height:            height,
		SpanCoverage:      cov.SpanCoverage,
		Frames:            []domain.FrameGate{},
		EvidenceFiles:     []string{},
	}

	lumaPath := filepath.Join(stageDir, "privacy-luma.gray")
	if err := g.extractGray(ctx, artifactPath, lumaPath); err != nil {
		return inspection, err
	}
	raw, err := os.ReadFile(lumaPath)
	if err != nil {
		return inspection, err
	}
	frameSize := width * height
	totalFrames := len(raw) / frameSize
	if totalFrames == 0 {
		return inspection, fmt.Errorf("privacy gate found no video frames")
	}
	inspection.TotalFrames = totalFrames
	if totalFrames != len(cov.Frames) {
		return inspection, fmt.Errorf("privacy gate frame count %d does not match coverage plan %d", totalFrames, len(cov.Frames))
	}

	evidenceIndexes := pickEvidence(cov.Frames)
	evidenceSet := map[int]bool{}
	for _, idx := range evidenceIndexes {
		evidenceSet[idx] = true
	}

	for idx, fr := range cov.Frames {
		row := domain.FrameGate{Placement: fr.Placement, Frame: fr.Frame, SourceMS: fr.SourceMS, OutputMS: fr.OutputMS, CutIn: fr.CutIn, Boxes: []domain.BoxResult{}}
		base := idx * frameSize
		labels := make([]string, 0, len(fr.Boxes))
		for label := range fr.Boxes {
			labels = append(labels, label)
		}
		// Deterministic ordering for stable evidence.
		for i := 0; i < len(labels); i++ {
			for j := i + 1; j < len(labels); j++ {
				if labels[j] < labels[i] {
					labels[i], labels[j] = labels[j], labels[i]
				}
			}
		}
		for _, label := range labels {
			r := fr.Boxes[label]
			worst, wx, wy := worstLuma(raw, base, width, height, r)
			row.Boxes = append(row.Boxes, domain.BoxResult{Label: label, X: r.X, Y: r.Y, Width: r.W, Height: r.H, WorstLuma: worst, WorstX: wx, WorstY: wy})
			if worst > GateLumaThreshold {
				inspection.FailedFrames++
			}
		}
		inspection.Frames = append(inspection.Frames, row)
		if evidenceSet[idx] {
			name := fmt.Sprintf("privacy-frame-%04d.png", idx)
			if err := g.evidencePNG(ctx, artifactPath, idx, filepath.Join(stageDir, name)); err != nil {
				return inspection, err
			}
			inspection.EvidenceFiles = append(inspection.EvidenceFiles, name)
		}
	}
	inspection.PassedFrames = totalFrames - inspection.FailedFrames
	inspection.Passed = inspection.FailedFrames == 0
	_ = os.Remove(lumaPath)
	return inspection, nil
}

func (g Gate) extractGray(ctx context.Context, input, output string) error {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats,
		"-i", input, "-an", "-f", "rawvideo", "-pix_fmt", "gray", output}
	_, err := media.Run(runCtx, g.FFmpeg, args, 65536)
	return err
}

// evidencePNG extracts one full-resolution RGB frame as an actual PNG, so a
// reviewer can see the delivered pixels rather than a scaled proxy.
func (g Gate) evidencePNG(ctx context.Context, input string, frameIndex int, output string) error {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats,
		"-i", input, "-vf", fmt.Sprintf("select=eq(n\\,%d)", frameIndex),
		"-vsync", "0", "-frames:v", "1", "-threads", "1", output}
	if _, err := media.Run(runCtx, g.FFmpeg, args, 65536); err != nil {
		return err
	}
	return nil
}

// worstLuma scans the eroded interior of one box and returns the brightest
// luma value and its coordinates. The erosion excludes the 4:2:0 boundary band
// where H.264 can bleed chroma/luma regardless of cover correctness.
func worstLuma(raw []byte, base, stride, height int, r Rect) (int, int, int) {
	x0 := r.X + GateErodePX
	y0 := r.Y + GateErodePX
	x1 := r.X + r.W - GateErodePX
	y1 := r.Y + r.H - GateErodePX
	if x1 <= x0 || y1 <= y0 {
		// Validated earlier, but stay fail-closed.
		return 255, r.X, r.Y
	}
	worst, wx, wy := 0, x0, y0
	for y := y0; y < y1; y++ {
		if y >= height {
			break
		}
		row := base + y*stride
		for x := x0; x < x1; x++ {
			if v := int(raw[row+x]); v > worst {
				worst, wx, wy = v, x, y
			}
		}
	}
	return worst, wx, wy
}

// pickEvidence chooses spread-out frames including every cut-in, so review
// images cover segment entry points and the middle/end of the composition.
func pickEvidence(frames []FrameBox) []int {
	if len(frames) <= maxEvidenceFrames {
		out := make([]int, len(frames))
		for i := range frames {
			out[i] = i
		}
		return out
	}
	chosen := map[int]bool{}
	for i, fr := range frames {
		if fr.CutIn {
			chosen[i] = true
		}
	}
	for len(chosen) < maxEvidenceFrames {
		best, bestGap := 0, -1
		for i := range frames {
			if chosen[i] {
				continue
			}
			gap := len(frames)
			for j := range chosen {
				d := i - j
				if d < 0 {
					d = -d
				}
				if d < gap {
					gap = d
				}
			}
			if gap > bestGap {
				bestGap, best = gap, i
			}
		}
		chosen[best] = true
	}
	out := make([]int, 0, len(chosen))
	for i := range chosen {
		out = append(out, i)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
