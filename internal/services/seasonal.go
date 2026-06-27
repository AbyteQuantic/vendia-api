// Spec: specs/086-branding-estacional/spec.md
package services

import (
	"sort"
	"time"

	"vendia-backend/internal/models"
)

// ResolveActiveCampaign — PURO/testeable. Devuelve la campaña estacional activa
// para `now`, o ok=false si ninguna. Candidata: Enabled && (ForceActive ||
// dentro de [StartsAt, EndsAt)). Si varias solapan, gana mayor Priority, luego
// StartsAt más reciente. `now` debe venir en hora Colombia (medianoche local).
func ResolveActiveCampaign(rows []models.SeasonalCampaign, now time.Time) (models.SeasonalCampaign, bool) {
	candidates := make([]models.SeasonalCampaign, 0, len(rows))
	for _, c := range rows {
		if !c.Enabled {
			continue
		}
		if c.ForceActive || inWindow(c, now) {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return models.SeasonalCampaign{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		si, sj := candidates[i].StartsAt, candidates[j].StartsAt
		if si == nil {
			return false
		}
		if sj == nil {
			return true
		}
		return si.After(*sj)
	})
	return candidates[0], true
}

// inWindow — now dentro de [StartsAt, EndsAt). nil = sin límite por ese lado.
func inWindow(c models.SeasonalCampaign, now time.Time) bool {
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return false
	}
	if c.EndsAt != nil && !now.Before(*c.EndsAt) { // now >= EndsAt → fuera (exclusivo)
		return false
	}
	return true
}
