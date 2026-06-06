package repository

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// BanScope is the (proxy, machine, domain) triple a ban is recorded against.
// A proxy banned in scope X can still be selected for any other scope —
// including the same proxy on the same machine for a different site.
type BanScope struct {
	ProxyID       int
	MachineID     string
	TargetDomain  string
	TargetCountry string // carried for dashboard rollup; not part of identity
}

// State machine values stored in proxy_domain_bans.state.
const (
	StateActive = "active"
	StateBanned = "banned"
)

// Display sub-states (derived, not stored). The dashboard wants to distinguish
// "we haven't tried probing yet" from "we've probed N times and it's still
// failing", which is the only difference between Cooldown and Recovery Test.
const (
	DisplayActive       = "active"
	DisplayCooldown     = "cooldown"
	DisplayRecoveryTest = "recovery_test"
)

// failureThreshold is the number of consecutive failures within a scope
// before the proxy is banned for that scope.
const failureThreshold = 3

// initialCooldown is the wait before the first probe after a fresh ban.
const initialCooldown = 30 * time.Minute

// probeBackoffBase doubles per failed probe: 30m, 60m, 120m, 240m, …
const probeBackoffBase = 30 * time.Minute

// maxCooldown caps backoff so probes still happen at least daily.
const maxCooldown = 24 * time.Hour

// historyCap is how many recovery durations we retain per scope for future
// adaptive learning. Not consumed yet, but written so the data is there.
const historyCap = 10

// BanRepository owns the proxy_domain_bans collection: per-scope state
// machine for the Active → Banned → Cooldown → Recovery Test → Active
// lifecycle.
type BanRepository struct {
	db *database.DB
}

func NewBanRepository(db *database.DB) *BanRepository {
	return &BanRepository{db: db}
}

// Tunable: override via PROXY_INITIAL_COOLDOWN env var ("15m", "30m", etc).
// Returns the user-configured value or the default if unset/invalid.
func (r *BanRepository) initialCooldownFor(_ BanScope) time.Duration {
	if raw := os.Getenv("PROXY_INITIAL_COOLDOWN"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		if mins, err := strconv.Atoi(raw); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return initialCooldown
}

// probeBackoff returns the wait until the next probe given how many probes
// have already failed for this scope.
func probeBackoff(failedProbes int) time.Duration {
	if failedProbes < 0 {
		failedProbes = 0
	}
	d := time.Duration(math.Pow(2, float64(failedProbes))) * probeBackoffBase
	if d > maxCooldown {
		d = maxCooldown
	}
	return d
}

// EnsureMongoIndexes creates the unique key for ban records.
func (r *BanRepository) EnsureMongoIndexes(ctx context.Context) error {
	if !r.db.IsMongo() {
		return nil
	}
	_, err := r.db.MongoDB().Collection("proxy_domain_bans").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "proxy_id", Value: 1},
				{Key: "machine_id", Value: 1},
				{Key: "target_domain", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "machine_id", Value: 1}, {Key: "target_domain", Value: 1}}},
		{Keys: bson.D{{Key: "state", Value: 1}, {Key: "next_probe_at", Value: 1}}},
	})
	return err
}

