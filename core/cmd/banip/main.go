// banip — manually create a banned record for one proxy on a (machine, site)
// scope, for testing the recovery flow.
//
//	go run ./cmd/banip -proxy 130.180.236.47:6052 -machine mini_pc_ui -domain www.cian.ru -country Russia
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	proxyAddr := flag.String("proxy", "", "proxy address, e.g. 130.180.236.47:6052")
	machine := flag.String("machine", "mini_pc_ui", "machine id")
	domain := flag.String("domain", "www.cian.ru", "target site/domain")
	country := flag.String("country", "Russia", "target country")
	cooldownMin := flag.Int("cooldown", 1, "minutes until first recovery trial (next_probe_at)")
	flag.Parse()
	if *proxyAddr == "" {
		fmt.Println("error: -proxy required")
		os.Exit(2)
	}

	uri, dbName := mongoEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Printf("connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)
	db := client.Database(dbName)

	var p struct {
		ID int `bson:"id"`
	}
	if err := db.Collection("proxies").FindOne(ctx, bson.M{"address": *proxyAddr}).Decode(&p); err != nil {
		fmt.Printf("proxy %q not found: %v\n", *proxyAddr, err)
		os.Exit(1)
	}

	now := time.Now()
	next := now.Add(time.Duration(*cooldownMin) * time.Minute)
	filter := bson.M{"proxy_id": p.ID, "machine_id": *machine, "target_domain": *domain}
	update := bson.M{"$set": bson.M{
		"proxy_id":                  p.ID,
		"machine_id":                *machine,
		"target_domain":             *domain,
		"target_country":            *country,
		"state":                     "banned",
		"failed_count":              3,
		"banned_at":                 now,
		"next_probe_at":             next,
		"probe_attempt":             0,
		"successful_since_recovery": 0,
		"last_failure_at":           now,
	}}
	if _, err := db.Collection("proxy_domain_bans").UpdateOne(ctx, filter, update, options.Update().SetUpsert(true)); err != nil {
		fmt.Printf("ban write failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("BANNED: proxy_id=%d %s on %s for %s (%s). First recovery trial in ~%dm.\n",
		p.ID, *proxyAddr, *machine, *domain, *country, *cooldownMin)
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
