package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// RecoveryTrialsHandler serves the per-scope recovery-trial debug history. Each
// row records that a banned proxy was tried against a site and whether it
// worked, so recovery behaviour can be verified from the dashboard.
type RecoveryTrialsHandler struct {
	bans   *repository.BanRepository
	logger *logger.Logger
}

func NewRecoveryTrialsHandler(bans *repository.BanRepository, log *logger.Logger) *RecoveryTrialsHandler {
	return &RecoveryTrialsHandler{bans: bans, logger: log}
}

// List handles GET /api/v1/recovery-trials.
//
//	@Summary		List recovery-trial history (newest first)
//	@Description	Per-trial audit rows from recovery_trials, optionally filtered
//	@Description	by proxy_id, machine_id and/or target_domain.
//	@Tags			bans
//	@Produce		json
//	@Param			proxy_id		query		int		false	"Filter by proxy id"
//	@Param			machine_id		query		string	false	"Filter by machine id"
//	@Param			target_domain	query		string	false	"Filter by target domain"
//	@Param			limit			query		int		false	"Max rows (default 50)"
//	@Success		200	{object}	map[string]interface{}
//	@Router			/recovery-trials [get]
func (h *RecoveryTrialsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := repository.RecoveryTrialFilter{
		MachineID:    q.Get("machine_id"),
		TargetDomain: q.Get("target_domain"),
	}
	if v := q.Get("proxy_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			filter.ProxyID = id
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}

	rows, err := h.bans.ListRecoveryTrials(r.Context(), filter)
	if err != nil {
		h.logger.Error("list recovery trials failed", "error", err)
		http.Error(w, "failed to list recovery trials", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"trials": rows,
		"total":  len(rows),
	})
}
