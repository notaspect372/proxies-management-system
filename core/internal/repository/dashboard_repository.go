package repository

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DashboardRepository handles dashboard statistics operations
type DashboardRepository struct {
	db *database.DB
}

// NewDashboardRepository creates a new DashboardRepository
func NewDashboardRepository(db *database.DB) *DashboardRepository {
	return &DashboardRepository{db: db}
}

// GetStats retrieves overall dashboard statistics
func (r *DashboardRepository) GetStats(ctx context.Context) (*models.DashboardStats, error) {
	if r.db.IsMongo() {
		return r.getStatsMongo(ctx)
	}

	query := `
		WITH current_stats AS (
			SELECT
				COUNT(*) FILTER (WHERE status = 'active') as active_proxies,
				COUNT(*) as total_proxies,
				COALESCE(SUM(requests), 0) as total_requests,
				COALESCE(AVG(CASE WHEN requests > 0 THEN (successful_requests::float / requests * 100) END), 0) as avg_success_rate,
				COALESCE(AVG(avg_response_time), 0)::int as avg_response_time
			FROM proxies
		),
		yesterday_stats AS (
			SELECT
				COUNT(*) as requests_yesterday,
				COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) * 100, 0) as success_rate_yesterday,
				COALESCE(AVG(response_time), 0)::int as response_time_yesterday
			FROM proxy_requests
			WHERE timestamp >= NOW() - INTERVAL '2 days'
			  AND timestamp < NOW() - INTERVAL '1 day'
		),
		today_stats AS (
			SELECT
				COUNT(*) as requests_today,
				COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) * 100, 0) as success_rate_today,
				COALESCE(AVG(response_time), 0)::int as response_time_today
			FROM proxy_requests
			WHERE timestamp >= NOW() - INTERVAL '1 day'
		)
		SELECT
			c.active_proxies,
			c.total_proxies,
			c.total_requests,
			c.avg_success_rate,
			c.avg_response_time,
			CASE WHEN y.requests_yesterday > 0
				THEN ((t.requests_today - y.requests_yesterday)::float / y.requests_yesterday * 100)
				ELSE 0
			END as request_growth,
			(t.success_rate_today - y.success_rate_yesterday) as success_rate_growth,
			(t.response_time_today - y.response_time_yesterday) as response_time_delta
		FROM current_stats c, yesterday_stats y, today_stats t
	`

	var stats models.DashboardStats
	err := r.db.Pool.QueryRow(ctx, query).Scan(
		&stats.ActiveProxies,
		&stats.TotalProxies,
		&stats.TotalRequests,
		&stats.AvgSuccessRate,
		&stats.AvgResponseTime,
		&stats.RequestGrowth,
		&stats.SuccessRateGrowth,
		&stats.ResponseTimeDelta,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get dashboard stats: %w", err)
	}

	return &stats, nil
}

// GetResponseTimeChart retrieves response time chart data
func (r *DashboardRepository) GetResponseTimeChart(ctx context.Context, interval string) ([]models.ChartDataPoint, error) {
	if r.db.IsMongo() {
		return r.getResponseTimeChartMongo(ctx, interval)
	}

	// Determine time bucket based on interval
	bucketSize := "4 hours"
	lookback := "24 hours"

	switch interval {
	case "1h":
		bucketSize = "1 hour"
		lookback = "24 hours"
	case "4h":
		bucketSize = "4 hours"
		lookback = "24 hours"
	case "1d":
		bucketSize = "1 day"
		lookback = "7 days"
	}

	query := fmt.Sprintf(`
		SELECT
			time_bucket('%s', timestamp) as bucket,
			COALESCE(AVG(response_time), 0)::int as avg_response_time
		FROM proxy_requests
		WHERE timestamp >= NOW() - INTERVAL '%s'
		  AND success = true
		GROUP BY bucket
		ORDER BY bucket
	`, bucketSize, lookback)

	rows, err := r.db.Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get response time chart: %w", err)
	}
	defer rows.Close()

	data := []models.ChartDataPoint{}
	for rows.Next() {
		var bucket time.Time
		var value int

		if err := rows.Scan(&bucket, &value); err != nil {
			return nil, fmt.Errorf("failed to scan chart data: %w", err)
		}

		data = append(data, models.ChartDataPoint{
			Time:  bucket.Format("15:04"),
			Value: value,
		})
	}

	return data, nil
}

