// Package services contains feature-level orchestration that doesn't fit
// neatly into a repository or a single HTTP handler. listener_sync drives
// the dashboard's "Aux Listeners" CRUD page: list, add, and delete
// (machine, country, port) entries that the server binds at startup. The
// entries are persisted as the AUX_LISTENERS_SHEET line of the loaded .env
// file; manual entries in AUX_LISTENERS are never touched.
package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/alpkeskin/rota/core/internal/config"
	"github.com/alpkeskin/rota/core/internal/models"
)

// ListenerEntry is one (machine, country) → port row.
type ListenerEntry struct {
	MachineID string `json:"machine_id"`
	Country   string `json:"country"`
	Port      int    `json:"port"`
}

// String renders the entry in the AUX_LISTENERS env format
// (`machine_id/country:port`).
func (e ListenerEntry) String() string {
	return fmt.Sprintf("%s/%s:%d", e.MachineID, e.Country, e.Port)
}

// ListenerSync owns the read/write pipeline for the sheet-managed listener
// list. It holds a reference to *Config because we mutate
// AuxListenersSheet in-place after a successful write so the dashboard
// reflects the new state without waiting for a restart (the actual aux
// listeners still need a restart to bind new ports — that's the restart
// banner in the UI).
type ListenerSync struct {
	cfg *config.Config
	mu  sync.Mutex // serialises Add/Delete so concurrent writes can't lose entries
}

// NewListenerSync wires the service against the live config pointer.
func NewListenerSync(cfg *config.Config) *ListenerSync {
	return &ListenerSync{cfg: cfg}
}

// ErrPortInUse means the requested port is already claimed by another
// listener (managed or manual). Surfaced as 409 by the handler.
var ErrPortInUse = errors.New("port already in use")

// ErrUnknownMachine means the requested machine_id isn't in models.Fleet.
// Surfaced as 400 by the handler.
var ErrUnknownMachine = errors.New("unknown machine_id")

// ErrInvalidInput covers shape problems (empty country, port out of range,
// etc.). Surfaced as 400 by the handler.
var ErrInvalidInput = errors.New("invalid listener input")

// ErrNoEnvFile means the server didn't load a .env file at startup, so we
// have nowhere to persist. Surfaced as 500 by the handler.
var ErrNoEnvFile = errors.New("no .env file to write")

// State is the bundle returned by List + after every mutation: the full
// managed entry list, plus the .env path so the UI can render "writing
// to /path/to/.env" in error messages.
type State struct {
	Entries       []ListenerEntry `json:"entries"`
	Manual        []ListenerEntry `json:"manual"`
	EnvPath       string          `json:"env_path"`
	FleetMachines []string        `json:"fleet_machines"`
}

// List returns the current managed entries (sorted by port), the read-only
// manual entries (for display), the .env path, and the Fleet machine ids
// for the dashboard dropdown. Single round-trip on page load.
func (s *ListenerSync) List() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

// Add validates and appends a new entry, then rewrites the .env. Returns
// the updated state on success.
func (s *ListenerSync) Add(in ListenerEntry) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	in.MachineID = strings.TrimSpace(in.MachineID)
	in.Country = strings.TrimSpace(in.Country)
	if in.MachineID == "" {
		return s.snapshot(), fmt.Errorf("%w: machine_id is required", ErrInvalidInput)
	}
	if in.Country == "" {
		return s.snapshot(), fmt.Errorf("%w: country is required", ErrInvalidInput)
	}
	if in.Port < 1 || in.Port > 65535 {
		return s.snapshot(), fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidInput)
	}
	if !models.IsValidMachineID(in.MachineID) {
		return s.snapshot(), fmt.Errorf("%w: %q (must be one of %s)", ErrUnknownMachine, in.MachineID, strings.Join(models.FleetMachineIDs(), ", "))
	}
	if owner := s.portOwner(in.Port); owner != "" {
		return s.snapshot(), fmt.Errorf("%w: port %d is already used by %s", ErrPortInUse, in.Port, owner)
	}

	entries := append(cloneEntries(s.cfg.AuxListenersSheet), in)
	if err := s.persist(entries); err != nil {
		return s.snapshot(), err
	}
	return s.snapshot(), nil
}

