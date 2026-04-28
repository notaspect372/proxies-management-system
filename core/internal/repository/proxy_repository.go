package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ProxyRepository handles proxy database operations
type ProxyRepository struct {
	db *database.DB
}

type proxyDoc struct {
	ID                 int        `bson:"id"`
	Address            string     `bson:"address"`
	Protocol           string     `bson:"protocol"`
	Username           *string    `bson:"username,omitempty"`
	Password           *string    `bson:"password,omitempty"`
	Status             string     `bson:"status"`
	Category           *string    `bson:"category,omitempty"`
	Cost               *float64   `bson:"cost,omitempty"`
	Country            *string    `bson:"country,omitempty"`
	Requests           int64      `bson:"requests"`
	SuccessfulRequests int64      `bson:"successful_requests"`
	FailedRequests     int64      `bson:"failed_requests"`
	AvgResponseTime    int        `bson:"avg_response_time"`
	LastCheck          *time.Time `bson:"last_check,omitempty"`
	LastError          *string    `bson:"last_error,omitempty"`
	CreatedAt          time.Time  `bson:"created_at"`
	UpdatedAt          time.Time  `bson:"updated_at"`
}

// NewProxyRepository creates a new ProxyRepository
func NewProxyRepository(db *database.DB) *ProxyRepository {
	return &ProxyRepository{db: db}
}

// GetDB returns the database instance
func (r *ProxyRepository) GetDB() *database.DB {
	return r.db
}

