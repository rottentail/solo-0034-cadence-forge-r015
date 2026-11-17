package service

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/encoding"
	"cadenceforge/internal/inspect"
	"cadenceforge/internal/privacy"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// AuditReport is the proxy-review result. It is produced from an explicitly
// pinned mask revision, never from whatever the latest revision happens to be,
// so a later edit cannot change what a reviewer saw or what a frozen render
// used.
type AuditReport struct {
	MaskID       string                   `json:"mask_id"`
	MaskRevision int                      `json:"mask_revision"`
	SourceID     string                   `json:"source_id"`
	Inspection   domain.PrivacyInspection `json:"inspection"`
}

// AuditMask renders the masked source through the real FFmpeg graph and runs
// the same full-resolution per-frame pixel gate as delivery. It returns the
// report, a working directory containing evidence PNGs and the contact sheet,
// and the file names the caller may stream. The caller is responsible for
// removing the returned directory after the response is complete.
func (s *Service) AuditMask(ctx context.Context, maskID string, revision int) (AuditReport, string, []string, error) {
	if revision < 1 {
		return AuditReport{}, "", nil, domain.Invalid("explicit positive mask revision is required")
	}
	st, err := s.Store.Snapshot()
	if err != nil {
		return AuditReport{}, "", nil, err
	}
	versions, ok := st.Masks[maskID]
	if !ok || revision > len(versions) {
		return AuditReport{}, "", nil, domain.Missing("mask revision", maskID)
	}
	mask := versions[revision-1]
	source := st.Sources[mask.SourceID]
	sourcePath, err := s.Blobs.Object(source.Digest)
	if err != nil {
		return AuditReport{}, "", nil, err
	}
	digest, n, err := inspect.Digest(sourcePath)
	if err != nil {
		return AuditReport{}, "", nil, err
	}
	if digest != source.Digest || n != source.Bytes {
		return AuditReport{}, "", nil, fmt.Errorf("source content integrity failed")
	}

	stage, err := os.MkdirTemp("", "cadence-audit-*")
	if err != nil {
		return AuditReport{}, "", nil, err
	}

	// A one-placement plan spans the whole source so every frame is reviewed,
	// including the active interval edges where a cut would enter. The audit
	// renders at the source's own frame cadence so the per-frame gate lines up
	// one-to-one with the decoded artifact rather than assuming 24 fps.
	fps := source.Media.FPS
	if fps < 1 {
		fps = 24
	}
	sheet := domain.Sheet{ID: "audit", Revision: 1, Name: "audit", Spans: []domain.Span{{SourceID: source.ID, InMS: 0, OutMS: source.Media.DurationMS, GainDB: 0}}}
	recipe := domain.Recipe{ID: "audit", Revision: 1, Name: "audit", Format: "mp4", Width: source.Media.Width, Height: source.Media.Height, FPS: fps, SampleRate: 48000}
	sources := map[string]domain.Source{source.ID: source}
	planner, err := privacy.NewPlanner([]domain.Mask{mask}, sources, recipe.Width, recipe.Height, fps)
	if err != nil {
		os.RemoveAll(stage)
		return AuditReport{}, "", nil, err
	}
	plan := domain.Plan{DurationMS: source.Media.DurationMS, Recipe: recipe, Placements: []domain.Placement{{SourceID: source.ID, SourceInMS: 0, SourceOutMS: source.Media.DurationMS, OutputInMS: 0, OutputOutMS: source.Media.DurationMS}}}
	cov, err := privacy.Build(planner, plan)
	if err != nil {
		os.RemoveAll(stage)
		return AuditReport{}, "", nil, err
	}

	// Render the masked proxy at full source resolution via the real graph.
	render := domain.Render{Sheet: sheet, Recipe: recipe, Masks: []domain.Mask{mask}, Sources: sources, Plan: plan}
	runner := encoding.Runner{Binary: s.Config.FFmpeg, Blobs: s.Blobs}
	name, err := runner.Encode(ctx, render, stage)
	if err != nil {
		os.RemoveAll(stage)
		return AuditReport{}, "", nil, err
	}
	gate := privacy.Gate{FFmpeg: s.Config.FFmpeg}
	report, err := gate.Run(ctx, render, cov, filepath.Join(stage, name), stage)
	if err != nil {
		os.RemoveAll(stage)
		return AuditReport{}, "", nil, err
	}
	sheetName, err := gate.ContactSheet(ctx, render, report, stage)
	if err != nil {
		os.RemoveAll(stage)
		return AuditReport{}, "", nil, err
	}
	report.ContactSheet = sheetName
	files := append([]string{}, report.EvidenceFiles...)
	if sheetName != "" {
		files = append(files, sheetName)
	}
	return AuditReport{MaskID: maskID, MaskRevision: revision, SourceID: source.ID, Inspection: report}, stage, files, nil
}
