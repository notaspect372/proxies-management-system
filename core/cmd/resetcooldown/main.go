// resetcooldown — set next_probe_at to now (and probe_attempt to 0) for every
// banned scope, so the probe worker re-tests them on its next tick. Run AFTER
// the core service is restarted with the new timing, otherwise the old process
// will reschedule them with the old backoff.
//
//	APPLY=1 go run ./cmd/resetcooldown
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	uri, dbName := mongoEnv()
	apply := os.Getenv("APPLY") == "1"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Printf("connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)

	col := client.Database(dbName).Collection("proxy_domain_bans")
	filter := bson.M{"state": "banned"}
	total, _ := col.CountDocuments(ctx, filter)
	fmt.Printf("banned scopes: %d\n", total)

	if !apply {
		fmt.Println("[DRY RUN] set APPLY=1 to reset next_probe_at to now.")
		return
	}
	res, err := col.UpdateMany(ctx, filter, bson.M{
		"$set": bson.M{"next_probe_at": time.Unix(0, 0), "probe_attempt": 0},
	})
	if err != nil {
		fmt.Printf("update: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[APPLIED] modified=%d — probes will fire within ~60s.\n", res.ModifiedCount)
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
