package repository

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// BanScope is the (proxy, machine, target_country) triple a ban is recorded
// against. A proxy banned in scope X can still be selected for any other scope.
type BanScope struct {
	ProxyID       int
	MachineID     string
	TargetCountry string
}

// failureThreshold is the number of consecutive failures within a scope before
// the proxy is banned for that scope.
const failureThreshold = 3

// defaultBanDuration is how long a proxy stays banned in a scope before
// becoming eligible again. Overridable via PROXY_BAN_DURATION (e.g. "30m",
// "1h", "2h30m").
const defaultBanDuration = time.Hour

// BanRepository owns the proxy_country_bans collection: it records per-scope
// success/failure streaks and the resulting bans.
type BanRepository struct {
	db           *database.DB
	banDuration  time.Duration
}

func NewBanRepository(db *database.DB) *BanRepository {
	dur := defaultBanDuration
	if raw := os.Getenv("PROXY_BAN_DURATION"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			dur = parsed
		} else if mins, err := strconv.Atoi(raw); err == nil && mins > 0 {
			// Allow plain integer = minutes for convenience.
			dur = time.Duration(mins) * time.Minute
		}
	}
	return &BanRepository{db: db, banDuration: dur}
}

// BanDuration returns the configured cool-down. Exposed for logging/debugging.
func (r *BanRepository) BanDuration() time.Duration { return r.banDuration }

// EnsureMongoIndexes creates the unique key for ban records.
func (r *BanRepository) EnsureMongoIndexes(ctx context.Context) error {
	if !r.db.IsMongo() {
		return nil
	}
	_, err := r.db.MongoDB().Collection("proxy_country_bans").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "proxy_id", Value: 1},
				{Key: "machine_id", Value: 1},
				{Key: "target_country", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "machine_id", Value: 1}, {Key: "target_country", Value: 1}}},
	})
	return err
}

// RecordSuccess clears the failure streak and any active ban for this scope.
func (r *BanRepository) RecordSuccess(ctx context.Context, scope BanScope) error {
	if !scope.valid() {
		return nil
	}
	now := time.Now()
	if r.db.IsMongo() {
		_, err := r.db.MongoDB().Collection("proxy_country_bans").UpdateOne(
			ctx,
			scope.mongoFilter(),
			bson.M{
				"$set": bson.M{
					"failed_count":    0,
					"banned_until":    nil,
					"last_success_at": now,
				},
				"$setOnInsert": scope.mongoSetOnInsert(),
			},
			options.Update().SetUpsert(true),
		)
		return err
	}
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO proxy_country_bans (proxy_id, machine_id, target_country, failed_count, banned_until, last_success_at)
		VALUES ($1, $2, $3, 0, NULL, $4)
		ON CONFLICT (proxy_id, machine_id, target_country) DO UPDATE SET
			failed_count    = 0,
			banned_until    = NULL,
			last_success_at = EXCLUDED.last_success_at
	`, scope.ProxyID, scope.MachineID, scope.TargetCountry, now)
	return err
}

// RecordFailure increments the per-scope failure streak. When it crosses the
// threshold the scope is banned for `banDuration` from now.
func (r *BanRepository) RecordFailure(ctx context.Context, scope BanScope) error {
	if !scope.valid() {
		return nil
	}
	now := time.Now()
	bannedUntil := now.Add(r.banDuration)

	if r.db.IsMongo() {
		// Increment first, then re-read to decide whether to set banned_until.
		// Two ops is fine — same document, low contention.
		col := r.db.MongoDB().Collection("proxy_country_bans")
		res := col.FindOneAndUpdate(
			ctx,
			scope.mongoFilter(),
			bson.M{
				"$inc": bson.M{"failed_count": 1},
				"$set": bson.M{"last_failure_at": now},
				"$setOnInsert": scope.mongoSetOnInsert(),
			},
			options.FindOneAndUpdate().
				SetUpsert(true).
				SetReturnDocument(options.After),
		)
		if err := res.Err(); err != nil {
			return err
		}
		var doc struct {
			FailedCount int `bson:"failed_count"`
		}
		if err := res.Decode(&doc); err != nil {
			return err
		}
		if doc.FailedCount >= failureThreshold {
			_, err := col.UpdateOne(ctx, scope.mongoFilter(), bson.M{
				"$set": bson.M{"banned_until": bannedUntil},
			})
			return err
		}
		return nil
	}

	// Postgres: do the increment + conditional ban in one upsert.
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO proxy_country_bans (proxy_id, machine_id, target_country, failed_count, banned_until, last_failure_at)
		VALUES ($1, $2, $3, 1, NULL, $4)
		ON CONFLICT (proxy_id, machine_id, target_country) DO UPDATE SET
			failed_count    = proxy_country_bans.failed_count + 1,
			last_failure_at = EXCLUDED.last_failure_at,
			banned_until    = CASE
				WHEN proxy_country_bans.failed_count + 1 >= $5 THEN $6
				ELSE proxy_country_bans.banned_until
			END
	`, scope.ProxyID, scope.MachineID, scope.TargetCountry, now, failureThreshold, bannedUntil)
	return err
}

