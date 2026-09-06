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
	// Mode is "sticky" (default — same proxy per (machine, domain)) or
	// "rotate" (fresh random proxy per request, no consecutive repeats).
	// Empty deserializes to "sticky" downstream.
	Mode string `json:"mode"`
}

// EffectiveMode is Mode with "" normalized to "sticky" so the UI never has
// to render an empty string.
func (e ListenerEntry) EffectiveMode() string {
	if e.Mode == "rotate" {
		return "rotate"
	}
	return "sticky"
}

// String renders the entry in the AUX_LISTENERS env format
// (`machine_id/country:port` or `machine_id/country:port:rotate`).
func (e ListenerEntry) String() string {
	base := fmt.Sprintf("%s/%s:%d", e.MachineID, e.Country, e.Port)
	if e.Mode == "rotate" {
		return base + ":rotate"
	}
	return base
}

// ApplyResult reports what a mutation did to the live listener sockets.
// Ports in Failed kept their .env entry but could not be bound — almost
// always because another process holds the port.
type ApplyResult struct {
	Added   []int          `json:"added"`
	Removed []int          `json:"removed"`
	Rebound []int          `json:"rebound"`
	Failed  map[int]string `json:"failed,omitempty"`
}

// ListenerBinder applies a desired listener set to the running process.
// Implemented by the proxy package's aux registry and injected at startup, so
// this package stays free of any socket handling.
type ListenerBinder interface {
	Apply(specs []config.AuxListenerConfig) ApplyResult
	ActivePorts() []int
}

// ListenerSync owns the read/write pipeline for the sheet-managed listener
// list. It holds a reference to *Config because we mutate AuxListenersSheet
// in-place after a successful write, and a ListenerBinder so the change also
// reaches the running sockets.
//
// Registering a port used to be a two-step affair: the UI wrote the .env, then
// a human restarted the service so the port would bind. The binder closes that
// gap — one port opens or closes on its own and every other listener keeps its
// connections. When no binder is wired (tests, or a build that doesn't run the
// proxy) writes still work and simply don't take effect until the next start.
type ListenerSync struct {
	cfg    *config.Config
	binder ListenerBinder
	mu     sync.Mutex // serialises Add/Delete so concurrent writes can't lose entries
}

// NewListenerSync wires the service against the live config pointer.
func NewListenerSync(cfg *config.Config) *ListenerSync {
	return &ListenerSync{cfg: cfg}
}

// SetBinder attaches the live socket registry. Called once at startup.
func (s *ListenerSync) SetBinder(b ListenerBinder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binder = b
}

// applyLive pushes the current desired set (manual + managed, exactly what a
// restart would bind) at the running listeners and returns what changed.
// Callers must hold s.mu. Returns nil when no binder is attached.
func (s *ListenerSync) applyLive() *ApplyResult {
	if s.binder == nil {
		return nil
	}
	desired := make([]config.AuxListenerConfig, 0, len(s.cfg.AuxListeners)+len(s.cfg.AuxListenersSheet))
	desired = append(desired, s.cfg.AuxListeners...)
	desired = append(desired, s.cfg.AuxListenersSheet...)
	res := s.binder.Apply(desired)
	return &res
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

// ErrNotFound means the caller asked to mutate an entry whose port isn't
// present in either managed or manual lists. Surfaced as 404 by the handler.
var ErrNotFound = errors.New("listener not found")

// State is the bundle returned by List + after every mutation: the full
// managed entry list, plus the .env path so the UI can render "writing
// to /path/to/.env" in error messages.
type State struct {
	Entries       []ListenerEntry `json:"entries"`
	Manual        []ListenerEntry `json:"manual"`
	EnvPath       string          `json:"env_path"`
	FleetMachines []string        `json:"fleet_machines"`

	// ActivePorts is what is bound right now, which is the honest answer to
	// "can I connect to this?" — the entry list only says what was configured.
	// The two diverge when a port is held by another process.
	ActivePorts []int `json:"active_ports"`

	// Applied is set on the responses to Add/Update/Delete and describes what
	// the mutation did to the live sockets. Absent on plain List.
	Applied *ApplyResult `json:"applied,omitempty"`
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
	in.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	if in.MachineID == "" {
		return s.snapshot(), fmt.Errorf("%w: machine_id is required", ErrInvalidInput)
	}
	if in.Country == "" {
		return s.snapshot(), fmt.Errorf("%w: country is required", ErrInvalidInput)
	}
	if in.Port < 1 || in.Port > 65535 {
		return s.snapshot(), fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidInput)
	}
	if in.Mode != "" && in.Mode != "sticky" && in.Mode != "rotate" {
		return s.snapshot(), fmt.Errorf("%w: mode must be sticky or rotate", ErrInvalidInput)
	}
	if in.Mode == "sticky" {
		// Store the default as empty so old parsers stay happy and diff-friendly.
		in.Mode = ""
	}
	if !models.IsValidMachineID(in.MachineID) {
		return s.snapshot(), fmt.Errorf("%w: %q (must be one of %s)", ErrUnknownMachine, in.MachineID, strings.Join(models.FleetMachineIDs(), ", "))
	}
	if owner := s.portOwner(in.Port); owner != "" {
		return s.snapshot(), fmt.Errorf("%w: port %d is already used by %s", ErrPortInUse, in.Port, owner)
	}

	entries := append(cloneEntries(s.cfg.AuxListenersSheet), in)
	if err := s.persistManaged(entries); err != nil {
		return s.snapshot(), err
	}
	return s.applied(), nil
}

