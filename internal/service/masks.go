package service

import (
	"cadenceforge/internal/domain"
	"time"
)

// SaveMask stores an immutable privacy-mask revision. Masks are versioned
// independently of sheets and recipes: saving a clip edit never changes a
// mask, and renders pin an explicit revision that later edits cannot affect.
func (s *Service) SaveMask(id string, expected int, input domain.Mask) (domain.Mask, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if id == "" {
		id = ID()
	}
	var out domain.Mask
	err := s.Store.Update(func(st *domain.State) error {
		versions := st.Masks[id]
		if len(versions) != expected {
			return domain.Conflict("mask revision changed")
		}
		if expected == 0 && len(versions) > 0 {
			return domain.Conflict("mask already exists")
		}
		if expected > 0 && len(versions) == 0 {
			return domain.Missing("mask", id)
		}
		source, ok := st.Sources[input.SourceID]
		if !ok {
			return domain.Missing("source", input.SourceID)
		}
		if expected > 0 && versions[0].SourceID != input.SourceID {
			return domain.Invalid("mask source identity is immutable across revisions")
		}
		if err := domain.ValidateMask(input, source); err != nil {
			return err
		}
		input.ID = id
		input.Revision = len(versions) + 1
		input.CreatedAt = time.Now().UTC()
		out = input
		st.Masks[id] = append(versions, input)
		st.Emit(id, "mask.revised", "immutable privacy revision saved")
		return nil
	})
	return out, err
}

// MaskHistory returns the immutable revisions of one mask for review.
func (s *Service) MaskHistory(id string) ([]domain.Mask, error) {
	st, err := s.Store.Snapshot()
	if err != nil {
		return nil, err
	}
	versions, ok := st.Masks[id]
	if !ok {
		return nil, domain.Missing("mask", id)
	}
	return versions, nil
}

// resolveMasks pins the explicitly selected revisions into immutable
// snapshots. It never reads "the latest" revision: a frozen render, its proxy
// review and every retry use exactly the revisions chosen at submit time.
func resolveMasks(st *domain.State, selections []domain.MaskSelection) ([]domain.Mask, error) {
	seen := map[string]bool{}
	out := make([]domain.Mask, 0, len(selections))
	for _, sel := range selections {
		if sel.MaskID == "" {
			return nil, domain.Invalid("mask selection requires a mask id")
		}
		if sel.MaskRevision < 1 {
			return nil, domain.Invalid("explicit positive mask revision is required")
		}
		if seen[sel.MaskID] {
			return nil, domain.Invalid("mask %s selected more than once", sel.MaskID)
		}
		seen[sel.MaskID] = true
		versions, ok := st.Masks[sel.MaskID]
		if !ok || sel.MaskRevision > len(versions) {
			return nil, domain.Missing("mask revision", sel.MaskID)
		}
		out = append(out, versions[sel.MaskRevision-1])
	}
	return out, nil
}
