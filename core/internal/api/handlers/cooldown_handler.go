package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// CooldownHandler powers the dashboard's Cooldown tab. Returns every (proxy,
// machine, domain) scope currently in the banned state, along with how long
// is left on its cooldown and how many probe attempts it's taken.
type CooldownHandler struct {
	bans   *repository.BanRepository
	logger *logger.Logger
}

func NewCooldownHandler(bans *repository.BanRepository, log *logger.Logger) *CooldownHandler {
	return &CooldownHandler{bans: bans, logger: log}
}

// List handles GET /api/v1/cooldowns.
//
//	@Summary		List proxies currently in cooldown / recovery test
//	@Description	One row per (proxy, machine, domain) scope that is banned
//	@Description	right now. Powers the dashboard Cooldown tab.
//	@Tags			cooldowns
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Router			/cooldowns [get]
func (h *CooldownHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.bans.ListCooldowns(r.Context())
	if err != nil {
		h.logger.Error("list cooldowns failed", "error", err)
		http.Error(w, "failed to list cooldowns", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"cooldowns": rows,
		"total":     len(rows),
	})
}

// ClearAll handles DELETE /api/v1/cooldowns.
//
//	@Summary		Clear every cooldown
//	@Description	Deletes all banned (proxy, machine, site) scopes, returning
//	@Description	those proxies to rotation immediately. Failure streaks are
//	@Description	dropped with them, so a cleared scope has to fail from
//	@Description	scratch before it can be banned again.
//	@Tags			cooldowns
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}	"Number of scopes cleared"
//	@Failure		500	{object}	models.ErrorResponse
//	@Router			/cooldowns [delete]
func (h *CooldownHandler) ClearAll(w http.ResponseWriter, r *http.Request) {
	cleared, err := h.bans.ClearAllCooldowns(r.Context())
	if err != nil {
		h.logger.Error("clear cooldowns failed", "error", err)
		http.Error(w, "failed to clear cooldowns", http.StatusInternalServerError)
		return
	}

	h.logger.Info("cleared all cooldowns", "cleared", cleared)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"cleared": cleared})
}
