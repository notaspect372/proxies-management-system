// unbanreachable — authoritative cleanup of artifact bans. For every banned
// scope it runs the NEW recovery logic (HTTP block-check, else raw CONNECT
// reachability on the scope's port) and unbans the ones that are actually
// reachable-and-not-blocked. Real blocks (403/429/markers) and genuinely
// unreachable proxies stay banned. Independent of the running core service.
//
//	DRY RUN : go run ./cmd/unbanreachable
//	APPLY   : APPLY=1 go run ./cmd/unbanreachable
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/proxy"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var antiBotMarkers = []string{
	"just a moment...", "cf-challenge", "__cf_chl", "checking your browser", "cf-mitigated",
	"datadome", "geo.captcha-delivery.com", "captcha-delivery.com",
	"_pxaction", "px-captcha", "perimeterx", "_abck", "akamai bot",
	"g-recaptcha", "h-captcha", "grecaptcha.render",
	"_incapsula_resource", "incapsula incident id",
	"access denied", "please verify you are human", "verify you are a human",
	"unusual traffic from your", "you have been blocked", "403 forbidden",
}

type scope struct {
	ID           any
	ProxyID      int
	Domain       string
	Port         string
	Machine      string
	Address      string
	Protocol     string
	Username     *string
	Password     *string
}

func main() {
	uri, dbName := mongoEnv()
	apply := os.Getenv("APPLY") == "1"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Printf("connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)
	db := client.Database(dbName)

	// Load banned scopes joined with proxy creds.
	cur, err := db.Collection("proxy_domain_bans").Find(ctx, bson.M{"state": "banned"})
	if err != nil {
		fmt.Printf("find bans: %v\n", err)
		os.Exit(1)
	}
	var bans []bson.M
	if err := cur.All(ctx, &bans); err != nil {
		fmt.Printf("decode bans: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("banned scopes: %d\n", len(bans))

	// Resolve proxy creds.
	proxByID := map[int]bson.M{}
	pcur, _ := db.Collection("proxies").Find(ctx, bson.M{})
	var prox []bson.M
	_ = pcur.All(ctx, &prox)
	for _, p := range prox {
		if id, ok := toInt(p["id"]); ok {
			proxByID[id] = p
		}
	}

	scopes := make([]scope, 0, len(bans))
	for _, b := range bans {
		pid, _ := toInt(b["proxy_id"])
		p, ok := proxByID[pid]
		if !ok {
			continue
		}
		s := scope{
			ID: b["_id"], ProxyID: pid,
			Domain:   asString(b["target_domain"]),
			Port:     asString(b["target_port"]),
			Machine:  asString(b["machine_id"]),
			Address:  asString(p["address"]),
			Protocol: asString(p["protocol"]),
		}
		if u := asStringPtr(p["username"]); u != nil {
			s.Username = u
		}
		if pw := asStringPtr(p["password"]); pw != nil {
			s.Password = pw
		}
		scopes = append(scopes, s)
	}

	// Probe in parallel.
	type result struct {
		s      scope
		pass   bool
		reason string
	}
	results := make([]result, len(scopes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	for i, s := range scopes {
		i, s := i, s
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			pass, reason := probe(s)
			results[i] = result{s, pass, reason}
		}()
	}
	wg.Wait()

	var pass, fail int
	col := db.Collection("proxy_domain_bans")
	now := time.Now()
	for _, r := range results {
		tag := "FAIL"
		if r.pass {
			tag = "PASS"
			pass++
		} else {
			fail++
		}
		fmt.Printf("  [%s] %s on %s  port=%s  (%s)\n", tag, r.s.Domain, r.s.Machine, portOr(r.s.Port), r.reason)
		if r.pass && apply {
			_, err := col.UpdateOne(ctx, bson.M{"_id": r.s.ID}, bson.M{"$set": bson.M{
				"state":                     "active",
				"failed_count":              0,
				"banned_at":                 nil,
				"next_probe_at":             nil,
				"probe_attempt":             0,
				"last_success_at":           now,
				"successful_since_recovery": 1,
			}})
			if err != nil {
				fmt.Printf("      unban write failed: %v\n", err)
			}
		}
	}

	fmt.Printf("\nreachable (would unban): %d   still banned (real): %d\n", pass, fail)
	if !apply {
		fmt.Println("[DRY RUN] no changes written. Set APPLY=1 to unban the reachable ones.")
	} else {
		fmt.Printf("[APPLIED] unbanned %d scopes.\n", pass)
	}
}

// probe mirrors probe.go runProbe: HTTP block-check, else raw reachability.
func probe(s scope) (bool, string) {
	mp := &models.Proxy{ID: s.ProxyID, Address: s.Address, Protocol: s.Protocol, Username: s.Username, Password: s.Password}
	if mp.Protocol == "" {
		mp.Protocol = "http"
	}
	// Step 1: HTTP block-check.
	if tr, err := proxy.CreateProxyTransport(mp); err == nil {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		c := &http.Client{Transport: tr, Timeout: 20 * time.Second}
		req, _ := http.NewRequest("GET", "https://"+s.Domain+"/", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		resp, err := c.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			if resp.StatusCode == 403 || resp.StatusCode == 429 {
				return false, fmt.Sprintf("blocked status %d", resp.StatusCode)
			}
			lower := bytes.ToLower(body)
			for _, m := range antiBotMarkers {
				if bytes.Contains(lower, []byte(m)) {
					return false, "marker:" + m
				}
			}
			return true, fmt.Sprintf("HTTP %d reachable", resp.StatusCode)
		}
	}
	// Step 2: raw reachability on the scope's port (or 443).
	port := s.Port
	if port == "" {
		port = "443"
	}
	if err := connectReach(mp, net.JoinHostPort(s.Domain, port), 15*time.Second); err == nil {
		return true, "reachable via CONNECT :" + port
	} else {
		return false, "unreachable: " + err.Error()
	}
}

func connectReach(p *models.Proxy, target string, timeout time.Duration) error {
	if p.Protocol != "http" && p.Protocol != "https" {
		return fmt.Errorf("reachability check only for http/https proxies (got %s)", p.Protocol)
	}
	conn, err := net.DialTimeout("tcp", p.Address, timeout)
	if err != nil {
		return fmt.Errorf("dial proxy: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if p.Username != nil && *p.Username != "" {
		pw := ""
		if p.Password != nil {
			pw = *p.Password
		}
		enc := base64.StdEncoding.EncodeToString([]byte(*p.Username + ":" + pw))
		req += "Proxy-Authorization: Basic " + enc + "\r\n"
	}
	req += "User-Agent: ProxyMonitor-Probe/1.0\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	statusLine := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	if !strings.Contains(statusLine, "200") {
		return fmt.Errorf("CONNECT failed: %s", statusLine)
	}
	return nil
}

func portOr(p string) string {
	if p == "" {
		return "443(default)"
	}
	return p
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asStringPtr(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

func mongoEnv() (string, string) {
	uri, db := os.Getenv("MONGO_URI"), os.Getenv("MONGO_DB")
	if uri != "" && db != "" {
		return uri, db
	}
	f, err := os.Open(".env")
	if err != nil {
		fmt.Println("MONGO_URI/MONGO_DB not set and ./.env missing")
		os.Exit(2)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
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
	return uri, db
}
