package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrNoEligibleProxy is returned by Checkout when no proxy matches the
// requested filter (or the pool is empty / fully unhealthy).
var ErrNoEligibleProxy = errors.New("no eligible proxy")

// AssignmentRepository owns the proxy_assignments table/collection and the
// sticky checkout logic.
type AssignmentRepository struct {
	db *database.DB
}

func NewAssignmentRepository(db *database.DB) *AssignmentRepository {
	return &AssignmentRepository{db: db}
}

// Checkout returns a proxy for (machine_id, domain). If a sticky assignment
// already exists and the underlying proxy is still healthy ("active" or
// "idle"), it is reused. Otherwise a fresh proxy is picked from the pool.
//
// targetCountry is treated as a SOFT preference: if any healthy proxy has
// that country we pick from that subset; if none do, we fall back to the
// full healthy pool. Either way we record targetCountry on the assignment so
// the dashboard can group by it.
func (r *AssignmentRepository) Checkout(
	ctx context.Context,
	machineID, domain, targetCountry string,
) (*models.Proxy, bool, error) {
	// 1. Look up existing assignment.
	existingProxyID, hasExisting, err := r.lookupAssignment(ctx, machineID, domain)
	if err != nil {
		return nil, false, fmt.Errorf("lookup assignment: %w", err)
	}

	if hasExisting {
		// Reuse if still healthy. We don't apply the country preference on
		// reuse — sticky bindings shouldn't flip just because the user
		// changed the target_country tag.
		proxy, err := r.fetchProxyIfHealthy(ctx, existingProxyID, "")
		if err != nil {
			return nil, false, err
		}
		if proxy != nil {
			if touchErr := r.touchAssignment(ctx, machineID, domain, targetCountry); touchErr != nil {
				// Non-fatal: stats only.
				_ = touchErr
			}
			return proxy, true, nil
		}
		// Existing assignment's proxy is gone or unhealthy → fall through and
		// pick a new one.
	}

	// 2. Try the country-preferred pool first; fall back to any healthy proxy.
	pool, err := r.eligiblePool(ctx, targetCountry)
	if err != nil {
		return nil, false, err
	}
	if len(pool) == 0 && targetCountry != "" {
		pool, err = r.eligiblePool(ctx, "")
		if err != nil {
			return nil, false, err
		}
	}
	if len(pool) == 0 {
		return nil, false, ErrNoEligibleProxy
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
	if err != nil {
		return nil, false, fmt.Errorf("rand: %w", err)
	}
	picked := pool[idx.Int64()]

	// 3. Persist the new assignment.
	if err := r.upsertAssignment(ctx, machineID, domain, picked.ID, targetCountry); err != nil {
		return nil, false, fmt.Errorf("upsert assignment: %w", err)
	}

	return picked, false, nil
}

// ListWithProxies returns every active assignment joined with proxy details.
// Used by the Infrastructure page.
func (r *AssignmentRepository) ListWithProxies(ctx context.Context) ([]models.AssignmentWithProxy, error) {
	if r.db.IsMongo() {
		return r.listWithProxiesMongo(ctx)
	}
	return r.listWithProxiesPG(ctx)
}

// Release deletes the sticky assignment for (machine_id, domain).
func (r *AssignmentRepository) Release(ctx context.Context, machineID, domain string) error {
	if r.db.IsMongo() {
		_, err := r.db.MongoDB().Collection("proxy_assignments").DeleteOne(ctx, bson.M{
			"machine_id": machineID,
			"domain":     domain,
		})
		return err
	}
	_, err := r.db.Pool.Exec(ctx, `
		DELETE FROM proxy_assignments
		WHERE machine_id = $1 AND domain = $2
	`, machineID, domain)
	return err
}

// EnsureMongoIndexes creates the unique (machine_id, domain) index and
// supporting secondary indexes. Called once at startup for the Mongo path.
func (r *AssignmentRepository) EnsureMongoIndexes(ctx context.Context) error {
	if !r.db.IsMongo() {
		return nil
	}
	col := r.db.MongoDB().Collection("proxy_assignments")
	_, err := col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "machine_id", Value: 1},
				{Key: "domain", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "machine_id", Value: 1}}},
		{Keys: bson.D{{Key: "proxy_id", Value: 1}}},
	})
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// internals
// ─────────────────────────────────────────────────────────────────────────────

type assignmentDoc struct {
	MachineID     string    `bson:"machine_id"`
	Domain        string    `bson:"domain"`
	ProxyID       int       `bson:"proxy_id"`
	TargetCountry string    `bson:"target_country,omitempty"`
	AssignedAt    time.Time `bson:"assigned_at"`
	LastUsedAt    time.Time `bson:"last_used_at"`
	RequestCount  int64     `bson:"request_count"`
}

