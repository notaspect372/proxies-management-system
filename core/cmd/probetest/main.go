// probetest — manually run the SAME recovery probe the system uses, against a
// given proxy + site, and print the real outcome plus the current ban state.
//
// Usage (run from the core/ directory):
//
//	go run ./cmd/probetest -proxy 9.142.8.162:5819 -site www.cian.ru
//	go run ./cmd/probetest -proxy 9.142.8.162:5819            # list all ban scopes for this proxy
//	go run ./cmd/probetest -proxy 9.142.8.162:5819 -site www.cian.ru -show   # also dump response body
//
// MONGO_URI / MONGO_DB are read from the environment, falling back to ./.env.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/proxy"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Mirrors probe.go's antiBotMarkers — keep in sync if that list changes.
var antiBotMarkers = []string{
	"just a moment...", "cf-challenge", "__cf_chl", "checking your browser", "cf-mitigated",
	"datadome", "geo.captcha-delivery.com", "captcha-delivery.com",
	"_pxaction", "px-captcha", "perimeterx", "_abck", "akamai bot",
	"g-recaptcha", "h-captcha", "grecaptcha.render",
	"_incapsula_resource", "incapsula incident id",
	"access denied", "please verify you are human", "verify you are a human",
	"unusual traffic from your", "you have been blocked", "403 forbidden",
}

func main() {
	proxyAddr := flag.String("proxy", "", "proxy address as shown on the dashboard, e.g. 9.142.8.162:5819")
	site := flag.String("site", "", "target site/domain to probe, e.g. www.cian.ru (omit to just list ban scopes)")
	show := flag.Bool("show", false, "print the first 1KB of the response body")
	port := flag.String("port", "", "port for the raw reachability check (e.g. 5228 for mtalk); defaults to the DB target_port or 443")
	flag.Parse()

	if *proxyAddr == "" {
		fmt.Println("error: -proxy is required (e.g. -proxy 9.142.8.162:5819)")
		os.Exit(2)
	}

	uri, dbName := mongoEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Printf("mongo connect failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)
	db := client.Database(dbName)

	// Look up the proxy by address.
	var p struct {
		ID       int     `bson:"id"`
		Address  string  `bson:"address"`
		Protocol string  `bson:"protocol"`
		Username *string `bson:"username"`
		Password *string `bson:"password"`
	}
	if err := db.Collection("proxies").FindOne(ctx, bson.M{"address": *proxyAddr}).Decode(&p); err != nil {
		fmt.Printf("proxy %q not found in DB: %v\n", *proxyAddr, err)
		os.Exit(1)
	}
	if p.Protocol == "" {
		p.Protocol = "http"
	}
	fmt.Printf("proxy_id=%d  address=%s  protocol=%s\n", p.ID, p.Address, p.Protocol)

	// Show current ban records for this proxy (optionally filtered by site).
	banFilter := bson.M{"proxy_id": p.ID}
	if *site != "" {
		banFilter["target_domain"] = *site
	}
	dbPort := ""
	cur, err := db.Collection("proxy_domain_bans").Find(ctx, banFilter)
	if err == nil {
		var rows []bson.M
		_ = cur.All(ctx, &rows)
		fmt.Printf("\nDB ban records for this proxy%s: %d\n", siteSuffix(*site), len(rows))
		for _, r := range rows {
			fmt.Printf("  [%v] %v on %v  attempt=%v next_probe=%v successes=%v target_port=%v\n",
				r["state"], r["target_domain"], r["machine_id"],
				r["probe_attempt"], r["next_probe_at"], r["successful_since_recovery"], r["target_port"])
			if tp, ok := r["target_port"].(string); ok && tp != "" {
				dbPort = tp
			}
		}
	}

	if *site == "" {
		fmt.Println("\n(no -site given, so no live probe was run)")
		return
	}

	// Run the live probe — same as the worker.
	fmt.Printf("\n--- live probe: GET https://%s/ through %s ---\n", *site, p.Address)
	mp := &models.Proxy{ID: p.ID, Address: p.Address, Protocol: p.Protocol, Username: p.Username, Password: p.Password}
	tr, err := proxy.CreateProxyTransport(mp)
	if err != nil {
		fmt.Printf("transport error: %v\n", err)
		os.Exit(1)
	}
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client2 := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	status, ct, body, lat, perr := doGet(ctx, client2, "https://"+*site+"/")

	// Step 1: did we get a real HTTP response? Then PASS unless it's a block.
	if perr == "" {
		reason, passed := classify(status, body)
		fmt.Printf("HTTP %d   content-type=%q   body=%d bytes   latency=%dms\n", status, ct, len(body), lat)
		if *show && len(body) > 0 {
			n := len(body)
			if n > 1024 {
				n = 1024
			}
			fmt.Printf("\n----- body (first %dB) -----\n%s\n---------------------------\n", n, string(body[:n]))
		}
		if passed {
			fmt.Println("\nVERDICT: PASS ✅  — proxy reached the site and isn't blocked. System WOULD UNBAN.")
		} else {
			fmt.Printf("\nVERDICT: FAIL ❌  (reason: %s) — explicit block signal. Stays BANNED.\n", reason)
		}
		return
	}

	// Step 2: no HTTP response (transport error or non-HTTP host like mtalk).
	// Fall back to a raw reachability check on the right port: -port flag wins,
	// else the DB target_port, else 443.
	reachPort := "443"
	if dbPort != "" {
		reachPort = dbPort
	}
	if *port != "" {
		reachPort = *port
	}
	fmt.Printf("no HTTP response (%s)\n", perr)
	fmt.Printf("falling back to raw reachability check (CONNECT to :%s)...\n", reachPort)
	if reachErr := connectReach(p.Protocol, p.Address, p.Username, p.Password, *site+":"+reachPort, 15*time.Second); reachErr == nil {
		fmt.Println("\nVERDICT: PASS ✅  — proxy CAN tunnel to the host (binary/non-HTTP service). System WOULD UNBAN.")
		fmt.Println("NOTE: site doesn't speak plain HTTP (e.g. mtalk), but the proxy reaches it — so it's NOT a real ban.")
	} else {
		fmt.Printf("\nVERDICT: FAIL ❌  — proxy could not reach the host at all (%s). Genuinely unreachable.\n", reachErr)
	}
}

// connectReach opens (and closes) a CONNECT tunnel to target through the proxy.
// nil error = the proxy can reach the host at TCP level.
func connectReach(proto, addr string, user, pass *string, target string, timeout time.Duration) error {
	if proto == "" {
		proto = "http"
	}
	if proto != "http" && proto != "https" {
		// For SOCKS, a plain HTTP GET error is inconclusive here; report it.
		return fmt.Errorf("reachability check only implemented for http/https proxies (got %s)", proto)
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("dial proxy: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if user != nil && *user != "" {
		pw := ""
		if pass != nil {
			pw = *pass
		}
		enc := base64.StdEncoding.EncodeToString([]byte(*user + ":" + pw))
		req += "Proxy-Authorization: Basic " + enc + "\r\n"
	}
	req += "User-Agent: ProxyMonitor-Probe/1.0\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("write CONNECT: %w", err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read CONNECT response: %w", err)
	}
	statusLine := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	if !strings.Contains(statusLine, "200") {
		return fmt.Errorf("CONNECT failed: %s", statusLine)
	}
	return nil
}

