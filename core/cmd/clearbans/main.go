// clearbans — wipe the proxy_domain_bans collection entirely. Every proxy
// becomes unbanned for every scope (clean slate). New bans form again from
// live traffic.
//
//	DRY RUN : go run ./cmd/clearbans
//	APPLY   : APPLY=1 go run ./cmd/clearbans
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
	total, _ := col.CountDocuments(ctx, bson.M{})
	banned, _ := col.CountDocuments(ctx, bson.M{"state": "banned"})
	fmt.Printf("ban records: %d total (%d banned)\n", total, banned)

	if !apply {
		fmt.Println("[DRY RUN] set APPLY=1 to DELETE all ban records (full clear).")
		return
	}
	res, err := col.DeleteMany(ctx, bson.M{})
	if err != nil {
		fmt.Printf("delete: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[APPLIED] deleted %d records — all proxies are now unbanned everywhere.\n", res.DeletedCount)
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
