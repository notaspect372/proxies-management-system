package proxy

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// AuxListenerSpec describes one (machine, country)→port aux listener. When
// MachineID is empty the listener falls back to defaultMachineID at start.
// Mode is "" (sticky) or "rotate" — rotate mode signals the main proxy to
// pick a fresh random proxy per request, avoiding consecutive repeats.
type AuxListenerSpec struct {
	MachineID string
	Country   string
	Port      int
	Mode      string
}

// key identifies a listener's routing behaviour. Two specs with the same key
// on the same port are interchangeable, so a reconcile can leave the socket
// alone. Any difference means the socket has to be rebound with new
// credentials.
func (s AuxListenerSpec) key() string {
	return s.MachineID + "|" + s.Country + "|" + s.Mode
}

// AuxRegistry owns the live aux listener sockets and can reconcile them
// against a new desired set while the process keeps running.
//
// Aux listeners used to bind once at startup, which meant registering a port
// through the dashboard only wrote it to the .env — someone still had to
// restart the whole service before anything could connect. That restart drops
// every in-flight connection on every other port too, so the cost of adding
// one listener was a fleet-wide blip. The registry exists so a single port can
// be opened or closed on its own, leaving the rest untouched.
type AuxRegistry struct {
	mu               sync.Mutex
	defaultMachineID string
	listenAddr       string
	mainPort         int
	log              *logger.Logger
	active           map[int]*auxListener
}

// auxListener is one bound socket plus the spec it was bound for.
type auxListener struct {
	spec     AuxListenerSpec
	listener net.Listener
}

// AuxDelta reports what a reconcile actually changed, so the caller can tell
// the operator which ports are live now instead of guessing.
type AuxDelta struct {
	Added   []int `json:"added"`
	Removed []int `json:"removed"`
	// Rebound are ports that stayed open but now carry different routing
	// credentials (machine, country or mode changed).
	Rebound []int `json:"rebound"`
	// Failed maps a port to why it could not be bound. The desired config is
	// already persisted at this point, so a failure here is reported rather
	// than rolled back — every other listener stays up.
	Failed map[int]string `json:"failed,omitempty"`
}

// Changed reports whether the reconcile did anything at all.
func (d AuxDelta) Changed() bool {
	return len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Rebound) > 0 || len(d.Failed) > 0
}

// NewAuxRegistry builds an empty registry. listenAddr empty falls back to
// `127.0.0.1` so we don't accidentally expose the proxy to the LAN.
func NewAuxRegistry(defaultMachineID, listenAddr string, mainPort int, log *logger.Logger) *AuxRegistry {
	if listenAddr == "" {
		listenAddr = "127.0.0.1"
	}
	return &AuxRegistry{
		defaultMachineID: defaultMachineID,
		listenAddr:       listenAddr,
		mainPort:         mainPort,
		log:              log,
		active:           map[int]*auxListener{},
	}
}

// Start binds every spec, failing on the first error. Used at startup, where a
// listener that can't bind means the configuration is wrong and the operator
// should see it immediately rather than discover a silently missing port.
func (r *AuxRegistry) Start(specs []AuxListenerSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range specs {
		resolved, err := r.resolve(s)
		if err != nil {
			return err
		}
		if _, exists := r.active[resolved.Port]; exists {
			return fmt.Errorf("aux listener on port %d declared twice", resolved.Port)
		}
		ln, err := r.bind(resolved)
		if err != nil {
			return err
		}
		r.active[resolved.Port] = &auxListener{spec: resolved, listener: ln}
	}
	return nil
}

// Apply reconciles the running sockets against `desired` and reports what
// changed. Ports absent from desired are closed; new ports are opened; ports
// whose routing changed are closed and reopened.
//
// Unlike Start this does not abort on the first failure. The caller has
// already persisted the desired config, so the useful behaviour is to get as
// close to it as possible and report the ports that resisted — usually a port
// held by another process.
func (r *AuxRegistry) Apply(desired []AuxListenerSpec) AuxDelta {
	r.mu.Lock()
	defer r.mu.Unlock()

	delta := AuxDelta{}

	// Resolve first so an unusable spec is reported without disturbing
	// anything that is currently working.
	want := make(map[int]AuxListenerSpec, len(desired))
	for _, s := range desired {
		resolved, err := r.resolve(s)
		if err != nil {
			delta.fail(s.Port, err.Error())
			continue
		}
		if _, dup := want[resolved.Port]; dup {
			delta.fail(resolved.Port, "declared twice in the desired set")
			continue
		}
		want[resolved.Port] = resolved
	}

	// Close first, so a listener that moved from one port to another (or two
	// that swapped ports) frees its socket before the new bind is attempted.
	for port, live := range r.active {
		target, keep := want[port]
		if keep && target.key() == live.spec.key() {
			continue
		}
		_ = live.listener.Close()
		delete(r.active, port)
		if keep {
			delta.Rebound = append(delta.Rebound, port)
		} else {
			delta.Removed = append(delta.Removed, port)
			r.log.Info("aux routing listener stopped", "port", port,
				"machine_id", live.spec.MachineID, "country", live.spec.Country)
		}
	}

	for port, spec := range want {
		if _, live := r.active[port]; live {
			continue
		}
		ln, err := r.bind(spec)
		if err != nil {
			delta.fail(port, err.Error())
			// A rebound port that failed to come back is really a removal.
			delta.Rebound = removeInt(delta.Rebound, port)
			r.log.Error("aux listener rebind failed", "port", port, "error", err)
			continue
		}
		r.active[port] = &auxListener{spec: spec, listener: ln}
		if !containsInt(delta.Rebound, port) {
			delta.Added = append(delta.Added, port)
		}
	}

	sort.Ints(delta.Added)
	sort.Ints(delta.Removed)
	sort.Ints(delta.Rebound)
	return delta
}

