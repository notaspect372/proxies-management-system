package proxy

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// AuxListenerSpec describes one country→port aux listener.
type AuxListenerSpec struct {
	Country string
	Port    int
}

// StartAuxListeners spawns a credential-injection listener for each spec.
// Each listener:
//
//	• accepts incoming proxy clients on its port (no auth required)
//	• injects Proxy-Authorization: Basic base64(machineID:Country)
//	• forwards everything to the main proxy on mainPort
//
// Lets a scraper point at a single dedicated port per country
// (proxy="localhost:8011") without having to embed credentials in the proxy
// URL — handy when the client (e.g. Chrome under SB UC mode) won't send auth
// preemptively.
func StartAuxListeners(machineID string, mainPort int, specs []AuxListenerSpec, log *logger.Logger) error {
	if machineID == "" || len(specs) == 0 {
		return nil
	}
	if !models.IsValidMachineID(machineID) {
		return fmt.Errorf("aux listeners require a valid ROUTING_DEFAULT_MACHINE; got %q", machineID)
	}
	for _, s := range specs {
		if err := startOneAuxListener(machineID, s.Country, s.Port, mainPort, log); err != nil {
			return err
		}
	}
	return nil
}

func startOneAuxListener(machineID, country string, listenPort, mainPort int, log *logger.Logger) error {
	if country == "" {
		return fmt.Errorf("aux listener on port %d has empty country", listenPort)
	}
	creds := base64.StdEncoding.EncodeToString([]byte(machineID + ":" + country))
	authHeaderValue := "Basic " + creds

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil {
		return fmt.Errorf("aux listener bind on %d: %w", listenPort, err)
	}

	log.Info("aux routing listener started",
		"port", listenPort,
		"machine_id", machineID,
		"country", country,
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
	return nil
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
