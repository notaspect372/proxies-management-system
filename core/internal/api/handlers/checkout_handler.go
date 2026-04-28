package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// CheckoutHandler exposes the scraper-facing proxy assignment endpoint plus
// the dashboard infrastructure tree.
type CheckoutHandler struct {
	assignments *repository.AssignmentRepository
	logger      *logger.Logger
}

func NewCheckoutHandler(assignments *repository.AssignmentRepository, log *logger.Logger) *CheckoutHandler {
	return &CheckoutHandler{assignments: assignments, logger: log}
}

// Checkout handles GET /api/v1/proxy.
//
//	@Summary		Check out a proxy for a scraper
//	@Description	Returns a proxy URL for the (machine_id, domain) pair. Sticky:
//	@Description	the same proxy is returned until it goes unhealthy. Optional
//	@Description	country filter.
//	@Tags			proxies
//	@Produce		json
//	@Param			machine_id	query		string	true	"Worker identifier (e.g. main_machine_vm1)"
//	@Param			domain		query		string	true	"Target domain (e.g. example.com)"
//	@Param			country		query		string	false	"Filter to proxies from this country"
//	@Success		200			{object}	models.ProxyCheckoutResponse
//	@Failure		400			{object}	models.ErrorResponse
//	@Failure		404			{object}	models.ErrorResponse
//	@Router			/proxy [get]
func (h *CheckoutHandler) Checkout(w http.ResponseWriter, r *http.Request) {
	machineID := strings.TrimSpace(r.URL.Query().Get("machine_id"))
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	country := strings.TrimSpace(r.URL.Query().Get("country"))

	if machineID == "" {
		h.errorResponse(w, http.StatusBadRequest, "machine_id is required")
		return
	}
	if domain == "" {
		h.errorResponse(w, http.StatusBadRequest, "domain is required")
		return
	}
	if !models.IsValidMachineID(machineID) {
		h.errorResponse(w, http.StatusBadRequest, fmt.Sprintf(
			"unknown machine_id %q — must be one of: %s",
			machineID, strings.Join(models.FleetMachineIDs(), ", "),
		))
		return
	}

	proxy, sticky, err := h.assignments.Checkout(r.Context(), machineID, domain, country)
	if err != nil {
		if errors.Is(err, repository.ErrNoEligibleProxy) {
			msg := "no eligible proxy available"
			if country != "" {
				msg += fmt.Sprintf(" for country=%s", country)
			}
			h.errorResponse(w, http.StatusNotFound, msg)
			return
		}
		h.logger.Error("proxy checkout failed", "error", err, "machine_id", machineID, "domain", domain)
		h.errorResponse(w, http.StatusInternalServerError, "checkout failed")
		return
	}

	resp := models.ProxyCheckoutResponse{
		ProxyID:       proxy.ID,
		Address:       proxy.Address,
		Protocol:      proxy.Protocol,
		Username:      proxy.Username,
		Password:      proxy.Password,
		Country:       proxy.Country,
		Category:      proxy.Category,
		URL:           buildProxyURL(proxy),
		Sticky:        sticky,
		MachineID:     machineID,
		Domain:        domain,
		TargetCountry: country,
	}

	h.logger.Info("proxy checkout",
		"source", "checkout",
		"machine_id", machineID,
		"domain", domain,
		"country", country,
		"proxy_id", proxy.ID,
		"sticky", sticky,
	)

	h.jsonResponse(w, http.StatusOK, resp)
}