func doGet(ctx context.Context, c *http.Client, url string) (int, string, []byte, int64, string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	start := time.Now()
	resp, err := c.Do(req)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		return 0, "", nil, lat, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, lat, ""
}

// classify mirrors probe.go classifyResponse (redirect-path check omitted —
// we don't track the final URL here, which is fine for a manual spot-check).
func classify(status int, body []byte) (string, bool) {
	// Only explicit block signals fail. Reachable (any other response,
	// including 404/5xx/small/JSON) = PASS — matches probe.go.
	if status == 403 || status == 429 {
		return "blocked status", false
	}
	lower := bytes.ToLower(body)
	for _, m := range antiBotMarkers {
		if bytes.Contains(lower, []byte(m)) {
			return "marker:" + m, false
		}
	}
	return "", true
}

func siteSuffix(site string) string {
	if site == "" {
		return ""
	}
	return " (site=" + site + ")"
}

func mongoEnv() (string, string) {
	uri, db := os.Getenv("MONGO_URI"), os.Getenv("MONGO_DB")
	if uri != "" && db != "" {
		return uri, db
	}
	// Fall back to ./.env
	f, err := os.Open(".env")
	if err != nil {
		fmt.Println("error: MONGO_URI/MONGO_DB not set and ./.env not found")
		os.Exit(2)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, "="); ok {
			switch strings.TrimSpace(k) {
			case "MONGO_URI":
				if uri == "" {
					uri = strings.TrimSpace(v)
				}
			case "MONGO_DB":
				if db == "" {
					db = strings.TrimSpace(v)
				}
			}
		}
	}
	if uri == "" || db == "" {
		fmt.Println("error: MONGO_URI/MONGO_DB missing from env and ./.env")
		os.Exit(2)
	}
	return uri, db
}
