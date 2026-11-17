package service

import (
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/privacy"
	"cadenceforge/internal/timeline"
	"strings"
	"time"
)

type Request struct {
	SheetID        string                 `json:"sheet_id"`
	SheetRevision  int                    `json:"sheet_revision"`
	RecipeID       string                 `json:"recipe_id"`
	RecipeRevision int                    `json:"recipe_revision"`
	Masks          []domain.MaskSelection `json:"masks,omitempty"`
}

func (s *Service) Submit(key string, request Request) (domain.Render, error) {
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return domain.Render{}, domain.Invalid("Idempotency-Key requires 1..128 bytes")
	}
	if request.SheetRevision < 1 || request.RecipeRevision < 1 {
		return domain.Render{}, domain.Invalid("explicit positive revisions are required")
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	var out domain.Render
	err := s.Store.Update(func(st *domain.State) error {
		fingerprint := hash(request)
		for _, r := range st.Renders {
			if r.Key == key {
				if r.RequestHash != fingerprint {
					return domain.Conflict("idempotency key has different input")
				}
				out = r
				return nil
			}
		}
		pending := 0
		for _, r := range st.Renders {
			if r.State == "queued" || r.State == "running" {
				pending++
			}
		}
		if pending >= s.Config.QueueLimit {
			return domain.Capacity("render queue is full")
		}
		sheets := st.Sheets[request.SheetID]
		recipes := st.Recipes[request.RecipeID]
		if request.SheetRevision > len(sheets) {
			return domain.Missing("sheet revision", request.SheetID)
		}
		if request.RecipeRevision > len(recipes) {
			return domain.Missing("recipe revision", request.RecipeID)
		}
		sheet := sheets[request.SheetRevision-1]
		recipe := recipes[request.RecipeRevision-1]
		masks, err := resolveMasks(st, request.Masks)
		if err != nil {
			return err
		}
		sources := map[string]domain.Source{}
		for _, p := range sheet.Spans {
			sources[p.SourceID] = st.Sources[p.SourceID]
		}
		for _, c := range sheet.Cues {
			sources[c.SourceID] = st.Sources[c.SourceID]
		}
		plan, err := timeline.Build(sheet, recipe, sources)
		if err != nil {
			return err
		}
		plan.VTT = caption.VTT(caption.Map(sheet, plan))
		// Privacy masks are burned into video only. Rejecting them on audio
		// renders keeps the contract explicit rather than silently ignoring.
		if len(masks) > 0 && recipe.Format != "mp4" {
			return domain.Invalid("privacy masks apply only to MP4 video renders")
		}
		if len(masks) > 0 {
			planner, err := privacy.NewPlanner(masks, sources, recipe.Width, recipe.Height, recipe.FPS)
			if err != nil {
				return err
			}
			// Submit-time structural check: reject partial cut-edge coverage
			// before any media work is scheduled. The definitive evidence is
			// still the per-frame pixel gate after encoding.
			if _, err = privacy.Build(planner, plan); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		id := ID()
		out = domain.Render{ID: id, Key: key, RequestHash: fingerprint, Sheet: sheet, Recipe: recipe, Masks: masks, Sources: sources, Plan: plan, State: "queued", Attempts: []domain.Attempt{}, CreatedAt: now, UpdatedAt: now}
		st.Renders[id] = out
		st.Emit(id, "render.queued", "frozen source, recipe and mask revisions")
		return nil
	})
	if err == nil {
		s.Notify()
	}
	return out, err
}