// ListenerPatch is the wire shape for partial listener updates. Every field
// is a pointer so we can distinguish "not provided" from "cleared to zero".
type ListenerPatch struct {
	MachineID *string `json:"machine_id,omitempty"`
	Country   *string `json:"country,omitempty"`
	Port      *int    `json:"port,omitempty"`
	Mode      *string `json:"mode,omitempty"`
}

// Update applies `patch` to the listener currently registered on `port`.
// Only non-nil fields are changed; everything else keeps its current value.
// Works on entries in either the managed or manual list — the entry stays
// in whichever list it was in.
//
// If Port is changed, the new port is checked for collisions against every
// other entry (both lists). Returns ErrNotFound if the source port isn't
// registered, ErrInvalidInput on shape errors, ErrUnknownMachine if a new
// machine_id isn't in the Fleet, and ErrPortInUse if a new port collides.
func (s *ListenerSync) Update(port int, patch ListenerPatch) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the entry by port — it's either in managed or manual.
	managedIdx := findPortIndex(s.cfg.AuxListenersSheet, port)
	manualIdx := findPortIndex(s.cfg.AuxListeners, port)
	if managedIdx < 0 && manualIdx < 0 {
		return s.snapshot(), fmt.Errorf("%w: no listener on port %d", ErrNotFound, port)
	}

	// Build the target entry by applying the patch on top of the existing.
	var current config.AuxListenerConfig
	inManaged := managedIdx >= 0
	if inManaged {
		current = s.cfg.AuxListenersSheet[managedIdx]
	} else {
		current = s.cfg.AuxListeners[manualIdx]
	}

	updated := current
	if patch.MachineID != nil {
		updated.MachineID = strings.TrimSpace(*patch.MachineID)
	}
	if patch.Country != nil {
		updated.Country = strings.TrimSpace(*patch.Country)
	}
	if patch.Port != nil {
		updated.Port = *patch.Port
	}
	if patch.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*patch.Mode))
		switch mode {
		case "", "sticky":
			updated.Mode = ""
		case "rotate":
			updated.Mode = "rotate"
		default:
			return s.snapshot(), fmt.Errorf("%w: mode must be sticky or rotate", ErrInvalidInput)
		}
	}

	// Validate the result.
	if updated.MachineID == "" {
		return s.snapshot(), fmt.Errorf("%w: machine_id is required", ErrInvalidInput)
	}
	if updated.Country == "" {
		return s.snapshot(), fmt.Errorf("%w: country is required", ErrInvalidInput)
	}
	if updated.Port < 1 || updated.Port > 65535 {
		return s.snapshot(), fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidInput)
	}
	if !models.IsValidMachineID(updated.MachineID) {
		return s.snapshot(), fmt.Errorf("%w: %q (must be one of %s)", ErrUnknownMachine, updated.MachineID, strings.Join(models.FleetMachineIDs(), ", "))
	}

	// Port collision: any OTHER entry (in either list) already using the
	// target port. The entry we're editing is always allowed to keep its
	// own port.
	if updated.Port != current.Port {
		if owner := s.portOwner(updated.Port); owner != "" {
			return s.snapshot(), fmt.Errorf("%w: port %d is already used by %s", ErrPortInUse, updated.Port, owner)
		}
	}

	// Write back into the correct list.
	if inManaged {
		out := make([]config.AuxListenerConfig, len(s.cfg.AuxListenersSheet))
		copy(out, s.cfg.AuxListenersSheet)
		out[managedIdx] = updated
		if err := s.persistManaged(configToEntries(out)); err != nil {
			return s.snapshot(), err
		}
	} else {
		out := make([]config.AuxListenerConfig, len(s.cfg.AuxListeners))
		copy(out, s.cfg.AuxListeners)
		out[manualIdx] = updated
		if err := s.persistManual(configToEntries(out)); err != nil {
			return s.snapshot(), err
		}
	}
	return s.applied(), nil
}

// findPortIndex returns the index of the entry on `port` in `in`, or -1.
func findPortIndex(in []config.AuxListenerConfig, port int) int {
	for i, e := range in {
		if e.Port == port {
			return i
		}
	}
	return -1
}

// Delete removes the entry on the given port from whichever list it lives
// in (managed AUX_LISTENERS_SHEET first, then manual AUX_LISTENERS). If it
// isn't in either list the call is a no-op so the UI's delete-after-stale-
// refresh case doesn't 404. Only the line that actually contained the port
// gets rewritten — the other list is untouched.
func (s *ListenerSync) Delete(port int) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if next, removed := withoutPort(s.cfg.AuxListenersSheet, port); removed {
		if err := s.persistManaged(configToEntries(next)); err != nil {
			return s.snapshot(), err
		}
		return s.applied(), nil
	}
	if next, removed := withoutPort(s.cfg.AuxListeners, port); removed {
		if err := s.persistManual(configToEntries(next)); err != nil {
			return s.snapshot(), err
		}
		return s.applied(), nil
	}
	return s.snapshot(), nil
}