// Delete removes the entry on the given port from AUX_LISTENERS_SHEET. If
// the port isn't in the managed set the call is a no-op (so the UI's
// delete-after-stale-refresh case doesn't 404). Manual entries are NEVER
// removed by this method — only the AUX_LISTENERS_SHEET section is touched.
func (s *ListenerSync) Delete(port int) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	curr := s.cfg.AuxListenersSheet
	out := make([]config.AuxListenerConfig, 0, len(curr))
	removed := false
	for _, e := range curr {
		if e.Port == port {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return s.snapshot(), nil
	}
	entries := configToEntries(out)
	if err := s.persist(entries); err != nil {
		return s.snapshot(), err
	}
	return s.snapshot(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// internals
// ─────────────────────────────────────────────────────────────────────────────

// snapshot builds a State from the current cfg. Callers must hold s.mu.
func (s *ListenerSync) snapshot() State {
	return State{
		Entries:       cloneEntries(s.cfg.AuxListenersSheet),
		Manual:        cloneEntries(s.cfg.AuxListeners),
		EnvPath:       s.cfg.EnvFilePath,
		FleetMachines: models.FleetMachineIDs(),
	}
}

// portOwner reports which existing listener claims port p, or "" if free.
// Checks BOTH managed and manual lists so the user can't accidentally pick
// a port already used elsewhere — the listener wouldn't bind at startup.
func (s *ListenerSync) portOwner(p int) string {
	for _, e := range s.cfg.AuxListenersSheet {
		if e.Port == p {
			return fmt.Sprintf("a sheet-managed listener (%s/%s)", labelMachine(e.MachineID), e.Country)
		}
	}
	for _, e := range s.cfg.AuxListeners {
		if e.Port == p {
			return fmt.Sprintf("the manual AUX_LISTENERS entry %s/%s:%d", labelMachine(e.MachineID), e.Country, e.Port)
		}
	}
	return ""
}

// labelMachine returns the machine id, or "(default)" when empty (which
// means "use ROUTING_DEFAULT_MACHINE" at runtime).
func labelMachine(id string) string {
	if id == "" {
		return "(default)"
	}
	return id
}

// persist sorts the entries, rewrites the .env, and updates the live config.
// Callers must hold s.mu.
func (s *ListenerSync) persist(entries []ListenerEntry) error {
	if s.cfg.EnvFilePath == "" {
		return ErrNoEnvFile
	}
	sortEntries(entries)
	if err := writeManagedEnv(s.cfg.EnvFilePath, entries); err != nil {
		return fmt.Errorf("write .env: %w", err)
	}
	s.cfg.AuxListenersSheet = entriesToConfig(entries)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Conversions + sort
// ─────────────────────────────────────────────────────────────────────────────

func cloneEntries(in []config.AuxListenerConfig) []ListenerEntry {
	out := make([]ListenerEntry, 0, len(in))
	for _, e := range in {
		out = append(out, ListenerEntry{MachineID: e.MachineID, Country: e.Country, Port: e.Port})
	}
	sortEntries(out)
	return out
}

func configToEntries(in []config.AuxListenerConfig) []ListenerEntry {
	out := make([]ListenerEntry, 0, len(in))
	for _, e := range in {
		out = append(out, ListenerEntry{MachineID: e.MachineID, Country: e.Country, Port: e.Port})
	}
	return out
}

func entriesToConfig(in []ListenerEntry) []config.AuxListenerConfig {
	out := make([]config.AuxListenerConfig, 0, len(in))
	for _, e := range in {
		out = append(out, config.AuxListenerConfig{MachineID: e.MachineID, Country: e.Country, Port: e.Port})
	}
	return out
}

func sortEntries(in []ListenerEntry) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Port != in[j].Port {
			return in[i].Port < in[j].Port
		}
		if in[i].MachineID != in[j].MachineID {
			return in[i].MachineID < in[j].MachineID
		}
		return in[i].Country < in[j].Country
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// .env rewrite
// ─────────────────────────────────────────────────────────────────────────────

// renderEnvValue produces the comma-joined value that goes after
// `AUX_LISTENERS_SHEET=` in the .env file. Each entry is `machine/country:port`.
func renderEnvValue(entries []ListenerEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, ",")
}

// writeManagedEnv rewrites only the AUX_LISTENERS_SHEET line of the .env
// file at envPath, preserving every other line (including comments and the
// manual AUX_LISTENERS entry). If the key isn't present, it's appended at
// the end under a marker comment so the next write can find and update it.
//
// We write to a temp sibling and rename, so a crash mid-write can't leave a
// half-empty .env that fails to load on the next start.
func writeManagedEnv(envPath string, entries []ListenerEntry) error {
	original, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}

	value := renderEnvValue(entries)
	lines := splitKeepEndings(string(original))
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "AUX_LISTENERS_SHEET=") {
			ending := lineEnding(line)
			lines[i] = "AUX_LISTENERS_SHEET=" + value + ending
			replaced = true
			break
		}
	}
	if !replaced {
		// Make sure the file ends with a newline before we append.
		if len(lines) > 0 {
			last := lines[len(lines)-1]
			if !strings.HasSuffix(last, "\n") {
				lines[len(lines)-1] = last + "\n"
			}
		}
		lines = append(lines,
			"\n",
			"# === managed by /dashboard/listeners — edit there, not here ===\n",
			"AUX_LISTENERS_SHEET="+value+"\n",
		)
	}

	tmp := envPath + ".tmp"
	out := strings.Join(lines, "")
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, envPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", filepath.Base(envPath), err)
	}
	return nil
}

// splitKeepEndings splits a file into lines while preserving the original
// line endings on each line. Necessary because Windows .env files often use
// CRLF and we don't want to silently switch the whole file to LF on write.
func splitKeepEndings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			out = append(out, s)
			return out
		}
		out = append(out, s[:idx+1])
		s = s[idx+1:]
	}
}

func lineEnding(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	default:
		return ""
	}
}