// GetSuccessRateChart retrieves success rate chart data
func (r *DashboardRepository) GetSuccessRateChart(ctx context.Context, interval string) ([]models.SuccessRateDataPoint, error) {
	if r.db.IsMongo() {
		return r.getSuccessRateChartMongo(ctx, interval)
	}

	// Determine time bucket based on interval
	bucketSize := "4 hours"
	lookback := "24 hours"

	switch interval {
	case "1h":
		bucketSize = "1 hour"
		lookback = "24 hours"
	case "4h":
		bucketSize = "4 hours"
		lookback = "24 hours"
	case "1d":
		bucketSize = "1 day"
		lookback = "7 days"
	}

	query := fmt.Sprintf(`
		SELECT
			time_bucket('%s', timestamp) as bucket,
			(COUNT(*) FILTER (WHERE success = true) * 100 / GREATEST(COUNT(*), 1))::int as success_rate,
			(COUNT(*) FILTER (WHERE success = false) * 100 / GREATEST(COUNT(*), 1))::int as failure_rate
		FROM proxy_requests
		WHERE timestamp >= NOW() - INTERVAL '%s'
		GROUP BY bucket
		ORDER BY bucket
	`, bucketSize, lookback)

	rows, err := r.db.Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get success rate chart: %w", err)
	}
	defer rows.Close()

	data := []models.SuccessRateDataPoint{}
	for rows.Next() {
		var bucket time.Time
		var success, failure int

		if err := rows.Scan(&bucket, &success, &failure); err != nil {
			return nil, fmt.Errorf("failed to scan chart data: %w", err)
		}

		data = append(data, models.SuccessRateDataPoint{
			Time:    bucket.Format("15:04"),
			Success: success,
			Failure: failure,
		})
	}

	return data, nil
}

func (r *DashboardRepository) getStatsMongo(ctx context.Context) (*models.DashboardStats, error) {
	proxiesCur, err := r.db.MongoDB().Collection("proxies").Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to load proxies: %w", err)
	}
	defer proxiesCur.Close(ctx)

	var stats models.DashboardStats
	var successRateSum float64
	for proxiesCur.Next(ctx) {
		var p struct {
			Status          string `bson:"status"`
			Requests        int64  `bson:"requests"`
			Successful      int64  `bson:"successful_requests"`
			AvgResponseTime int    `bson:"avg_response_time"`
		}
		if err := proxiesCur.Decode(&p); err != nil {
			return nil, fmt.Errorf("failed to decode proxy: %w", err)
		}
		stats.TotalProxies++
		if p.Status == "active" {
			stats.ActiveProxies++
		}
		stats.TotalRequests += p.Requests
		stats.AvgResponseTime += p.AvgResponseTime
		if p.Requests > 0 {
			successRateSum += (float64(p.Successful) / float64(p.Requests)) * 100
		}
	}
	if stats.TotalProxies > 0 {
		stats.AvgResponseTime = stats.AvgResponseTime / stats.TotalProxies
		stats.AvgSuccessRate = successRateSum / float64(stats.TotalProxies)
	}

	now := time.Now()
	todayStart := now.Add(-24 * time.Hour)
	yesterdayStart := now.Add(-48 * time.Hour)

	var todayCount, yesterdayCount int
	var todaySuccess, yesterdaySuccess int
	var todayRespTotal, yesterdayRespTotal int

	// Aggregate server-side: bucket the last 48h into today / yesterday
	// windows and return one summary row per bucket. Previously this loaded
	// every row into Go (200k+ rows on Atlas free tier = 60s+).
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"timestamp": bson.M{"$gte": yesterdayStart},
		}}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{"$cond": bson.A{
				bson.M{"$gte": bson.A{"$timestamp", todayStart}},
				"today",
				"yesterday",
			}},
			"count":     bson.M{"$sum": 1},
			"successes": bson.M{"$sum": bson.M{"$cond": bson.A{"$success", 1, 0}}},
			"resp_sum":  bson.M{"$sum": "$response_time"},
		}}},
	}
	reqCur, err := r.db.MongoDB().Collection("proxy_requests").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate requests: %w", err)
	}
	defer reqCur.Close(ctx)

	for reqCur.Next(ctx) {
		var bucket struct {
			ID        string `bson:"_id"`
			Count     int    `bson:"count"`
			Successes int    `bson:"successes"`
			RespSum   int    `bson:"resp_sum"`
		}
		if err := reqCur.Decode(&bucket); err != nil {
			return nil, fmt.Errorf("failed to decode stats bucket: %w", err)
		}
		switch bucket.ID {
		case "today":
			todayCount = bucket.Count
			todaySuccess = bucket.Successes
			todayRespTotal = bucket.RespSum
		case "yesterday":
			yesterdayCount = bucket.Count
			yesterdaySuccess = bucket.Successes
			yesterdayRespTotal = bucket.RespSum
		}
	}

	var todaySuccessRate, yesterdaySuccessRate float64
	var todayRespAvg, yesterdayRespAvg int
	if todayCount > 0 {
		todaySuccessRate = float64(todaySuccess) * 100 / float64(todayCount)
		todayRespAvg = todayRespTotal / todayCount
	}
	if yesterdayCount > 0 {
		yesterdaySuccessRate = float64(yesterdaySuccess) * 100 / float64(yesterdayCount)
		yesterdayRespAvg = yesterdayRespTotal / yesterdayCount
	}

	if yesterdayCount > 0 {
		stats.RequestGrowth = (float64(todayCount-yesterdayCount) / float64(yesterdayCount)) * 100
	}
	stats.SuccessRateGrowth = todaySuccessRate - yesterdaySuccessRate
	stats.ResponseTimeDelta = todayRespAvg - yesterdayRespAvg

	return &stats, nil
}