// Release handles DELETE /api/v1/proxy. Removes the sticky binding so the
// next checkout for (machine_id, domain) picks a fresh proxy.
//
//	@Summary		Release a sticky proxy assignment
//	@Description	Drops the (machine_id, domain) binding so the next checkout picks fresh.
//	@Tags			proxies
//	@Produce		json
//	@Param			machine_id	query	string	true	"Worker identifier"
//	@Param			domain		query	string	true	"Target domain"
//	@Success		204
//	@Failure		400	{object}	models.ErrorResponse
//	@Router			/proxy [delete]
func (h *CheckoutHandler) Release(w http.ResponseWriter, r *http.Request) {
	machineID := strings.TrimSpace(r.URL.Query().Get("machine_id"))
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if machineID == "" || domain == "" {
		h.errorResponse(w, http.StatusBadRequest, "machine_id and domain are required")
		return
	}
	if err := h.assignments.Release(r.Context(), machineID, domain); err != nil {
		h.logger.Error("release failed", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "release failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Infrastructure handles GET /api/v1/infrastructure.
//
//	@Summary		Fleet topology with live proxy assignments
//	@Description	Returns the hardcoded machine/VM topology grouped by the
//	@Description	target country each scraper is currently working on.
//	@Tags			infrastructure
//	@Produce		json
//	@Success		200	{object}	models.InfrastructureResponse
//	@Router			/infrastructure [get]
func (h *CheckoutHandler) Infrastructure(w http.ResponseWriter, r *http.Request) {
	assignments, err := h.assignments.ListWithProxies(r.Context())
	if err != nil {
		h.logger.Error("infrastructure list failed", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "failed to load assignments")
		return
	}

	// Map machine_id (host or VM) → its parent host id, so a VM's assignments
	// roll up to the physical machine for the country grouping.
	parentOf := map[string]string{}
	for _, m := range models.Fleet {
		parentOf[m.ID] = m.ID
		for _, vm := range m.VMs {
			parentOf[vm.ID] = m.ID
		}
	}

	// Bucket: machine_root → target_country → assignments
	type bucketKey struct{ machine, country string }
	groups := map[bucketKey][]models.AssignmentWithProxy{}
	for _, a := range assignments {
		root, ok := parentOf[a.MachineID]
		if !ok {
			// Foreign machine_id — drop it from the UI (could happen if a
			// machine was retired from Fleet but still has rows).
			continue
		}
		country := a.TargetCountry
		if country == "" {
			country = "Untagged"
		}
		key := bucketKey{root, country}
		groups[key] = append(groups[key], a)
	}

	machines := make([]models.InfrastructureMachine, 0, len(models.Fleet))
	for _, m := range models.Fleet {
		// Collect every country group present for this machine.
		var countryGroups []models.InfrastructureCountryGroup
		seen := map[string]bool{}
		total := 0
		for k, as := range groups {
			if k.machine != m.ID || seen[k.country] {
				continue
			}
			seen[k.country] = true

			active := 0
			for _, a := range as {
				if a.ProxyStatus == "active" {
					active++
				}
			}
			countryGroups = append(countryGroups, models.InfrastructureCountryGroup{
				TargetCountry: k.country,
				ActiveCount:   active,
				TotalCount:    len(as),
				Assignments:   as,
			})
			total += len(as)
		}

		machines = append(machines, models.InfrastructureMachine{
			ID:            m.ID,
			Name:          m.Name,
			Hostname:      m.Hostname,
			Kind:          m.Kind,
			VMs:           m.VMs,
			CountryGroups: countryGroups,
			TotalAssigned: total,
		})
	}

	h.jsonResponse(w, http.StatusOK, models.InfrastructureResponse{Machines: machines})
}

// buildProxyURL produces a URL the scraper can pass directly to its HTTP
// client (e.g. http://user:pass@host:port). Falls back gracefully when no
// credentials are set.
func buildProxyURL(p *models.Proxy) string {
	scheme := p.Protocol
	if scheme == "" {
		scheme = "http"
	}
	u := url.URL{Scheme: scheme, Host: p.Address}
	if p.Username != nil && *p.Username != "" {
		if p.Password != nil {
			u.User = url.UserPassword(*p.Username, *p.Password)
		} else {
			u.User = url.User(*p.Username)
		}
	}
	return u.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers (mirror the pattern used by other handlers)
// ─────────────────────────────────────────────────────────────────────────────

func (h *CheckoutHandler) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (h *CheckoutHandler) errorResponse(w http.ResponseWriter, status int, message string) {
	h.jsonResponse(w, status, models.ErrorResponse{Error: message})
}
