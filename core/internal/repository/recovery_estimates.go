package repository

// Per-site recovery learning. Every time a banned scope actually comes back to
// life we bank one observation in recovery_events: how long the ban lasted and
// how many trials it took. Aggregating those observations per site gives the
// "estimated cooldown period for <site> is 1.5 hour" figure the dashboard
// shows, and the raw rows behind it are what you get when you click through.
//
// Caveat worth remembering when reading these numbers: a recovery is only
// observed when a trial succeeds, so recovery_sec is an UPPER bound on the true
// unban moment — the site may have lifted the ban any time after the previous
// failed trial. The doubling trial ladder (30m, 60m, 120m, 240m…) is what
// bounds how loose that estimate can get.

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// maxRecoverySamples caps how many raw observations we pull for aggregation so
// a long-lived deployment cannot turn the estimates endpoint into a full scan.
const maxRecoverySamples = 20000

// recoverySample is one observed ban to active transition, the unit the
// estimates are computed from.
type recoverySample struct {
	TargetDomain  string
	TargetCountry string
	MachineID     string
	ProxyID       int
	RecoverySec   int64
	Trials        int
	BannedAt      time.Time
	RecoveredAt   time.Time
}

// DomainRecoveryEstimate is the learned cooldown profile for one site.
type DomainRecoveryEstimate struct {
	TargetDomain     string     `json:"target_domain"`
	TargetCountry    string     `json:"target_country,omitempty"`
	Samples          int        `json:"samples"`
	EstimatedSec     int64      `json:"estimated_sec"`
	MedianSec        int64      `json:"median_sec"`
	MinSec           int64      `json:"min_sec"`
	MaxSec           int64      `json:"max_sec"`
	AvgTrials        float64    `json:"avg_trials"`
	DistinctProxies  int        `json:"distinct_proxies"`
	DistinctMachines int        `json:"distinct_machines"`
	CurrentlyBanned  int        `json:"currently_banned"`
	LastRecoveredAt  *time.Time `json:"last_recovered_at,omitempty"`
}

// RecoveryEventRow is one observation as surfaced by the drill-down view.
type RecoveryEventRow struct {
	ProxyID       int       `json:"proxy_id"`
	ProxyAddress  string    `json:"proxy_address,omitempty"`
	MachineID     string    `json:"machine_id"`
	TargetDomain  string    `json:"target_domain"`
	TargetCountry string    `json:"target_country,omitempty"`
	BannedAt      time.Time `json:"banned_at"`
	RecoveredAt   time.Time `json:"recovered_at"`
	RecoverySec   int64     `json:"recovery_sec"`
	Trials        int       `json:"trials"`
}