// IsBanned reports whether the scope is currently banned (banned_until in the
// future).
func (r *BanRepository) IsBanned(ctx context.Context, scope BanScope) (bool, error) {
	if !scope.valid() {
		return false, nil
	}
	now := time.Now()
	if r.db.IsMongo() {
		err := r.db.MongoDB().Collection("proxy_country_bans").
			FindOne(ctx, bson.M{
				"proxy_id":       scope.ProxyID,
				"machine_id":     scope.MachineID,
				"target_country": scope.TargetCountry,
				"banned_until":   bson.M{"$gt": now},
			}).Err()
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
	var one int
	err := r.db.Pool.QueryRow(ctx, `
		SELECT 1 FROM proxy_country_bans
		WHERE proxy_id = $1 AND machine_id = $2 AND target_country = $3 AND banned_until > $4
		LIMIT 1
	`, scope.ProxyID, scope.MachineID, scope.TargetCountry, now).Scan(&one)
	if err != nil {
		// pgx returns ErrNoRows when no match; treat as not-banned.
		return false, nil
	}
	return true, nil
}

// BannedProxyIDs returns the set of proxy IDs currently banned for the given
// (machine, country). Used by Checkout to filter the eligible pool.
func (r *BanRepository) BannedProxyIDs(ctx context.Context, machineID, targetCountry string) (map[int]bool, error) {
	out := map[int]bool{}
	if machineID == "" || targetCountry == "" {
		return out, nil
	}
	now := time.Now()
	if r.db.IsMongo() {
		cur, err := r.db.MongoDB().Collection("proxy_country_bans").Find(ctx, bson.M{
			"machine_id":     machineID,
			"target_country": targetCountry,
			"banned_until":   bson.M{"$gt": now},
		})
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)
		for cur.Next(ctx) {
			var d struct {
				ProxyID int `bson:"proxy_id"`
			}
			if err := cur.Decode(&d); err != nil {
				return nil, err
			}
			out[d.ProxyID] = true
		}
		return out, nil
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT proxy_id FROM proxy_country_bans
		WHERE machine_id = $1 AND target_country = $2 AND banned_until > $3
	`, machineID, targetCountry, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		out[pid] = true
	}
	return out, nil
}

// scope helpers

func (s BanScope) valid() bool {
	return s.ProxyID > 0 && s.MachineID != "" && s.TargetCountry != ""
}

func (s BanScope) mongoFilter() bson.M {
	return bson.M{
		"proxy_id":       s.ProxyID,
		"machine_id":     s.MachineID,
		"target_country": s.TargetCountry,
	}
}

func (s BanScope) mongoSetOnInsert() bson.M {
	return bson.M{
		"proxy_id":       s.ProxyID,
		"machine_id":     s.MachineID,
		"target_country": s.TargetCountry,
	}
}

// String renders the scope for log lines.
func (s BanScope) String() string {
	return fmt.Sprintf("proxy=%d machine=%s country=%s", s.ProxyID, s.MachineID, s.TargetCountry)
}