func (r *DashboardRepository) getResponseTimeChartMongo(ctx context.Context, interval string) ([]models.ChartDataPoint, error) {
	step, lookback := chartInterval(interval)
	start := time.Now().Add(-lookback)

	cur, err := r.db.MongoDB().Collection("proxy_requests").Find(ctx, bson.M{
		"timestamp": bson.M{"$gte": start},
		"success":   true,
	}, options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to load response time chart: %w", err)
	}
	defer cur.Close(ctx)

	type agg struct {
		sum   int
		count int
	}
	buckets := map[time.Time]agg{}
	for cur.Next(ctx) {
		var req struct {
			Timestamp    time.Time `bson:"timestamp"`
			ResponseTime int       `bson:"response_time"`
		}
		if err := cur.Decode(&req); err != nil {
			return nil, fmt.Errorf("failed to decode chart point: %w", err)
		}
		b := truncateTime(req.Timestamp, step)
		x := buckets[b]
		x.sum += req.ResponseTime
		x.count++
		buckets[b] = x
	}

	keys := make([]time.Time, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	out := make([]models.ChartDataPoint, 0, len(keys))
	for _, k := range keys {
		x := buckets[k]
		v := 0
		if x.count > 0 {
			v = x.sum / x.count
		}
		out = append(out, models.ChartDataPoint{Time: k.Format("15:04"), Value: v})
	}
	return out, nil
}

func (r *DashboardRepository) getSuccessRateChartMongo(ctx context.Context, interval string) ([]models.SuccessRateDataPoint, error) {
	step, lookback := chartInterval(interval)
	start := time.Now().Add(-lookback)

	cur, err := r.db.MongoDB().Collection("proxy_requests").Find(ctx, bson.M{
		"timestamp": bson.M{"$gte": start},
	}, options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to load success rate chart: %w", err)
	}
	defer cur.Close(ctx)

	type agg struct {
		total   int
		success int
	}
	buckets := map[time.Time]agg{}
	for cur.Next(ctx) {
		var req struct {
			Timestamp time.Time `bson:"timestamp"`
			Success   bool      `bson:"success"`
		}
		if err := cur.Decode(&req); err != nil {
			return nil, fmt.Errorf("failed to decode chart point: %w", err)
		}
		b := truncateTime(req.Timestamp, step)
		x := buckets[b]
		x.total++
		if req.Success {
			x.success++
		}
		buckets[b] = x
	}

	keys := make([]time.Time, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	out := make([]models.SuccessRateDataPoint, 0, len(keys))
	for _, k := range keys {
		x := buckets[k]
		success := 0
		failure := 0
		if x.total > 0 {
			success = (x.success * 100) / x.total
			failure = 100 - success
		}
		out = append(out, models.SuccessRateDataPoint{
			Time:    k.Format("15:04"),
			Success: success,
			Failure: failure,
		})
	}
	return out, nil
}

func chartInterval(interval string) (time.Duration, time.Duration) {
	switch interval {
	case "1h":
		return time.Hour, 24 * time.Hour
	case "1d":
		return 24 * time.Hour, 7 * 24 * time.Hour
	default:
		return 4 * time.Hour, 24 * time.Hour
	}
}

func truncateTime(t time.Time, step time.Duration) time.Time {
	return t.UTC().Truncate(step)
}