// recordRecoveryEvent banks one ban to active observation. Best-effort by
// design: the originating request has already succeeded, so a stats write
// failure is swallowed rather than failing the caller.
func (r *BanRepository) recordRecoveryEvent(ctx context.Context, scope BanScope, bannedAt, recoveredAt time.Time, trials int) {
	if !scope.valid() || bannedAt.IsZero() || recoveredAt.Before(bannedAt) {
		return
	}
	sec := int64(recoveredAt.Sub(bannedAt).Seconds())
	if sec <= 0 {
		return
	}

	if r.db.IsMongo() {
		_, _ = r.db.MongoDB().Collection("recovery_events").InsertOne(ctx, bson.M{
			"proxy_id":       scope.ProxyID,
			"machine_id":     scope.MachineID,
			"target_domain":  scope.TargetDomain,
			"target_country": scope.TargetCountry,
			"banned_at":      bannedAt,
			"recovered_at":   recoveredAt,
			"recovery_sec":   sec,
			"trials":         trials,
		})
		return
	}

	_, _ = r.db.Pool.Exec(ctx, `
		INSERT INTO recovery_events (
			proxy_id, machine_id, target_domain, target_country,
			banned_at, recovered_at, recovery_sec, trials
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, scope.ProxyID, scope.MachineID, scope.TargetDomain,
		nullableStr(scope.TargetCountry), bannedAt, recoveredAt, sec, trials)
}

// loadRecoverySamples pulls raw observations, newest first. An empty domain
// means every site. Shared by the aggregate and drill-down paths so both
// drivers get identical maths.
func (r *BanRepository) loadRecoverySamples(ctx context.Context, domain string, limit int) ([]recoverySample, error) {
	if limit <= 0 || limit > maxRecoverySamples {
		limit = maxRecoverySamples
	}
	out := make([]recoverySample, 0, 64)

	if r.db.IsMongo() {
		filter := bson.M{}
		if domain != "" {
			filter["target_domain"] = domain
		}
		cur, err := r.db.MongoDB().Collection("recovery_events").Find(ctx, filter,
			options.Find().SetSort(bson.D{{Key: "recovered_at", Value: -1}}).SetLimit(int64(limit)))
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)
		type doc struct {
			ProxyID       int       `bson:"proxy_id"`
			MachineID     string    `bson:"machine_id"`
			TargetDomain  string    `bson:"target_domain"`
			TargetCountry string    `bson:"target_country"`
			BannedAt      time.Time `bson:"banned_at"`
			RecoveredAt   time.Time `bson:"recovered_at"`
			RecoverySec   int64     `bson:"recovery_sec"`
			Trials        int       `bson:"trials"`
		}
		var raw []doc
		if err := cur.All(ctx, &raw); err != nil {
			return nil, err
		}
		for _, d := range raw {
			out = append(out, recoverySample{
				TargetDomain: d.TargetDomain, TargetCountry: d.TargetCountry,
				MachineID: d.MachineID, ProxyID: d.ProxyID,
				RecoverySec: d.RecoverySec, Trials: d.Trials,
				BannedAt: d.BannedAt, RecoveredAt: d.RecoveredAt,
			})
		}
		return out, nil
	}

	query := `
		SELECT proxy_id, machine_id, target_domain, COALESCE(target_country, ''),
		       banned_at, recovered_at, recovery_sec, trials
		FROM recovery_events`
	args := []any{}
	if domain != "" {
		query += ` WHERE target_domain = $1 ORDER BY recovered_at DESC LIMIT $2`
		args = append(args, domain, limit)
	} else {
		query += ` ORDER BY recovered_at DESC LIMIT $1`
		args = append(args, limit)
	}

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s recoverySample
		if err := rows.Scan(&s.ProxyID, &s.MachineID, &s.TargetDomain, &s.TargetCountry,
			&s.BannedAt, &s.RecoveredAt, &s.RecoverySec, &s.Trials); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ListDomainRecoveryEstimates returns the learned cooldown profile per site,
// ordered by sample count (best-evidenced sites first). Sites never observed
// recovering simply do not appear — there is nothing to estimate from.
func (r *BanRepository) ListDomainRecoveryEstimates(ctx context.Context) ([]DomainRecoveryEstimate, error) {
	samples, err := r.loadRecoverySamples(ctx, "", 0)
	if err != nil {
		return nil, err
	}

	type acc struct {
		durations []int64
		trials    int
		country   string
		proxies   map[int]struct{}
		machines  map[string]struct{}
		last      time.Time
	}
	byDomain := map[string]*acc{}
	for _, s := range samples {
		a, ok := byDomain[s.TargetDomain]
		if !ok {
			a = &acc{proxies: map[int]struct{}{}, machines: map[string]struct{}{}}
			byDomain[s.TargetDomain] = a
		}
		a.durations = append(a.durations, s.RecoverySec)
		a.trials += s.Trials
		a.proxies[s.ProxyID] = struct{}{}
		a.machines[s.MachineID] = struct{}{}
		// Samples arrive newest-first, so the first country seen is current.
		if a.country == "" {
			a.country = s.TargetCountry
		}
		if s.RecoveredAt.After(a.last) {
			a.last = s.RecoveredAt
		}
	}

	banned, err := r.currentlyBannedByDomain(ctx)
	if err != nil {
		// Live ban counts sit next to the estimate as context; losing them
		// must not blank out the whole page.
		banned = map[string]int{}
	}

	out := make([]DomainRecoveryEstimate, 0, len(byDomain))
	for domain, a := range byDomain {
		d := append([]int64(nil), a.durations...)
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })

		var sum int64
		for _, v := range d {
			sum += v
		}
		est := DomainRecoveryEstimate{
			TargetDomain:     domain,
			TargetCountry:    a.country,
			Samples:          len(d),
			EstimatedSec:     sum / int64(len(d)),
			MedianSec:        median(d),
			MinSec:           d[0],
			MaxSec:           d[len(d)-1],
			AvgTrials:        float64(a.trials) / float64(len(d)),
			DistinctProxies:  len(a.proxies),
			DistinctMachines: len(a.machines),
			CurrentlyBanned:  banned[domain],
		}
		if !a.last.IsZero() {
			last := a.last
			est.LastRecoveredAt = &last
		}
		out = append(out, est)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Samples != out[j].Samples {
			return out[i].Samples > out[j].Samples
		}
		return out[i].TargetDomain < out[j].TargetDomain
	})
	return out, nil
}

// ListRecoveryEvents returns the raw observations behind one site's estimate,
// newest first, with proxy addresses resolved for display.
func (r *BanRepository) ListRecoveryEvents(ctx context.Context, domain string, limit int) ([]RecoveryEventRow, error) {
	if domain == "" {
		return []RecoveryEventRow{}, nil
	}
	if limit <= 0 {
		limit = 200
	}
	samples, err := r.loadRecoverySamples(ctx, domain, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int, 0, len(samples))
	seen := map[int]struct{}{}
	for _, s := range samples {
		if _, ok := seen[s.ProxyID]; !ok {
			seen[s.ProxyID] = struct{}{}
			ids = append(ids, s.ProxyID)
		}
	}
	addr := r.proxyAddresses(ctx, ids)

	out := make([]RecoveryEventRow, 0, len(samples))
	for _, s := range samples {
		out = append(out, RecoveryEventRow{
			ProxyID:       s.ProxyID,
			ProxyAddress:  addr[s.ProxyID],
			MachineID:     s.MachineID,
			TargetDomain:  s.TargetDomain,
			TargetCountry: s.TargetCountry,
			BannedAt:      s.BannedAt,
			RecoveredAt:   s.RecoveredAt,
			RecoverySec:   s.RecoverySec,
			Trials:        s.Trials,
		})
	}
	return out, nil
}

// currentlyBannedByDomain counts scopes sitting in the banned state per site,
// so the estimate can be read next to how many are waiting on it right now.
func (r *BanRepository) currentlyBannedByDomain(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}

	if r.db.IsMongo() {
		cur, err := r.db.MongoDB().Collection("proxy_domain_bans").
			Find(ctx, bson.M{"state": StateBanned})
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)
		for cur.Next(ctx) {
			var d struct {
				TargetDomain string `bson:"target_domain"`
			}
			if err := cur.Decode(&d); err != nil {
				return nil, err
			}
			out[d.TargetDomain]++
		}
		return out, nil
	}

	rows, err := r.db.Pool.Query(ctx, `
		SELECT target_domain, COUNT(*) FROM proxy_domain_bans
		WHERE state = 'banned' GROUP BY target_domain
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d] = n
	}
	return out, nil
}