// RecordSuccess updates the scope on a successful request: zeros the failure
// streak, clears any ban, increments the post-recovery success counter, and
// (if we're transitioning out of a ban) records the recovery duration into
// the rolling history for future adaptive learning.
func (r *BanRepository) RecordSuccess(ctx context.Context, scope BanScope) error {
	if !scope.valid() {
		return nil
	}
	now := time.Now()

	if r.db.IsMongo() {
		col := r.db.MongoDB().Collection("proxy_domain_bans")
		// Re-read the doc so we can compute the recovery duration if we're
		// transitioning out of a ban. UpdateOne would lose that context.
		var existing struct {
			State    string     `bson:"state"`
			BannedAt *time.Time `bson:"banned_at"`
		}
		err := col.FindOne(ctx, scope.mongoFilter()).Decode(&existing)
		if err != nil && err != mongo.ErrNoDocuments {
			return err
		}

		update := bson.M{
			"$set": bson.M{
				"state":           StateActive,
				"failed_count":    0,
				"banned_at":       nil,
				"next_probe_at":   nil,
				"probe_attempt":   0,
				"last_success_at": now,
				"target_country":  scope.TargetCountry,
			},
			"$inc":         bson.M{"successful_since_recovery": 1},
			"$setOnInsert": scope.mongoSetOnInsert(),
		}

		// On a clean recovery (was banned, now succeeding) push the duration
		// to history. Capped slice — bounded growth per scope.
		if existing.State == StateBanned && existing.BannedAt != nil {
			recoverySec := int64(now.Sub(*existing.BannedAt).Seconds())
			update["$push"] = bson.M{
				"recovery_history": bson.M{
					"$each":  bson.A{recoverySec},
					"$slice": -historyCap,
				},
			}
			// Reset post-recovery counter to 1 (this success) rather than
			// incrementing the stale pre-ban value.
			set := update["$set"].(bson.M)
			set["successful_since_recovery"] = 1
			delete(update, "$inc")
		}

		_, err = col.UpdateOne(ctx, scope.mongoFilter(), update, options.Update().SetUpsert(true))
		return err
	}

	// Postgres path
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO proxy_domain_bans (
			proxy_id, machine_id, target_domain, target_country,
			state, failed_count, banned_at, next_probe_at, probe_attempt,
			last_success_at, successful_since_recovery
		) VALUES ($1, $2, $3, $4, 'active', 0, NULL, NULL, 0, $5, 1)
		ON CONFLICT (proxy_id, machine_id, target_domain) DO UPDATE SET
			state                     = 'active',
			failed_count              = 0,
			banned_at                 = NULL,
			next_probe_at             = NULL,
			probe_attempt             = 0,
			last_success_at           = EXCLUDED.last_success_at,
			target_country            = COALESCE(EXCLUDED.target_country, proxy_domain_bans.target_country),
			successful_since_recovery = CASE
				WHEN proxy_domain_bans.state = 'banned' THEN 1
				ELSE proxy_domain_bans.successful_since_recovery + 1
			END
	`, scope.ProxyID, scope.MachineID, scope.TargetDomain, nullableStr(scope.TargetCountry), now)
	return err
}

// RecordFailure increments the per-scope failure streak. When it crosses
// the threshold the scope flips to banned with an initial cooldown timer.
// Repeat failures while already banned reset the cooldown clock but do not
// change the probe attempt counter (that's owned by RecordProbeResult).
func (r *BanRepository) RecordFailure(ctx context.Context, scope BanScope) error {
	if !scope.valid() {
		return nil
	}
	now := time.Now()

	if r.db.IsMongo() {
		col := r.db.MongoDB().Collection("proxy_domain_bans")
		// First: bump the failure counter atomically.
		res := col.FindOneAndUpdate(
			ctx,
			scope.mongoFilter(),
			bson.M{
				"$inc": bson.M{"failed_count": 1},
				"$set": bson.M{
					"last_failure_at": now,
					"target_country":  scope.TargetCountry,
				},
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
			FailedCount int    `bson:"failed_count"`
			State       string `bson:"state"`
		}
		if err := res.Decode(&doc); err != nil {
			return err
		}

		// If we just crossed the threshold OR we were already banned but
		// the counter implies more breakage, refresh the cooldown timer.
		if doc.FailedCount >= failureThreshold {
			cooldown := r.initialCooldownFor(scope)
			_, err := col.UpdateOne(ctx, scope.mongoFilter(), bson.M{
				"$set": bson.M{
					"state":                     StateBanned,
					"banned_at":                 now,
					"next_probe_at":             now.Add(cooldown),
					"probe_attempt":             0,
					"successful_since_recovery": 0,
				},
			})
			return err
		}
		return nil
	}

	// Postgres path: do it in one upsert.
	cooldown := r.initialCooldownFor(scope)
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO proxy_domain_bans (
			proxy_id, machine_id, target_domain, target_country,
			state, failed_count, last_failure_at
		) VALUES ($1, $2, $3, $4, 'active', 1, $5)
		ON CONFLICT (proxy_id, machine_id, target_domain) DO UPDATE SET
			failed_count    = proxy_domain_bans.failed_count + 1,
			last_failure_at = EXCLUDED.last_failure_at,
			target_country  = COALESCE(EXCLUDED.target_country, proxy_domain_bans.target_country),
			state           = CASE
				WHEN proxy_domain_bans.failed_count + 1 >= $6 THEN 'banned'
				ELSE proxy_domain_bans.state
			END,
			banned_at       = CASE
				WHEN proxy_domain_bans.failed_count + 1 >= $6 AND proxy_domain_bans.state != 'banned' THEN $5
				ELSE proxy_domain_bans.banned_at
			END,
			next_probe_at   = CASE
				WHEN proxy_domain_bans.failed_count + 1 >= $6 AND proxy_domain_bans.state != 'banned' THEN $5 + $7::interval
				ELSE proxy_domain_bans.next_probe_at
			END,
			probe_attempt   = CASE
				WHEN proxy_domain_bans.failed_count + 1 >= $6 AND proxy_domain_bans.state != 'banned' THEN 0
				ELSE proxy_domain_bans.probe_attempt
			END,
			successful_since_recovery = CASE
				WHEN proxy_domain_bans.failed_count + 1 >= $6 AND proxy_domain_bans.state != 'banned' THEN 0
				ELSE proxy_domain_bans.successful_since_recovery
			END
	`, scope.ProxyID, scope.MachineID, scope.TargetDomain, nullableStr(scope.TargetCountry),
		now, failureThreshold, fmt.Sprintf("%d seconds", int(cooldown.Seconds())))
	return err
}

