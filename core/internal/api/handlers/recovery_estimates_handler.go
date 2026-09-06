package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/go-chi/chi/v5"
)

// RecoveryEstimatesHandler serves the learned per-site cooldown profile: how
// long bans on a given site have historically lasted, aggregated from every
// observed recovery across every proxy and machine. Powers the dashboard's
// Recovery Estimates page and its per-site drill-down.
type RecoveryEstimatesHandler struct {
	bans   *repository.BanRepository
	logger *logger.Logger
}

func NewRecoveryEstimatesHandler(bans *repository.BanRepository, log *logger.Logger) *RecoveryEstimatesHandler {
	return &RecoveryEstimatesHandler{bans: bans, logger: log}
}

// List handles GET /api/v1/recovery-estimates.
//
//	@Summary		Learned cooldown estimate per site
//	@Description	One row per site that has been observed recovering at least
//	@Description	once, with mean/median/min/max ban duration and how many
//	@Description	observations back it. Best-evidenced sites first.
//	@Tags			bans
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Failure		500	{object}	models.ErrorResponse
//	@Router			/recovery-estimates [get]
func (h *RecoveryEstimatesHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.bans.ListDomainRecoveryEstimates(r.Context())
	if err != nil {
		h.logger.Error("list recovery estimates failed", "error", err)
		http.Error(w, "failed to list recovery estimates", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"estimates": rows,
		"total":     len(rows),
	})
}

// Detail handles GET /api/v1/recovery-estimates/{domain}.
//
//	@Summary		Observations behind one site's estimate
//	@Description	The individual ban to recovery observations that produced the
//	@Description	site's estimate, newest first, plus the recovery-trial log for
//	@Description	the same site so the trial ladder can be read alongside it.
//	@Tags			bans
//	@Produce		json
//	@Param			domain	path		string	true	"Target domain"
//	@Param			limit	query		int		false	"Max observations (default 200)"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]string
//	@Failure		500		{object}	models.ErrorResponse
//	@Router			/recovery-estimates/{domain} [get]
func (h *RecoveryEstimatesHandler) Detail(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "domain")))
	if domain == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}

	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	events, err := h.bans.ListRecoveryEvents(r.Context(), domain, limit)
	if err != nil {
		h.logger.Error("list recovery events failed", "error", err, "domain", domain)
		http.Error(w, "failed to list recovery events", http.StatusInternalServerError)
		return
	}

	// The trial log for the same site lets the UI show the ladder that led to
	// each recovery. Non-fatal: an empty trial list still renders a useful page.
	trials, err := h.bans.ListRecoveryTrials(r.Context(), repository.RecoveryTrialFilter{
		TargetDomain: domain,
		Limit:        200,
	})
	if err != nil {
		h.logger.Warn("trial log fetch failed for estimate detail", "error", err, "domain", domain)
		trials = nil
	}

	// Currently-banned scopes for this site, so the page can show who is
	// waiting on the estimate right now.
	var waiting []repository.CooldownRow
	if all, err := h.bans.ListCooldowns(r.Context()); err == nil {
		for _, c := range all {
			if c.TargetDomain == domain {
				waiting = append(waiting, c)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"target_domain": domain,
		"events":        events,
		"trials":        trials,
		"waiting":       waiting,
	})
}
