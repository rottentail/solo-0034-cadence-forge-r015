package privacy

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// ContactSheet renders the retained full-resolution evidence frames into one
// tiled PNG. Individual full-resolution frames remain available alongside it;
// the sheet is an index for reviewers, not a downscaled substitute for the
// per-frame pixel evidence.
func (g Gate) ContactSheet(ctx context.Context, render domain.Render, inspection domain.PrivacyInspection, stageDir string) (string, error) {
	if len(inspection.EvidenceFiles) == 0 {
		return "", nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pattern := filepath.Join(stageDir, "privacy-frame-%04d.png")
	cols := 2
	rows := (len(inspection.EvidenceFiles) + cols - 1) / cols
	output := filepath.Join(stageDir, "privacy-contact-sheet.png")
	// Tile at full per-frame resolution; tile only arranges frames, it does
	// not scale them down, so no leak can hide in a low-resolution proxy.
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file,pipe", "-i", pattern,
		"-filter_complex", fmt.Sprintf("tile=%dx%d", cols, rows),
		"-frames:v", "1", "-threads", "1", output}
	if _, err := media.Run(runCtx, g.FFmpeg, args, 65536); err != nil {
		return "", err
	}
	return "privacy-contact-sheet.png", nil
}