// proxyAddresses resolves ids to "host:port" for display. Best-effort: a proxy
// deleted since the observation was banked simply resolves to empty.
func (r *BanRepository) proxyAddresses(ctx context.Context, ids []int) map[int]string {
	addr := map[int]string{}
	if len(ids) == 0 {
		return addr
	}

	if r.db.IsMongo() {
		cur, err := r.db.MongoDB().Collection("proxies").Find(ctx, bson.M{"id": bson.M{"$in": ids}})
		if err != nil {
			return addr
		}
		defer cur.Close(ctx)
		for cur.Next(ctx) {
			var p struct {
				ID      int    `bson:"id"`
				Address string `bson:"address"`
			}
			if err := cur.Decode(&p); err != nil {
				return addr
			}
			addr[p.ID] = p.Address
		}
		return addr
	}

	rows, err := r.db.Pool.Query(ctx, `SELECT id, address FROM proxies WHERE id = ANY($1)`, ids)
	if err != nil {
		return addr
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var a string
		if err := rows.Scan(&id, &a); err != nil {
			return addr
		}
		addr[id] = a
	}
	return addr
}

// median expects a slice already sorted ascending and non-empty.
func median(sorted []int64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// EnsureRecoveryEventIndexes creates the lookup keys for recovery_events.
// Called alongside the other Mongo index setup at startup.
func (r *BanRepository) EnsureRecoveryEventIndexes(ctx context.Context) error {
	if !r.db.IsMongo() {
		return nil
	}
	_, err := r.db.MongoDB().Collection("recovery_events").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "target_domain", Value: 1}, {Key: "recovered_at", Value: -1}}},
		{Keys: bson.D{{Key: "recovered_at", Value: -1}}},
	})
	return err
}