// withoutPort returns a copy of in with any entry on the given port removed,
// plus a bool reporting whether anything was actually removed.
func withoutPort(in []config.AuxListenerConfig, port int) ([]config.AuxListenerConfig, bool) {
	out := make([]config.AuxListenerConfig, 0, len(in))
	removed := false
	for _, e := range in {
		if e.Port == port {
			removed = true
			continue
		}
		out = append(out, e)
	}
	return out, removed
}

// ─────────────────────────────────────────────────────────────────────────────
// internals
// ─────────────────────────────────────────────────────────────────────────────

// snapshot builds a State from the current cfg. Callers must hold s.mu.
func (s *ListenerSync) snapshot() State {
	st := State{
		Entries:       cloneEntries(s.cfg.AuxListenersSheet),
		Manual:        cloneEntries(s.cfg.AuxListeners),
		EnvPath:       s.cfg.EnvFilePath,
		FleetMachines: models.FleetMachineIDs(),
	}
	if s.binder != nil {
		st.ActivePorts = s.binder.ActivePorts()
	}
	return st
}

// applied runs the reconcile and returns the snapshot carrying its result.
// Every successful mutation ends with this so the caller learns which ports
// are live, not merely which ones were written. Callers must hold s.mu.
func (s *ListenerSync) applied() State {
	res := s.applyLive()
	st := s.snapshot()
	st.Applied = res
	return st
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

// persistManaged sorts entries, rewrites AUX_LISTENERS_SHEET in the .env,
// and updates the live config.AuxListenersSheet. Callers must hold s.mu.
func (s *ListenerSync) persistManaged(entries []ListenerEntry) error {
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

// persistManual sorts entries, rewrites AUX_LISTENERS in the .env, and
// updates the live config.AuxListeners. Used when the UI deletes an entry
// that was originally hand-maintained. Callers must hold s.mu.
func (s *ListenerSync) persistManual(entries []ListenerEntry) error {
	if s.cfg.EnvFilePath == "" {
		return ErrNoEnvFile
	}
	sortEntries(entries)
	if err := writeManualEnv(s.cfg.EnvFilePath, entries); err != nil {
		return fmt.Errorf("write .env: %w", err)
	}
	s.cfg.AuxListeners = entriesToConfig(entries)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Conversions + sort
// ─────────────────────────────────────────────────────────────────────────────

func cloneEntries(in []config.AuxListenerConfig) []ListenerEntry {
	out := make([]ListenerEntry, 0, len(in))
	for _, e := range in {
		out = append(out, ListenerEntry{MachineID: e.MachineID, Country: e.Country, Port: e.Port, Mode: e.Mode})
	}
	sortEntries(out)
	return out
}

func configToEntries(in []config.AuxListenerConfig) []ListenerEntry {
	out := make([]ListenerEntry, 0, len(in))
	for _, e := range in {
		out = append(out, ListenerEntry{MachineID: e.MachineID, Country: e.Country, Port: e.Port, Mode: e.Mode})
	}
	return out
}

func entriesToConfig(in []ListenerEntry) []config.AuxListenerConfig {
	out := make([]config.AuxListenerConfig, 0, len(in))
	for _, e := range in {
		out = append(out, config.AuxListenerConfig{MachineID: e.MachineID, Country: e.Country, Port: e.Port, Mode: e.Mode})
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

// writeManagedEnv rewrites the AUX_LISTENERS_SHEET line of the .env file.
func writeManagedEnv(envPath string, entries []ListenerEntry) error {
	return writeEnvKey(envPath, "AUX_LISTENERS_SHEET", renderEnvValue(entries),
		"# === managed by /dashboard/listener-sync — add/remove there, not here ===")
}

// writeManualEnv rewrites the AUX_LISTENERS line of the .env file. Used
// when the UI deletes an entry that lives in the manually-maintained list.
func writeManualEnv(envPath string, entries []ListenerEntry) error {
	return writeEnvKey(envPath, "AUX_LISTENERS", renderEnvValue(entries), "")
}

// writeEnvKey rewrites the line for `<key>=` in the .env file at envPath,
// preserving every other line (including comments and other env values). If
// the key isn't present, it's appended at the end — optionally under
// appendComment, which lets the managed-block line explain to whoever opens
// the file that a UI is rewriting it.
//
// Writes to a temp sibling + rename so a crash mid-write can't leave a
// half-empty .env that fails to load on the next start.
func writeEnvKey(envPath, key, value, appendComment string) error {
	original, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}

	prefix := key + "="
	lines := splitKeepEndings(string(original))
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, prefix) {
			ending := lineEnding(line)
			lines[i] = prefix + value + ending
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
		lines = append(lines, "\n")
		if appendComment != "" {
			lines = append(lines, appendComment+"\n")
		}
		lines = append(lines, prefix+value+"\n")
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