// List retrieves proxies with pagination and filters
func (r *ProxyRepository) List(ctx context.Context, page, limit int, search, status, protocol, category, country, sortField, sortOrder string) ([]models.ProxyWithStats, int, error) {
	if r.db.IsMongo() {
		filter := bson.M{}
		if search != "" {
			filter["address"] = bson.M{"$regex": search, "$options": "i"}
		}
		if status != "" {
			filter["status"] = status
		}
		if protocol != "" {
			filter["protocol"] = protocol
		}
		if category != "" {
			filter["category"] = category
		}
		if country != "" {
			filter["country"] = country
		}

		total64, err := r.db.MongoDB().Collection("proxies").CountDocuments(ctx, filter)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count proxies: %w", err)
		}

		validSortFields := map[string]bool{
			"address":           true,
			"status":            true,
			"requests":          true,
			"avg_response_time": true,
			"created_at":        true,
		}
		if !validSortFields[sortField] {
			sortField = "created_at"
		}
		sortDirection := -1
		if sortOrder == "asc" {
			sortDirection = 1
		}

		offset := (page - 1) * limit
		cursor, err := r.db.MongoDB().Collection("proxies").Find(
			ctx,
			filter,
			options.Find().
				SetSort(bson.D{{Key: sortField, Value: sortDirection}}).
				SetSkip(int64(offset)).
				SetLimit(int64(limit)),
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to list proxies: %w", err)
		}
		defer cursor.Close(ctx)

		proxies := []models.ProxyWithStats{}
		for cursor.Next(ctx) {
			var p proxyDoc
			if err := cursor.Decode(&p); err != nil {
				return nil, 0, fmt.Errorf("failed to decode proxy: %w", err)
			}

			successRate := 0.0
			if p.Requests > 0 {
				successRate = (float64(p.SuccessfulRequests) / float64(p.Requests)) * 100
			}
			proxies = append(proxies, models.ProxyWithStats{
				ID:              p.ID,
				Address:         p.Address,
				Protocol:        p.Protocol,
				Username:        p.Username,
				Status:          p.Status,
				Category:        p.Category,
				Cost:            p.Cost,
				Country:         p.Country,
				Requests:        p.Requests,
				SuccessRate:     successRate,
				AvgResponseTime: p.AvgResponseTime,
				LastCheck:       p.LastCheck,
				CreatedAt:       p.CreatedAt,
				UpdatedAt:       p.UpdatedAt,
			})
		}

		return proxies, int(total64), nil
	}

	// Build WHERE clause
	whereClauses := []string{}
	args := []interface{}{}
	argPos := 1

	if search != "" {
		// Use both ILIKE for simple search and to_tsvector for full-text search
		whereClauses = append(whereClauses, fmt.Sprintf("(address ILIKE $%d OR to_tsvector('simple', address) @@ plainto_tsquery('simple', $%d))", argPos, argPos))
		args = append(args, "%"+search+"%")
		argPos++
	}

	if status != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argPos))
		args = append(args, status)
		argPos++
	}

	if protocol != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("protocol = $%d", argPos))
		args = append(args, protocol)
		argPos++
	}

	if category != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("category = $%d", argPos))
		args = append(args, category)
		argPos++
	}

	if country != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("country = $%d", argPos))
		args = append(args, country)
		argPos++
	}

	whereClause := ""
	if len(whereClauses) > 0 {
		whereClause = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Validate and set sort field
	validSortFields := map[string]bool{
		"address":           true,
		"status":            true,
		"requests":          true,
		"avg_response_time": true,
		"created_at":        true,
	}

	if !validSortFields[sortField] {
		sortField = "created_at"
	}

	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "desc"
	}

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM proxies %s", whereClause)
	var total int
	if err := r.db.Pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count proxies: %w", err)
	}

	// Get proxies
	offset := (page - 1) * limit
	query := fmt.Sprintf(`
		SELECT
			id, address, protocol, username, status, category, cost, country,
			requests, successful_requests, failed_requests,
			avg_response_time, last_check, created_at, updated_at
		FROM proxies
		%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, whereClause, sortField, sortOrder, argPos, argPos+1)

	args = append(args, limit, offset)

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list proxies: %w", err)
	}
	defer rows.Close()

	proxies := []models.ProxyWithStats{}
	for rows.Next() {
		var p models.Proxy
		err := rows.Scan(
			&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Status, &p.Category, &p.Cost, &p.Country,
			&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
			&p.AvgResponseTime, &p.LastCheck, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan proxy: %w", err)
		}

		// Calculate success rate
		successRate := 0.0
		if p.Requests > 0 {
			successRate = (float64(p.SuccessfulRequests) / float64(p.Requests)) * 100
		}

		proxies = append(proxies, models.ProxyWithStats{
			ID:              p.ID,
			Address:         p.Address,
			Protocol:        p.Protocol,
			Username:        p.Username,
			Status:          p.Status,
			Category:        p.Category,
			Cost:            p.Cost,
			Country:         p.Country,
			Requests:        p.Requests,
			SuccessRate:     successRate,
			AvgResponseTime: p.AvgResponseTime,
			LastCheck:       p.LastCheck,
			CreatedAt:       p.CreatedAt,
			UpdatedAt:       p.UpdatedAt,
		})
	}

	return proxies, total, nil
}

// GetByID retrieves a proxy by ID
func (r *ProxyRepository) GetByID(ctx context.Context, id int) (*models.Proxy, error) {
	if r.db.IsMongo() {
		var p proxyDoc
		err := r.db.MongoDB().Collection("proxies").FindOne(ctx, bson.M{"id": id}).Decode(&p)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return nil, nil
			}
			return nil, fmt.Errorf("failed to get proxy: %w", err)
		}
		return proxyDocToModel(p), nil
	}

	query := `
		SELECT
			id, address, protocol, username, password, status, category, cost, country,
			requests, successful_requests, failed_requests,
			avg_response_time, last_check, last_error, created_at, updated_at
		FROM proxies
		WHERE id = $1
	`

	var p models.Proxy
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status, &p.Category, &p.Cost, &p.Country,
		&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
		&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get proxy: %w", err)
	}

	return &p, nil
}

// Create creates a new proxy
func (r *ProxyRepository) Create(ctx context.Context, req models.CreateProxyRequest) (*models.Proxy, error) {
	if r.db.IsMongo() {
		nextID, err := r.nextProxyID(ctx)
		if err != nil {
			return nil, err
		}

		now := time.Now()
		doc := proxyDoc{
			ID:                 nextID,
			Address:            req.Address,
			Protocol:           req.Protocol,
			Username:           req.Username,
			Password:           req.Password,
			Status:             "idle",
			Category:           req.Category,
			Cost:               req.Cost,
			Country:            req.Country,
			Requests:           0,
			SuccessfulRequests: 0,
			FailedRequests:     0,
			AvgResponseTime:    0,
			CreatedAt:          now,
			UpdatedAt:          now,
		}

		_, err = r.db.MongoDB().Collection("proxies").InsertOne(ctx, doc)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return nil, fmt.Errorf("proxy with address %s and protocol %s already exists", req.Address, req.Protocol)
			}
			return nil, fmt.Errorf("failed to create proxy: %w", err)
		}

		return proxyDocToModel(doc), nil
	}

	query := `
		INSERT INTO proxies (address, protocol, username, password, category, cost, country)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, address, protocol, username, status, category, cost, country, created_at, updated_at
	`

	var p models.Proxy
	err := r.db.Pool.QueryRow(ctx, query, req.Address, req.Protocol, req.Username, req.Password, req.Category, req.Cost, req.Country).Scan(
		&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Status, &p.Category, &p.Cost, &p.Country, &p.CreatedAt, &p.UpdatedAt,
	)

	if err != nil {
		// Check if it's a unique constraint violation
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("proxy with address %s and protocol %s already exists", req.Address, req.Protocol)
		}
		return nil, fmt.Errorf("failed to create proxy: %w", err)
	}

	return &p, nil
}

// Update updates a proxy
func (r *ProxyRepository) Update(ctx context.Context, id int, req models.UpdateProxyRequest) (*models.Proxy, error) {
	if r.db.IsMongo() {
		set := bson.M{"updated_at": time.Now()}
		if req.Address != "" {
			set["address"] = req.Address
		}
		if req.Protocol != "" {
			set["protocol"] = req.Protocol
		}
		set["username"] = req.Username
		set["password"] = req.Password
		if req.Category != nil {
			set["category"] = req.Category
		}
		if req.Cost != nil {
			set["cost"] = req.Cost
		}
		if req.Country != nil {
			set["country"] = req.Country
		}

		opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
		var updated proxyDoc
		err := r.db.MongoDB().Collection("proxies").FindOneAndUpdate(
			ctx,
			bson.M{"id": id},
			bson.M{"$set": set},
			opts,
		).Decode(&updated)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return nil, nil
			}
			return nil, fmt.Errorf("failed to update proxy: %w", err)
		}

		return proxyDocToModel(updated), nil
	}

	query := `
		UPDATE proxies
		SET address = COALESCE(NULLIF($1, ''), address),
		    protocol = COALESCE(NULLIF($2, ''), protocol),
		    username = $3,
		    password = $4,
		    category = COALESCE($5, category),
		    cost = COALESCE($6, cost),
		    country = COALESCE($7, country),
		    updated_at = NOW()
		WHERE id = $8
		RETURNING id, address, protocol, status, category, cost, country, updated_at
	`

	var p models.Proxy
	err := r.db.Pool.QueryRow(ctx, query, req.Address, req.Protocol, req.Username, req.Password, req.Category, req.Cost, req.Country, id).Scan(
		&p.ID, &p.Address, &p.Protocol, &p.Status, &p.Category, &p.Cost, &p.Country, &p.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to update proxy: %w", err)
	}

	return &p, nil
}

// Delete deletes a proxy by ID
func (r *ProxyRepository) Delete(ctx context.Context, id int) error {
	if r.db.IsMongo() {
		_, err := r.db.MongoDB().Collection("proxies").DeleteOne(ctx, bson.M{"id": id})
		if err != nil {
			return fmt.Errorf("failed to delete proxy: %w", err)
		}
		return nil
	}

	query := `DELETE FROM proxies WHERE id = $1`
	_, err := r.db.Pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete proxy: %w", err)
	}
	return nil
}

// BulkDelete deletes multiple proxies
func (r *ProxyRepository) BulkDelete(ctx context.Context, ids []int) (int, error) {
	if r.db.IsMongo() {
		result, err := r.db.MongoDB().Collection("proxies").DeleteMany(ctx, bson.M{"id": bson.M{"$in": ids}})
		if err != nil {
			return 0, fmt.Errorf("failed to bulk delete proxies: %w", err)
		}
		return int(result.DeletedCount), nil
	}

	query := `DELETE FROM proxies WHERE id = ANY($1)`
	result, err := r.db.Pool.Exec(ctx, query, ids)
	if err != nil {
		return 0, fmt.Errorf("failed to bulk delete proxies: %w", err)
	}
	return int(result.RowsAffected()), nil
}

// GetStats retrieves overall proxy statistics
func (r *ProxyRepository) GetStats(ctx context.Context) (map[string]interface{}, error) {
	if r.db.IsMongo() {
		cursor, err := r.db.MongoDB().Collection("proxies").Find(ctx, bson.M{})
		if err != nil {
			return nil, fmt.Errorf("failed to get stats: %w", err)
		}
		defer cursor.Close(ctx)

		var total, active, failed, idle int
		var totalRequests int64
		var avgSum int64
		for cursor.Next(ctx) {
			var p proxyDoc
			if err := cursor.Decode(&p); err != nil {
				return nil, fmt.Errorf("failed to decode proxy: %w", err)
			}
			total++
			switch p.Status {
			case "active":
				active++
			case "failed":
				failed++
			default:
				idle++
			}
			totalRequests += p.Requests
			avgSum += int64(p.AvgResponseTime)
		}
		avg := 0
		if total > 0 {
			avg = int(avgSum / int64(total))
		}

		return map[string]interface{}{
			"total":             total,
			"active":            active,
			"failed":            failed,
			"idle":              idle,
			"total_requests":    totalRequests,
			"avg_response_time": avg,
		}, nil
	}

	query := `
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE status = 'active') as active,
			COUNT(*) FILTER (WHERE status = 'failed') as failed,
			COUNT(*) FILTER (WHERE status = 'idle') as idle,
			COALESCE(SUM(requests), 0) as total_requests,
			COALESCE(AVG(avg_response_time), 0) as avg_response_time
		FROM proxies
	`

	var stats struct {
		Total           int
		Active          int
		Failed          int
		Idle            int
		TotalRequests   int64
		AvgResponseTime float64
	}

	err := r.db.Pool.QueryRow(ctx, query).Scan(
		&stats.Total, &stats.Active, &stats.Failed, &stats.Idle,
		&stats.TotalRequests, &stats.AvgResponseTime,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	return map[string]interface{}{
		"total":             stats.Total,
		"active":            stats.Active,
		"failed":            stats.Failed,
		"idle":              stats.Idle,
		"total_requests":    stats.TotalRequests,
		"avg_response_time": int(stats.AvgResponseTime),
	}, nil
}

// GetAllActive retrieves all active proxies
func (r *ProxyRepository) GetAllActive(ctx context.Context) ([]models.ProxyStatusSimple, error) {
	if r.db.IsMongo() {
		cursor, err := r.db.MongoDB().Collection("proxies").Find(
			ctx,
			bson.M{"status": "active"},
			options.Find().SetSort(bson.D{{Key: "address", Value: 1}}),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to get active proxies: %w", err)
		}
		defer cursor.Close(ctx)

		proxies := []models.ProxyStatusSimple{}
		for cursor.Next(ctx) {
			var p proxyDoc
			if err := cursor.Decode(&p); err != nil {
				return nil, fmt.Errorf("failed to decode proxy: %w", err)
			}

			successRate := 0.0
			if p.Requests > 0 {
				successRate = (float64(p.SuccessfulRequests) / float64(p.Requests)) * 100
			}

			proxies = append(proxies, models.ProxyStatusSimple{
				ID:          fmt.Sprintf("%d", p.ID),
				Address:     p.Address,
				Status:      p.Status,
				Requests:    p.Requests,
				SuccessRate: successRate,
			})
		}
		return proxies, nil
	}

	query := `
		SELECT
			id, address, status, requests,
			successful_requests, failed_requests
		FROM proxies
		WHERE status = 'active'
		ORDER BY address
	`

	rows, err := r.db.Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active proxies: %w", err)
	}
	defer rows.Close()

	proxies := []models.ProxyStatusSimple{}
	for rows.Next() {
		var p struct {
			ID                 int
			Address            string
			Status             string
			Requests           int64
			SuccessfulRequests int64
			FailedRequests     int64
		}

		err := rows.Scan(&p.ID, &p.Address, &p.Status, &p.Requests, &p.SuccessfulRequests, &p.FailedRequests)
		if err != nil {
			return nil, fmt.Errorf("failed to scan proxy: %w", err)
		}

		successRate := 0.0
		if p.Requests > 0 {
			successRate = (float64(p.SuccessfulRequests) / float64(p.Requests)) * 100
		}

		proxies = append(proxies, models.ProxyStatusSimple{
			ID:          fmt.Sprintf("%d", p.ID),
			Address:     p.Address,
			Status:      p.Status,
			Requests:    p.Requests,
			SuccessRate: successRate,
		})
	}

	return proxies, nil
}

func (r *ProxyRepository) GetForSelection(ctx context.Context, includeFailed bool) ([]*models.Proxy, error) {
	if !r.db.IsMongo() {
		statuses := []string{"active", "idle"}
		if includeFailed {
			statuses = append(statuses, "failed")
		}
		query := `
			SELECT
				id, address, protocol, username, password, status, category, cost, country,
				requests, successful_requests, failed_requests,
				avg_response_time, last_check, last_error, created_at, updated_at
			FROM proxies
			WHERE status = ANY($1)
			ORDER BY address
		`
		rows, err := r.db.Pool.Query(ctx, query, statuses)
		if err != nil {
			return nil, fmt.Errorf("failed to load proxies: %w", err)
		}
		defer rows.Close()

		out := make([]*models.Proxy, 0)
		for rows.Next() {
			var p models.Proxy
			if err := rows.Scan(
				&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status, &p.Category, &p.Cost, &p.Country,
				&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
				&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
			); err != nil {
				return nil, fmt.Errorf("failed to scan proxy: %w", err)
			}
			out = append(out, &p)
		}
		return out, nil
	}

	filter := bson.M{"status": bson.M{"$in": []string{"active", "idle"}}}
	if includeFailed {
		filter = bson.M{}
	}
	cursor, err := r.db.MongoDB().Collection("proxies").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "address", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to load proxies: %w", err)
	}
	defer cursor.Close(ctx)

	out := make([]*models.Proxy, 0)
	for cursor.Next(ctx) {
		var p proxyDoc
		if err := cursor.Decode(&p); err != nil {
			return nil, fmt.Errorf("failed to decode proxy: %w", err)
		}
		out = append(out, proxyDocToModel(p))
	}
	return out, nil
}

func (r *ProxyRepository) nextProxyID(ctx context.Context) (int, error) {
	var p proxyDoc
	err := r.db.MongoDB().Collection("proxies").
		FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "id", Value: -1}})).
		Decode(&p)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return 1, nil
		}
		return 0, fmt.Errorf("failed to get next proxy id: %w", err)
	}
	return p.ID + 1, nil
}

func proxyDocToModel(p proxyDoc) *models.Proxy {
	return &models.Proxy{
		ID:                 p.ID,
		Address:            p.Address,
		Protocol:           p.Protocol,
		Username:           p.Username,
		Password:           p.Password,
		Status:             p.Status,
		Category:           p.Category,
		Cost:               p.Cost,
		Country:            p.Country,
		Requests:           p.Requests,
		SuccessfulRequests: p.SuccessfulRequests,
		FailedRequests:     p.FailedRequests,
		AvgResponseTime:    p.AvgResponseTime,
		LastCheck:          p.LastCheck,
		LastError:          p.LastError,
		CreatedAt:          p.CreatedAt,
		UpdatedAt:          p.UpdatedAt,
	}
}

// ListWithoutCountry returns proxies (id, address) that have no country yet.
// Used by the background geo detector. When force=true, returns every proxy
// regardless of whether country is set.
func (r *ProxyRepository) ListWithoutCountry(ctx context.Context, force bool, limit int) ([]models.Proxy, error) {
	if limit <= 0 {
		limit = 200
	}
	if r.db.IsMongo() {
		filter := bson.M{}
		if !force {
			filter["$or"] = []bson.M{
				{"country": bson.M{"$exists": false}},
				{"country": nil},
				{"country": ""},
			}
		}
		cur, err := r.db.MongoDB().Collection("proxies").Find(
			ctx,
			filter,
			options.Find().SetLimit(int64(limit)),
		)
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)

		out := make([]models.Proxy, 0, limit)
		for cur.Next(ctx) {
			var p proxyDoc
			if err := cur.Decode(&p); err != nil {
				return nil, err
			}
			out = append(out, models.Proxy{ID: p.ID, Address: p.Address})
		}
		return out, nil
	}

	q := `SELECT id, address FROM proxies`
	if !force {
		q += ` WHERE country IS NULL OR country = ''`
	}
	q += fmt.Sprintf(` ORDER BY id LIMIT %d`, limit)
	rows, err := r.db.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.Proxy, 0, limit)
	for rows.Next() {
		var p models.Proxy
		if err := rows.Scan(&p.ID, &p.Address); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// SetCountry persists the detected country on a proxy.
func (r *ProxyRepository) SetCountry(ctx context.Context, id int, country string) error {
	if r.db.IsMongo() {
		_, err := r.db.MongoDB().Collection("proxies").UpdateOne(ctx,
			bson.M{"id": id},
			bson.M{"$set": bson.M{"country": country, "updated_at": time.Now()}},
		)
		return err
	}
	_, err := r.db.Pool.Exec(ctx,
		`UPDATE proxies SET country = $1, updated_at = NOW() WHERE id = $2`,
		country, id,
	)
	return err
}

func (r *ProxyRepository) RecordProxyRequest(ctx context.Context, record map[string]any) error {
	if !r.db.IsMongo() {
		return nil
	}
	_, err := r.db.MongoDB().Collection("proxy_requests").InsertOne(ctx, record)
	return err
}

func (r *ProxyRepository) UpdateProxyAfterRequest(ctx context.Context, proxyID int, success bool, responseTime int, ts time.Time, errorMsg *string) error {
	if !r.db.IsMongo() {
		return nil
	}
	now := time.Now()
	if success {
		_, err := r.db.MongoDB().Collection("proxies").UpdateOne(ctx, bson.M{"id": proxyID}, bson.M{
			"$inc": bson.M{
				"requests":            1,
				"successful_requests": 1,
			},
			"$set": bson.M{
				"failed_requests":   0,
				"avg_response_time": responseTime,
				"last_check":        ts,
				"last_error":        nil,
				"status":            "active",
				"updated_at":        now,
			},
		})
		return err
	}

	var updated struct {
		FailedRequests int64 `bson:"failed_requests"`
	}
	err := r.db.MongoDB().Collection("proxies").FindOneAndUpdate(
		ctx,
		bson.M{"id": proxyID},
		bson.M{
			"$inc": bson.M{
				"requests":        1,
				"failed_requests": 1,
			},
			"$set": bson.M{
				"avg_response_time": responseTime,
				"last_check":        ts,
				"last_error":        errorMsg,
				"updated_at":        now,
			},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if err != nil {
		return err
	}

	if updated.FailedRequests >= 3 {
		_, err = r.db.MongoDB().Collection("proxies").UpdateOne(ctx, bson.M{"id": proxyID}, bson.M{
			"$set": bson.M{
				"status":     "failed",
				"updated_at": now,
			},
		})
	}
	return err
}

func (r *ProxyRepository) UpdateProxyStatusOnly(ctx context.Context, proxyID int, status string) error {
	if !r.db.IsMongo() {
		return nil
	}
	_, err := r.db.MongoDB().Collection("proxies").UpdateOne(ctx, bson.M{"id": proxyID}, bson.M{
		"$set": bson.M{"status": status, "updated_at": time.Now()},
	})
	return err
}

func (r *ProxyRepository) RecordHealthCheckResult(ctx context.Context, proxyID int, success bool, ts time.Time, errorMsg string) error {
	if !r.db.IsMongo() {
		return nil
	}
	now := time.Now()
	if success {
		_, err := r.db.MongoDB().Collection("proxies").UpdateOne(ctx, bson.M{"id": proxyID}, bson.M{
			"$set": bson.M{
				"last_check":      ts,
				"last_error":      nil,
				"failed_requests": 0,
				"status":          "active",
				"updated_at":      now,
			},
		})
		return err
	}

	var updated struct {
		FailedRequests int64 `bson:"failed_requests"`
	}
	err := r.db.MongoDB().Collection("proxies").FindOneAndUpdate(
		ctx,
		bson.M{"id": proxyID},
		bson.M{
			"$inc": bson.M{"failed_requests": 1},
			"$set": bson.M{
				"last_check": ts,
				"last_error": &errorMsg,
				"updated_at": now,
			},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if err != nil {
		return err
	}

	if updated.FailedRequests >= 3 {
		_, err = r.db.MongoDB().Collection("proxies").UpdateOne(ctx, bson.M{"id": proxyID}, bson.M{
			"$set": bson.M{
				"status":     "failed",
				"updated_at": now,
			},
		})
	}
	return err
}

func (r *ProxyRepository) GetRecentRequestsMongo(ctx context.Context, proxyID int, limit int) ([]map[string]any, error) {
	cursor, err := r.db.MongoDB().Collection("proxy_requests").Find(
		ctx,
		bson.M{"proxy_id": proxyID},
		options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	out := make([]map[string]any, 0, limit)
	for cursor.Next(ctx) {
		var m map[string]any
		if err := cursor.Decode(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, _ := out[i]["timestamp"].(time.Time)
		tj, _ := out[j]["timestamp"].(time.Time)
		return ti.After(tj)
	})
	return out, nil
}