// ActivePorts returns the ports currently bound, sorted. Lets the API report
// what is actually listening rather than what the config file wishes for.
func (r *AuxRegistry) ActivePorts() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, 0, len(r.active))
	for p := range r.active {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Close shuts every listener down. Used on graceful shutdown.
func (r *AuxRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for port, live := range r.active {
		_ = live.listener.Close()
		delete(r.active, port)
	}
}

func (d *AuxDelta) fail(port int, reason string) {
	if d.Failed == nil {
		d.Failed = map[int]string{}
	}
	d.Failed[port] = reason
}

func containsInt(in []int, v int) bool {
	for _, x := range in {
		if x == v {
			return true
		}
	}
	return false
}

func removeInt(in []int, v int) []int {
	out := in[:0]
	for _, x := range in {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// resolve fills in the default machine and validates the spec. Callers must
// hold r.mu.
func (r *AuxRegistry) resolve(s AuxListenerSpec) (AuxListenerSpec, error) {
	if s.MachineID == "" {
		s.MachineID = r.defaultMachineID
	}
	if s.MachineID == "" {
		return s, fmt.Errorf("aux listener on port %d has no machine_id and ROUTING_DEFAULT_MACHINE is unset", s.Port)
	}
	if !models.IsValidMachineID(s.MachineID) {
		return s, fmt.Errorf("aux listener on port %d: unknown machine_id %q", s.Port, s.MachineID)
	}
	if s.Country == "" {
		return s, fmt.Errorf("aux listener on port %d has empty country", s.Port)
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, fmt.Errorf("aux listener port %d out of range", s.Port)
	}
	return s, nil
}

// bind opens the socket and starts its accept loop. Callers must hold r.mu.
func (r *AuxRegistry) bind(s AuxListenerSpec) (net.Listener, error) {
	return startOneAuxListener(s.MachineID, s.Country, s.Mode, r.listenAddr, s.Port, r.mainPort, r.log)
}

// StartAuxListeners spawns a credential-injection listener for each spec and
// returns the registry owning them, so listeners can later be added or removed
// without restarting the process. Each listener:
//
//	• accepts incoming proxy clients on its port (no auth required)
//	• injects Proxy-Authorization: Basic base64(machineID:Country)
//	• forwards everything to the main proxy on mainPort
//
// Lets a scraper point at a single dedicated port per (machine, country)
// without embedding credentials in the proxy URL — handy when the client
// (e.g. Chrome under SB UC mode) won't send auth preemptively. The per-spec
// MachineID overrides defaultMachineID, which is useful when one Rota instance
// fronts scrapers running as different fleet machines.
//
// listenAddr controls the bind interface. Empty string falls back to
// `127.0.0.1` so we don't accidentally expose the proxy to the LAN.
func StartAuxListeners(defaultMachineID string, listenAddr string, mainPort int, specs []AuxListenerSpec, log *logger.Logger) (*AuxRegistry, error) {
	reg := NewAuxRegistry(defaultMachineID, listenAddr, mainPort, log)
	if err := reg.Start(specs); err != nil {
		reg.Close()
		return nil, err
	}
	return reg, nil
}

func startOneAuxListener(machineID, country, mode, listenAddr string, listenPort, mainPort int, log *logger.Logger) (net.Listener, error) {
	if country == "" {
		return nil, fmt.Errorf("aux listener on port %d has empty country", listenPort)
	}
	// Encode routing hints into Basic auth. The 3rd colon field is
	// optional and carries the mode when non-sticky. Two fields =
	// sticky (backward compatible).
	credsPayload := machineID + ":" + country
	if mode == "rotate" {
		credsPayload += ":rotate"
	}
	creds := base64.StdEncoding.EncodeToString([]byte(credsPayload))
	authHeaderValue := "Basic " + creds

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", listenAddr, listenPort))
	if err != nil {
		return nil, fmt.Errorf("aux listener bind on %s:%d: %w", listenAddr, listenPort, err)
	}

	log.Info("aux routing listener started",
		"addr", listenAddr,
		"port", listenPort,
		"machine_id", machineID,
		"country", country,
		"mode", func() string {
			if mode == "" {
				return "sticky"
			}
			return mode
		}(),
	)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !strings.Contains(err.Error(), "use of closed network connection") {
					log.Error("aux listener accept failed", "port", listenPort, "error", err)
				}
				return
			}
			go handleAuxConn(conn, mainPort, authHeaderValue, log)
		}
	}()
	return listener, nil
}

func handleAuxConn(client net.Conn, mainPort int, authHeaderValue string, log *logger.Logger) {
	defer client.Close()

	if err := client.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return
	}

	bufReader := bufio.NewReader(client)
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		log.Debug("aux listener: malformed request", "error", err)
		return
	}
	_ = client.SetReadDeadline(time.Time{})

	upstream, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", mainPort))
	if err != nil {
		log.Warn("aux listener: cannot dial upstream", "error", err)
		return
	}
	defer upstream.Close()

	// Inject the routing credentials. From here on the upstream sees the
	// request as if the client had sent the auth header itself.
	req.Header.Set("Proxy-Authorization", authHeaderValue)

	if err := req.Write(upstream); err != nil {
		log.Debug("aux listener: failed to write request to upstream", "error", err)
		return
	}

	// Forward bytes the client already buffered (TLS handshake right after
	// CONNECT, body fragments past the headers, etc.).
	if buffered := bufReader.Buffered(); buffered > 0 {
		b, _ := bufReader.Peek(buffered)
		if _, err := upstream.Write(b); err != nil {
			return
		}
		_, _ = bufReader.Discard(buffered)
	}

	// Bidirectional pipe until either side closes.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	wg.Wait()
}
