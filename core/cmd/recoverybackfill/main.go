// recoverybackfill — seeds recovery_events from the recovery_history arrays
// already sitting on proxy_domain_bans documents, so the per-site cooldown
// estimates start with whatever history the system happened to record before
// recovery_events existed.
//
// Each proxy_domain_bans doc carries up to 10 past recovery durations (seconds)
// for its (proxy, machine, domain) scope. Those are real observations — they
// just were never aggregated. This tool turns each one into a recovery_events
// row so the Recovery Estimates page is useful on day one instead of empty
// until fresh recoveries accumulate.
//
// Timestamps: recovery_history only kept the duration, not when it happened, so
// backfilled rows are stamped with a synthetic recovered_at derived from the
// document's last_success_at (falling back to now) and are marked
// backfilled:true. The durations — the thing the estimate is actually built
// from — are exact.
//
// Re-running is safe: rows written by a previous run are deleted first, so the
// tool converges rather than multiplying observations.
//
//	DRY RUN : go run ./cmd/recoverybackfill
//	APPLY   : APPLY=1 go run ./cmd/recoverybackfill
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	uri, dbName := mongoEnv()
	apply := os.Getenv("APPLY") == "1"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Println("mongo connect:", err)
		os.Exit(1)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	db := client.Database(dbName)

	cur, err := db.Collection("proxy_domain_bans").Find(ctx, bson.M{
		"recovery_history": bson.M{"$exists": true, "$ne": bson.A{}},
	})
	if err != nil {
		fmt.Println("find bans:", err)
		os.Exit(1)
	}
	defer cur.Close(ctx)

	type banDoc struct {
		ProxyID         int       `bson:"proxy_id"`
		MachineID       string    `bson:"machine_id"`
		TargetDomain    string    `bson:"target_domain"`
		TargetCountry   string    `bson:"target_country"`
		RecoveryHistory []int64   `bson:"recovery_history"`
		LastSuccessAt   time.Time `bson:"last_success_at"`
	}
	var docs []banDoc
	if err := cur.All(ctx, &docs); err != nil {
		fmt.Println("decode bans:", err)
		os.Exit(1)
	}

	now := time.Now()
	rows := make([]any, 0, 256)
	perDomain := map[string]int{}

	for _, d := range docs {
		if d.ProxyID <= 0 || d.MachineID == "" || d.TargetDomain == "" {
			continue
		}
		// Walk the history newest-last and space the synthetic timestamps out
		// so ordering by recovered_at stays stable and deterministic.
		anchor := d.LastSuccessAt
		if anchor.IsZero() {
			anchor = now
		}
		for i, sec := range d.RecoveryHistory {
			if sec <= 0 {
				continue
			}
			offset := time.Duration(len(d.RecoveryHistory)-1-i) * time.Hour
			recoveredAt := anchor.Add(-offset)
			rows = append(rows, bson.M{
				"proxy_id":       d.ProxyID,
				"machine_id":     d.MachineID,
				"target_domain":  d.TargetDomain,
				"target_country": d.TargetCountry,
				"banned_at":      recoveredAt.Add(-time.Duration(sec) * time.Second),
				"recovered_at":   recoveredAt,
				"recovery_sec":   sec,
				"trials":         0,
				"backfilled":     true,
			})
			perDomain[d.TargetDomain]++
		}
	}

	fmt.Printf("scanned %d ban docs with history\n", len(docs))
	fmt.Printf("would write %d recovery_events rows across %d sites\n\n", len(rows), len(perDomain))

	domains := make([]string, 0, len(perDomain))
	for k := range perDomain {
		domains = append(domains, k)
	}
	sort.Slice(domains, func(i, j int) bool {
		if perDomain[domains[i]] != perDomain[domains[j]] {
			return perDomain[domains[i]] > perDomain[domains[j]]
		}
		return domains[i] < domains[j]
	})
	for i, d := range domains {
		if i >= 25 {
			fmt.Printf("  ... and %d more sites\n", len(domains)-25)
			break
		}
		fmt.Printf("  %-50s %d observations\n", d, perDomain[d])
	}

	if len(rows) == 0 {
		fmt.Println("\nnothing to backfill — no recovery_history data recorded yet")
		return
	}

	if !apply {
		fmt.Println("\nDRY RUN — set APPLY=1 to write these rows")
		return
	}

	col := db.Collection("recovery_events")
	del, err := col.DeleteMany(ctx, bson.M{"backfilled": true})
	if err != nil {
		fmt.Println("clear previous backfill:", err)
		os.Exit(1)
	}
	res, err := col.InsertMany(ctx, rows)
	if err != nil {
		fmt.Println("insert:", err)
		os.Exit(1)
	}
	fmt.Printf("\nremoved %d rows from a previous backfill, inserted %d\n",
		del.DeletedCount, len(res.InsertedIDs))
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
	if uri == "" || db == "" {
		fmt.Println("MONGO_URI/MONGO_DB missing")
		os.Exit(2)
	}
	return uri, db
}
