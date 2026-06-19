package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/alpkeskin/rota/core/internal/services"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/go-chi/chi/v5"
)

// ListenerSyncHandler exposes CRUD on the sheet-managed aux listener list:
//
//	GET    /api/v1/admin/listeners         → current state
//	POST   /api/v1/admin/listeners         → add one entry
//	DELETE /api/v1/admin/listeners/{port}  → remove the entry on that port
//
// Mutations rewrite AUX_LISTENERS_SHEET in the loaded .env file. The aux
// listeners themselves still bind only at server start — the dashboard
// shows a "restart required" banner after every successful mutation.
type ListenerSyncHandler struct {
	sync   *services.ListenerSync
	logger *logger.Logger
}

func NewListenerSyncHandler(sync *services.ListenerSync, log *logger.Logger) *ListenerSyncHandler {
	return &ListenerSyncHandler{sync: sync, logger: log}
}

// List handles GET /api/v1/admin/listeners.
//
//	@Summary	List managed aux listeners + manual entries + Fleet ids
//	@Tags		admin
//	@Produce	json
//	@Success	200	{object}	services.State
//	@Router		/admin/listeners [get]
func (h *ListenerSyncHandler) List(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.sync.List())
}

// Add handles POST /api/v1/admin/listeners.
//
//	@Summary	Register a new (machine, country) → port listener
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		entry	body		services.ListenerEntry	true	"Listener spec"
//	@Success	200		{object}	services.State
//	@Failure	400		{object}	map[string]string "invalid input / unknown machine"
//	@Failure	409		{object}	map[string]string "port already in use"
//	@Failure	500		{object}	map[string]string
//	@Router		/admin/listeners [post]
func (h *ListenerSyncHandler) Add(w http.ResponseWriter, r *http.Request) {
	var in services.ListenerEntry
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	state, err := h.sync.Add(in)
	if err != nil {
		writeListenerError(w, h.logger, "listener add failed", err)
		return
	}
	h.logger.Info("listener added", "machine_id", in.MachineID, "country", in.Country, "port", in.Port)
	writeJSON(w, http.StatusOK, state)
}

// Delete handles DELETE /api/v1/admin/listeners/{port}.
//
//	@Summary	Remove the managed listener bound to {port}
//	@Tags		admin
//	@Produce	json
//	@Param		port	path		int	true	"Port"
//	@Success	200		{object}	services.State
//	@Failure	400		{object}	map[string]string "bad port"
//	@Failure	500		{object}	map[string]string
//	@Router		/admin/listeners/{port} [delete]
func (h *ListenerSyncHandler) Delete(w http.ResponseWriter, r *http.Request) {
	portStr := chi.URLParam(r, "port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "port must be an integer between 1 and 65535"})
		return
	}
	state, err := h.sync.Delete(port)
	if err != nil {
		writeListenerError(w, h.logger, "listener delete failed", err)
		return
	}
	h.logger.Info("listener removed", "port", port)
	writeJSON(w, http.StatusOK, state)
}

// writeListenerError maps service-level errors to HTTP status codes.
func writeListenerError(w http.ResponseWriter, log *logger.Logger, msg string, err error) {
	switch {
	case errors.Is(err, services.ErrInvalidInput),
		errors.Is(err, services.ErrUnknownMachine):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, services.ErrPortInUse):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, services.ErrNoEnvFile):
		log.Error(msg, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "server didn't load a .env file at startup — can't persist. Restart the server with its working directory next to core/.env.",
		})
	default:
		log.Error(msg, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