// RecordProbeResult is called by the background probe worker after sending a
// trial request to a banned scope. If the probe passed we clear the ban
// (same as RecordSuccess). If it failed we leave state=banned, bump
// probe_attempt, and schedule the next probe with exponential backoff.
func (r *BanRepository) RecordProbeResult(ctx context.Context, scope BanScope, passed bool) error {
	if !scope.valid() {
		return nil
	}
	if passed {
		return r.RecordSuccess(ctx, scope)
	}

	now := time.Now()
	if r.db.IsMongo() {
		col := r.db.MongoDB().Collection("proxy_domain_bans")
		var doc struct {
			ProbeAttempt int `bson:"probe_attempt"`
		}
		if err := col.FindOne(ctx, scope.mongoFilter()).Decode(&doc); err != nil {
			return err
		}
		nextAttempt := doc.ProbeAttempt + 1
		next := now.Add(probeBackoff(nextAttempt))
		_, err := col.UpdateOne(ctx, scope.mongoFilter(), bson.M{
			"$set": bson.M{
				"probe_attempt":   nextAttempt,
				"last_probe_at":   now,
				"last_failure_at": now,
				"next_probe_at":   next,
			},
		})
		return err
	}

	var current int
	if err := r.db.Pool.QueryRow(ctx, `
		SELECT probe_attempt FROM proxy_domain_bans
		WHERE proxy_id = $1 AND machine_id = $2 AND target_domain = $3
	`, scope.ProxyID, scope.MachineID, scope.TargetDomain).Scan(&current); err != nil {
		return err
	}
	nextAttempt := current + 1
	next := now.Add(probeBackoff(nextAttempt))
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE proxy_domain_bans
		SET probe_attempt   = $4,
		    last_probe_at   = $5,
		    last_failure_at = $5,
		    next_probe_at   = $6
		WHERE proxy_id = $1 AND machine_id = $2 AND target_domain = $3
	`, scope.ProxyID, scope.MachineID, scope.TargetDomain, nextAttempt, now, next)
	return err
}

// IsBanned reports whether this exact (proxy, machine, domain) is in the
// banned state right now. Cheap point lookup for sticky-binding checks.
func (r *BanRepository) IsBanned(ctx context.Context, scope BanScope) (bool, error) {
	if !scope.valid() {
		return false, nil
	}
	if r.db.IsMongo() {
		err := r.db.MongoDB().Collection("proxy_domain_bans").
			FindOne(ctx, bson.M{
				"proxy_id":      scope.ProxyID,
				"machine_id":    scope.MachineID,
				"target_domain": scope.TargetDomain,
				"state":         StateBanned,
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
		SELECT 1 FROM proxy_domain_bans
		WHERE proxy_id = $1 AND machine_id = $2 AND target_domain = $3 AND state = 'banned'
		LIMIT 1
	`, scope.ProxyID, scope.MachineID, scope.TargetDomain).Scan(&one)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// BannedProxyIDsForDomain returns the set of proxy IDs currently banned for