func (r *AssignmentRepository) lookupAssignment(ctx context.Context, machineID, domain string) (int, bool, error) {
	if r.db.IsMongo() {
		var doc assignmentDoc
		err := r.db.MongoDB().Collection("proxy_assignments").
			FindOne(ctx, bson.M{"machine_id": machineID, "domain": domain}).
			Decode(&doc)
		if err == mongo.ErrNoDocuments {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		return doc.ProxyID, true, nil
	}

	var proxyID int
	err := r.db.Pool.QueryRow(ctx, `
		SELECT proxy_id FROM proxy_assignments
		WHERE machine_id = $1 AND domain = $2
	`, machineID, domain).Scan(&proxyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return proxyID, true, nil
}

// fetchProxyIfHealthy returns the proxy if it is still in a healthy state
// (active/idle) and matches the optional country filter; otherwise nil.
func (r *AssignmentRepository) fetchProxyIfHealthy(ctx context.Context, proxyID int, country string) (*models.Proxy, error) {
	if r.db.IsMongo() {
		var p proxyDoc
		err := r.db.MongoDB().Collection("proxies").
			FindOne(ctx, bson.M{"id": proxyID}).Decode(&p)
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if p.Status != "active" && p.Status != "idle" {
			return nil, nil
		}
		if country != "" && (p.Country == nil || *p.Country != country) {
			return nil, nil
		}
		return proxyDocToModel(p), nil
	}

	var p models.Proxy
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, address, protocol, username, password, status, category, cost, country,
		       requests, successful_requests, failed_requests,
		       avg_response_time, last_check, last_error, created_at, updated_at
		FROM proxies WHERE id = $1
	`, proxyID).Scan(
		&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status, &p.Category, &p.Cost, &p.Country,
		&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
		&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Status != "active" && p.Status != "idle" {
		return nil, nil
	}
	if country != "" && (p.Country == nil || *p.Country != country) {
		return nil, nil
	}
	return &p, nil
}

// eligiblePool returns active+idle proxies that match the country filter.
func (r *AssignmentRepository) eligiblePool(ctx context.Context, country string) ([]*models.Proxy, error) {
	if r.db.IsMongo() {
		filter := bson.M{"status": bson.M{"$in": []string{"active", "idle"}}}
		if country != "" {
			filter["country"] = country
		}
		cur, err := r.db.MongoDB().Collection("proxies").Find(ctx, filter)
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)

		out := make([]*models.Proxy, 0, 128)
		for cur.Next(ctx) {
			var p proxyDoc
			if err := cur.Decode(&p); err != nil {
				return nil, err
			}
			out = append(out, proxyDocToModel(p))
		}
		return out, nil
	}

	args := []interface{}{[]string{"active", "idle"}}
	q := `
		SELECT id, address, protocol, username, password, status, category, cost, country,
		       requests, successful_requests, failed_requests,
		       avg_response_time, last_check, last_error, created_at, updated_at
		FROM proxies WHERE status = ANY($1)
	`
	if country != "" {
		q += " AND country = $2"
		args = append(args, country)
	}
	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*models.Proxy, 0, 128)
	for rows.Next() {
		var p models.Proxy
		if err := rows.Scan(
			&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status, &p.Category, &p.Cost, &p.Country,
			&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
			&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, nil
}

func (r *AssignmentRepository) upsertAssignment(ctx context.Context, machineID, domain string, proxyID int, targetCountry string) error {
	now := time.Now()
	if r.db.IsMongo() {
		set := bson.M{
			"proxy_id":     proxyID,
			"last_used_at": now,
		}
		if targetCountry != "" {
			set["target_country"] = targetCountry
		}
		_, err := r.db.MongoDB().Collection("proxy_assignments").UpdateOne(
			ctx,
			bson.M{"machine_id": machineID, "domain": domain},
			bson.M{
				"$set": set,
				"$setOnInsert": bson.M{
					"machine_id":  machineID,
					"domain":      domain,
					"assigned_at": now,
				},
				"$inc": bson.M{"request_count": 1},
			},
			options.Update().SetUpsert(true),
		)
		return err
	}

	var tc *string
	if targetCountry != "" {
		tc = &targetCountry
	}
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO proxy_assignments (machine_id, domain, proxy_id, target_country, assigned_at, last_used_at, request_count)
		VALUES ($1, $2, $3, $4, $5, $5, 1)
		ON CONFLICT (machine_id, domain) DO UPDATE SET
			proxy_id       = EXCLUDED.proxy_id,
			target_country = COALESCE(EXCLUDED.target_country, proxy_assignments.target_country),
			assigned_at    = CASE WHEN proxy_assignments.proxy_id = EXCLUDED.proxy_id
			                      THEN proxy_assignments.assigned_at ELSE EXCLUDED.assigned_at END,
			last_used_at   = EXCLUDED.last_used_at,
			request_count  = proxy_assignments.request_count + 1
	`, machineID, domain, proxyID, tc, now)
	return err
}

func (r *AssignmentRepository) touchAssignment(ctx context.Context, machineID, domain, targetCountry string) error {
	now := time.Now()
	if r.db.IsMongo() {
		set := bson.M{"last_used_at": now}
		if targetCountry != "" {
			set["target_country"] = targetCountry
		}
		_, err := r.db.MongoDB().Collection("proxy_assignments").UpdateOne(
			ctx,
			bson.M{"machine_id": machineID, "domain": domain},
			bson.M{
				"$set": set,
				"$inc": bson.M{"request_count": 1},
			},
		)
		return err
	}
	if targetCountry != "" {
		_, err := r.db.Pool.Exec(ctx, `
			UPDATE proxy_assignments
			SET last_used_at = $3, request_count = request_count + 1, target_country = $4
			WHERE machine_id = $1 AND domain = $2
		`, machineID, domain, now, targetCountry)
		return err
	}
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE proxy_assignments
		SET last_used_at = $3, request_count = request_count + 1
		WHERE machine_id = $1 AND domain = $2
	`, machineID, domain, now)
	return err
}

func (r *AssignmentRepository) listWithProxiesPG(ctx context.Context) ([]models.AssignmentWithProxy, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT a.machine_id, a.domain, COALESCE(a.target_country, ''),
		       a.assigned_at, a.last_used_at, a.request_count,
		       p.id, p.address, p.protocol, p.username, p.status, p.category, p.country, p.cost,
		       p.requests, p.successful_requests, p.avg_response_time
		FROM proxy_assignments a
		JOIN proxies p ON p.id = a.proxy_id
		ORDER BY a.last_used_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AssignmentWithProxy, 0)
	for rows.Next() {
		var a models.AssignmentWithProxy
		var requests, successful int64
		if err := rows.Scan(
			&a.MachineID, &a.Domain, &a.TargetCountry,
			&a.AssignedAt, &a.LastUsedAt, &a.RequestCount,
			&a.ProxyID, &a.ProxyAddress, &a.ProxyProtocol, &a.ProxyUsername, &a.ProxyStatus,
			&a.ProxyCategory, &a.ProxyCountry, &a.ProxyCost,
			&requests, &successful, &a.AvgResponseTime,
		); err != nil {
			return nil, err
		}
		if requests > 0 {
			a.SuccessRate = float64(successful) / float64(requests) * 100
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *AssignmentRepository) listWithProxiesMongo(ctx context.Context) ([]models.AssignmentWithProxy, error) {
	cur, err := r.db.MongoDB().Collection("proxy_assignments").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var docs []assignmentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return []models.AssignmentWithProxy{}, nil
	}

	idSet := make(map[int]struct{}, len(docs))
	for _, d := range docs {
		idSet[d.ProxyID] = struct{}{}
	}
	ids := make([]int, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}

	// Single batched lookup; the previous per-id FindOne loop made one Atlas
	// round-trip per assignment and dominated /api/v1/infrastructure latency.
	pcur, err := r.db.MongoDB().Collection("proxies").Find(ctx, bson.M{"id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer pcur.Close(ctx)
	proxyByID := make(map[int]proxyDoc, len(ids))
	for pcur.Next(ctx) {
		var p proxyDoc
		if err := pcur.Decode(&p); err != nil {
			return nil, err
		}
		proxyByID[p.ID] = p
	}

	out := make([]models.AssignmentWithProxy, 0, len(docs))
	for _, doc := range docs {
		p, ok := proxyByID[doc.ProxyID]
		if !ok {
			continue
		}
		successRate := 0.0
		if p.Requests > 0 {
			successRate = float64(p.SuccessfulRequests) / float64(p.Requests) * 100
		}
		out = append(out, models.AssignmentWithProxy{
			MachineID:       doc.MachineID,
			Domain:          doc.Domain,
			TargetCountry:   doc.TargetCountry,
			AssignedAt:      doc.AssignedAt,
			LastUsedAt:      doc.LastUsedAt,
			RequestCount:    doc.RequestCount,
			ProxyID:         p.ID,
			ProxyAddress:    p.Address,
			ProxyProtocol:   p.Protocol,
			ProxyUsername:   p.Username,
			ProxyStatus:     p.Status,
			ProxyCategory:   p.Category,
			ProxyCountry:    p.Country,
			ProxyCost:       p.Cost,
			SuccessRate:     successRate,
			AvgResponseTime: p.AvgResponseTime,
		})
	}
	return out, nil
}
