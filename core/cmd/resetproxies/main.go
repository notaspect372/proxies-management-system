// resetproxies — flip every proxy currently in `status: failed` back to
// `active`, resetting `failed_requests` to 0. Used to break the chicken-and-
// egg outage where all proxies are marked failed (3+ consecutive real request
// failures each) so checkout hands out no proxies, so no real requests can
// succeed, so nothing self-heals. After running this the scrapers get proxies
// again and successful requests keep them active.
//
//	DRY RUN : go run ./cmd/resetproxies
//	APPLY   : APPLY=1 go run ./cmd/resetproxies
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

	col := client.Database(dbName).Collection("proxies")
	total, _ := col.CountDocuments(ctx, bson.M{})
	failed, _ := col.CountDocuments(ctx, bson.M{"status": "failed"})
	fmt.Printf("proxies: %d total (%d failed)\n", total, failed)

	if failed == 0 {
		fmt.Println("nothing to reset — no proxies in status:failed.")
		return
	}

	if !apply {
		fmt.Printf("[DRY RUN] set APPLY=1 to flip %d failed → active and clear failed_requests.\n", failed)
		return
	}

	res, err := col.UpdateMany(
		ctx,
		bson.M{"status": "failed"},
		bson.M{
			"$set": bson.M{
				"status":          "active",
				"failed_requests": 0,
				"last_error":      nil,
				"updated_at":      time.Now(),
			},
		},
	)
	if err != nil {
		fmt.Printf("update: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[APPLIED] reset %d proxies to active — scrapers should start getting assignments again.\n", res.ModifiedCount)
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
