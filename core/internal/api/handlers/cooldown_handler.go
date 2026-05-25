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