// the given (machine, domain). Used by Checkout to filter the eligible pool.
func (r *BanRepository) BannedProxyIDsForDomain(ctx context.Context, machineID, domain string) (map[int]bool, error) {
	out := map[int]bool{}
	if machineID == "" || domain == "" {
		return out, nil
	}
	if r.db.IsMongo() {
		cur, err := r.db.MongoDB().Collection("proxy_domain_bans").Find(ctx, bson.M{
			"machine_id":    machineID,
			"target_domain": domain,
			"state":         StateBanned,
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
		SELECT proxy_id FROM proxy_domain_bans
		WHERE machine_id = $1 AND target_domain = $2 AND state = 'banned'
	`, machineID, domain)
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

// AllBannedProxiesByMachineCountry returns every active ban in one
// round-trip, keyed by [machine_id][country] → set of banned proxy IDs.
// Replaces N sequential BannedProxiesForCountry calls in the
// Infrastructure handler, which on Atlas free tier were summing to 60s+
// and timing out the dashboard.
func (r *BanRepository) AllBannedProxiesByMachineCountry(ctx context.Context) (map[string]map[string]map[int]bool, error) {
	out := map[string]map[string]map[int]bool{}
	add := func(machineID, country string, pid int) {
		if machineID == "" || country == "" {
			return
		}
		if _, ok := out[machineID]; !ok {
			out[machineID] = map[string]map[int]bool{}
		}
		if _, ok := out[machineID][country]; !ok {
			out[machineID][country] = map[int]bool{}
		}
		out[machineID][country][pid] = true
	}
	if r.db.IsMongo() {
		cur, err := r.db.MongoDB().Collection("proxy_domain_bans").Find(ctx, bson.M{
			"state": StateBanned,
		})
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)
		for cur.Next(ctx) {
			var d struct {
				MachineID     string `bson:"machine_id"`
				TargetCountry string `bson:"target_country"`
				ProxyID       int    `bson:"proxy_id"`
			}
			if err := cur.Decode(&d); err != nil {
				return nil, err
			}
			add(d.MachineID, d.TargetCountry, d.ProxyID)
		}
		return out, nil
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT machine_id, target_country, proxy_id
		FROM proxy_domain_bans
		WHERE state = 'banned' AND target_country <> ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var machineID, country string
		var pid int
		if err := rows.Scan(&machineID, &country, &pid); err != nil {
			return nil, err
		}
		add(machineID, country, pid)
	}
	return out, nil
}

// BannedProxiesForCountry returns the set of proxies that are banned in at
// least one domain belonging to the given (machine, country). Used by the
// dashboard country-rollup count.
func (r *BanRepository) BannedProxiesForCountry(ctx context.Context, machineID, country string) (map[int]bool, error) {
	out := map[int]bool{}
	if machineID == "" || country == "" {
		return out, nil
	}
	if r.db.IsMongo() {
		cur, err := r.db.MongoDB().Collection("proxy_domain_bans").Find(ctx, bson.M{
			"machine_id":     machineID,
			"target_country": country,
			"state":          StateBanned,
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
		SELECT DISTINCT proxy_id FROM proxy_domain_bans
		WHERE machine_id = $1 AND target_country = $2 AND state = 'banned'
	`, machineID, country)
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

// CooldownRow is what the /api/v1/cooldowns endpoint surfaces per row.
type CooldownRow struct {
	ProxyID                 int        `json:"proxy_id"`
	ProxyAddress            string     `json:"proxy_address"`
	TargetDomain            string     `json:"target_domain"`
	TargetCountry           string     `json:"target_country,omitempty"`
	MachineID               string     `json:"machine_id"`
	State                   string     `json:"state"`
	DisplayState            string     `json:"display_state"`
	BannedAt                *time.Time `json:"banned_at,omitempty"`
	NextProbeAt             *time.Time `json:"next_probe_at,omitempty"`
	CooldownRemainingSec    int64      `json:"cooldown_remaining_sec"`
	ProbeAttempt            int        `json:"probe_attempt"`
	SuccessfulSinceRecovery int        `json:"successful_since_recovery"`
	LastProbeAt             *time.Time `json:"last_probe_at,omitempty"`
	LastFailureAt           *time.Time `json:"last_failure_at,omitempty"`
}

// ListCooldowns returns every currently-banned scope joined with the proxy
// address. Powers the dashboard cooldown tab.
func (r *BanRepository) ListCooldowns(ctx context.Context) ([]CooldownRow, error) {
	now := time.Now()

	if r.db.IsMongo() {
		col := r.db.MongoDB().Collection("proxy_domain_bans")
		cur, err := col.Find(ctx, bson.M{"state": StateBanned})
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)

		type doc struct {
			ProxyID                 int        `bson:"proxy_id"`
			MachineID               string     `bson:"machine_id"`
			TargetDomain            string     `bson:"target_domain"`
			TargetCountry           string     `bson:"target_country"`
			State                   string     `bson:"state"`
			BannedAt                *time.Time `bson:"banned_at"`
			NextProbeAt             *time.Time `bson:"next_probe_at"`
			ProbeAttempt            int        `bson:"probe_attempt"`
			SuccessfulSinceRecovery int        `bson:"successful_since_recovery"`
			LastProbeAt             *time.Time `bson:"last_probe_at"`
			LastFailureAt           *time.Time `bson:"last_failure_at"`
		}
		var raw []doc
		if err := cur.All(ctx, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return []CooldownRow{}, nil
		}

		// Batch-fetch proxy addresses.
		idSet := map[int]struct{}{}
		for _, d := range raw {
			idSet[d.ProxyID] = struct{}{}
		}
		ids := make([]int, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		pcur, err := r.db.MongoDB().Collection("proxies").Find(ctx, bson.M{"id": bson.M{"$in": ids}})
		if err != nil {
			return nil, err
		}
		defer pcur.Close(ctx)
		addr := map[int]string{}
		for pcur.Next(ctx) {
			var p struct {
				ID      int    `bson:"id"`
				Address string `bson:"address"`
			}
			if err := pcur.Decode(&p); err != nil {
				return nil, err
			}
			addr[p.ID] = p.Address
		}

		out := make([]CooldownRow, 0, len(raw))
		for _, d := range raw {
			row := CooldownRow{
				ProxyID:                 d.ProxyID,
				ProxyAddress:            addr[d.ProxyID],
				TargetDomain:            d.TargetDomain,
				TargetCountry:           d.TargetCountry,
				MachineID:               d.MachineID,
				State:                   d.State,
				DisplayState:            displayState(d.State, d.ProbeAttempt),
				BannedAt:                d.BannedAt,
				NextProbeAt:             d.NextProbeAt,
				ProbeAttempt:            d.ProbeAttempt,
				SuccessfulSinceRecovery: d.SuccessfulSinceRecovery,
				LastProbeAt:             d.LastProbeAt,
				LastFailureAt:           d.LastFailureAt,
			}
			if d.NextProbeAt != nil {
				remain := int64(d.NextProbeAt.Sub(now).Seconds())
				if remain < 0 {
					remain = 0
				}
				row.CooldownRemainingSec = remain
			}
			out = append(out, row)
		}
		return out, nil
	}

	rows, err := r.db.Pool.Query(ctx, `
		SELECT b.proxy_id, b.machine_id, b.target_domain, b.target_country,
		       b.state, b.banned_at, b.next_probe_at, b.probe_attempt,
		       b.successful_since_recovery, b.last_probe_at, b.last_failure_at,
		       p.address
		FROM proxy_domain_bans b
		JOIN proxies p ON p.id = b.proxy_id
		WHERE b.state = 'banned'
		ORDER BY b.next_probe_at ASC NULLS LAST
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CooldownRow, 0)
	for rows.Next() {
		var (
			r0 CooldownRow
			tc *string
		)
		if err := rows.Scan(
			&r0.ProxyID, &r0.MachineID, &r0.TargetDomain, &tc,
			&r0.State, &r0.BannedAt, &r0.NextProbeAt, &r0.ProbeAttempt,
			&r0.SuccessfulSinceRecovery, &r0.LastProbeAt, &r0.LastFailureAt,
			&r0.ProxyAddress,
		); err != nil {
			return nil, err
		}
		if tc != nil {
			r0.TargetCountry = *tc
		}
		r0.DisplayState = displayState(r0.State, r0.ProbeAttempt)
		if r0.NextProbeAt != nil {
			remain := int64(r0.NextProbeAt.Sub(now).Seconds())
			if remain < 0 {
				remain = 0
			}
			r0.CooldownRemainingSec = remain
		}
		out = append(out, r0)
	}
	return out, nil
}

// NextProbeBatch returns banned scopes whose next_probe_at has fired. The
// probe worker calls this in a loop. Joined with proxy info so the worker
// doesn't need a second round-trip.
type ProbeTarget struct {
	Scope         BanScope
	ProxyAddress  string
	ProxyProtocol string
	ProxyUsername *string
	ProxyPassword *string
}

func (r *BanRepository) NextProbeBatch(ctx context.Context, limit int) ([]ProbeTarget, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now()
	out := []ProbeTarget{}

	if r.db.IsMongo() {
		opts := options.Find().SetLimit(int64(limit)).SetSort(bson.D{{Key: "next_probe_at", Value: 1}})
		cur, err := r.db.MongoDB().Collection("proxy_domain_bans").Find(ctx, bson.M{
			"state":         StateBanned,
			"next_probe_at": bson.M{"$lte": now},
		}, opts)
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)
		type bd struct {
			ProxyID       int    `bson:"proxy_id"`
			MachineID     string `bson:"machine_id"`
			TargetDomain  string `bson:"target_domain"`
			TargetCountry string `bson:"target_country"`
		}
		var raw []bd
		if err := cur.All(ctx, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return out, nil
		}
		idSet := map[int]struct{}{}
		for _, b := range raw {
			idSet[b.ProxyID] = struct{}{}
		}
		ids := make([]int, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		pcur, err := r.db.MongoDB().Collection("proxies").Find(ctx, bson.M{"id": bson.M{"$in": ids}})
		if err != nil {
			return nil, err
		}
		defer pcur.Close(ctx)
		type pd struct {
			ID       int     `bson:"id"`
			Address  string  `bson:"address"`
			Protocol string  `bson:"protocol"`
			Username *string `bson:"username,omitempty"`
			Password *string `bson:"password,omitempty"`
		}
		proxByID := map[int]pd{}
		for pcur.Next(ctx) {
			var p pd
			if err := pcur.Decode(&p); err != nil {
				return nil, err
			}
			proxByID[p.ID] = p
		}
		for _, b := range raw {
			p, ok := proxByID[b.ProxyID]
			if !ok {
				continue
			}
			out = append(out, ProbeTarget{
				Scope: BanScope{
					ProxyID:       b.ProxyID,
					MachineID:     b.MachineID,
					TargetDomain:  b.TargetDomain,
					TargetCountry: b.TargetCountry,
				},
				ProxyAddress:  p.Address,
				ProxyProtocol: p.Protocol,
				ProxyUsername: p.Username,
				ProxyPassword: p.Password,
			})
		}
		return out, nil
	}

	rows, err := r.db.Pool.Query(ctx, `
		SELECT b.proxy_id, b.machine_id, b.target_domain, COALESCE(b.target_country, ''),
		       p.address, p.protocol, p.username, p.password
		FROM proxy_domain_bans b
		JOIN proxies p ON p.id = b.proxy_id
		WHERE b.state = 'banned' AND b.next_probe_at <= $1
		ORDER BY b.next_probe_at ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t ProbeTarget
		if err := rows.Scan(
			&t.Scope.ProxyID, &t.Scope.MachineID, &t.Scope.TargetDomain, &t.Scope.TargetCountry,
			&t.ProxyAddress, &t.ProxyProtocol, &t.ProxyUsername, &t.ProxyPassword,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// helpers

func displayState(state string, probeAttempt int) string {
	if state != StateBanned {
		return DisplayActive
	}
	if probeAttempt == 0 {
		return DisplayCooldown
	}
	return DisplayRecoveryTest
}

func (s BanScope) valid() bool {
	return s.ProxyID > 0 && s.MachineID != "" && s.TargetDomain != ""
}

func (s BanScope) mongoFilter() bson.M {
	return bson.M{
		"proxy_id":      s.ProxyID,
		"machine_id":    s.MachineID,
		"target_domain": s.TargetDomain,
	}
}

func (s BanScope) mongoSetOnInsert() bson.M {
	return bson.M{
		"proxy_id":      s.ProxyID,
		"machine_id":    s.MachineID,
		"target_domain": s.TargetDomain,
	}
}

func (s BanScope) String() string {
	return fmt.Sprintf("proxy=%d machine=%s domain=%s", s.ProxyID, s.MachineID, s.TargetDomain)
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
