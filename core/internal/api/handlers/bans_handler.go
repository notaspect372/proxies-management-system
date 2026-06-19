package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// BansHandler powers the dashboard's Ban Analysis tab. It returns every banned
// (proxy, machine, domain) scope along with a derived classification
// (permanent / recovering / cooldown) so stuck/definitive bans can be told
// apart from ones still being trialed.
type BansHandler struct {
	bans   *repository.BanRepository
	logger *logger.Logger
}

func NewBansHandler(bans *repository.BanRepository, log *logger.Logger) *BansHandler {
	return &BansHandler{bans: bans, logger: log}
}

// List handles GET /api/v1/bans.
//
//	@Summary		List banned scopes with permanent/recovering/cooldown classification
//	@Description	One row per banned (proxy, machine, domain) scope, enriched
//	@Description	with a computed classification and never_recovered flag.
//	@Description	Optional ?classification=permanent filters to a single class.
//	@Tags			bans
//	@Produce		json
//	@Param			classification	query		string	false	"Filter: permanent | recovering | cooldown"
//	@Success		200	{object}	map[string]interface{}
//	@Router			/bans [get]
func (h *BansHandler) List(w http.ResponseWriter, r *http.Request) {
	classification := r.URL.Query().Get("classification")
	rows, err := h.bans.ListBans(r.Context(), classification)
	if err != nil {
		h.logger.Error("list bans failed", "error", err)
		http.Error(w, "failed to list bans", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"bans":  rows,
		"total": len(rows),
	})
}
