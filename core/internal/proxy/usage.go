package proxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alpkeskin/rota/core/internal/repository"
)

// UsageTracker tracks proxy usage and updates statistics
type UsageTracker struct {
	repo *repository.ProxyRepository
}

// NewUsageTracker creates a new usage tracker
func NewUsageTracker(repo *repository.ProxyRepository) *UsageTracker {
	return &UsageTracker{
		repo: repo,
	}
}

// RequestRecord represents a single proxy request
type RequestRecord struct {
	ProxyID      int
	ProxyAddress string
	RequestedURL string
	Method       string
	Success      bool
	ResponseTime int // milliseconds
	StatusCode   int
	ErrorMessage string
	Timestamp    time.Time
}

// RecordRequest records a proxy request and updates statistics
func (t *UsageTracker) RecordRequest(ctx context.Context, record RequestRecord) error {
	// Insert into proxy_requests hypertable
	if err := t.insertProxyRequest(ctx, record); err != nil {
		if shouldIgnoreUsageDBError(err) {
			return nil
		}
		return fmt.Errorf("failed to insert proxy request: %w", err)
	}

	// Update proxy statistics
	if err := t.updateProxyStats(ctx, record); err != nil {
		if shouldIgnoreUsageDBError(err) {
			return nil
		}
		return fmt.Errorf("failed to update proxy stats: %w", err)
	}

	return nil
}

// insertProxyRequest inserts a record into the proxy_requests hypertable
func (t *UsageTracker) insertProxyRequest(ctx context.Context, record RequestRecord) error {
	if t.repo.GetDB().IsMongo() {
		var statusCode *int
		if record.StatusCode > 0 {
			statusCode = &record.StatusCode
		}
		var errorMsg *string
		if record.ErrorMessage != "" {
			errorMsg = &record.ErrorMessage
		}
		return t.repo.RecordProxyRequest(ctx, map[string]any{
			"proxy_id":      record.ProxyID,
			"proxy_address": record.ProxyAddress,
			"method":        record.Method,
			"url":           record.RequestedURL,
			"status_code":   statusCode,
			"success":       record.Success,
			"response_time": record.ResponseTime,
			"error":         errorMsg,
			"timestamp":     record.Timestamp,
		})
	}

	query := `
		INSERT INTO proxy_requests (
			proxy_id, proxy_address, method, url, status_code, success, response_time, error, timestamp
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	var errorMsg *string
	if record.ErrorMessage != "" {
		errorMsg = &record.ErrorMessage
	}

	var statusCode *int
	if record.StatusCode > 0 {
		statusCode = &record.StatusCode
	}

	_, err := t.repo.GetDB().Pool.Exec(
		ctx,
		query,
		record.ProxyID,
		record.ProxyAddress,
		record.Method,
		record.RequestedURL,
		statusCode,
		record.Success,
		record.ResponseTime,
		errorMsg,
		record.Timestamp,
	)

	return err
}

// updateProxyStats updates proxy statistics in the proxies table
func (t *UsageTracker) updateProxyStats(ctx context.Context, record RequestRecord) error {
	if t.repo.GetDB().IsMongo() {
		var errorMsg *string
		if record.ErrorMessage != "" {
			errorMsg = &record.ErrorMessage
		}
		return t.repo.UpdateProxyAfterRequest(ctx, record.ProxyID, record.Success, record.ResponseTime, record.Timestamp, errorMsg)
	}

	// Use a single query to update all statistics atomically
	// Note: We calculate avg_response_time correctly by using current requests value before increment
	query := `
		UPDATE proxies
		SET
			requests = requests + 1,
			successful_requests = CASE
				WHEN $2 THEN successful_requests + 1
				ELSE successful_requests
			END,
			failed_requests = CASE
				WHEN $2 THEN 0  -- Reset consecutive failures on success
				ELSE failed_requests + 1
			END,
			avg_response_time = (
				CASE
					WHEN requests = 0 THEN $3
					ELSE ((avg_response_time * requests) + $3) / (requests + 1)
				END
			)::INTEGER,
			last_check = $4,
			last_error = CASE
				WHEN $2 THEN NULL  -- Clear error on success
				ELSE $5
			END,
			status = CASE
				WHEN $2 THEN 'active'  -- Success = active
				ELSE CASE
					WHEN (failed_requests + 1) >= 3 THEN 'failed'  -- 3 consecutive failures = failed
					ELSE status
				END
			END,
			updated_at = NOW()
		WHERE id = $1
	`

	var errorMsg *string
	if record.ErrorMessage != "" {
		errorMsg = &record.ErrorMessage
	}

	_, err := t.repo.GetDB().Pool.Exec(
		ctx,
		query,
		record.ProxyID,
		record.Success,
		record.ResponseTime,
		record.Timestamp,
		errorMsg,
	)

	return err
}

// UpdateProxyStatus updates only the status of a proxy
func (t *UsageTracker) UpdateProxyStatus(ctx context.Context, proxyID int, status string) error {
	if t.repo.GetDB().IsMongo() {
		return t.repo.UpdateProxyStatusOnly(ctx, proxyID, status)
	}

	query := `
		UPDATE proxies
		SET status = $1, updated_at = NOW()
		WHERE id = $2
	`

	_, err := t.repo.GetDB().Pool.Exec(ctx, query, status, proxyID)
	return err
}

// RecordHealthCheck records a health check result
func (t *UsageTracker) RecordHealthCheck(ctx context.Context, proxyID int, success bool, responseTime int, errorMsg string) error {
	if t.repo.GetDB().IsMongo() {
		return t.repo.RecordHealthCheckResult(ctx, proxyID, success, time.Now(), errorMsg)
	}

	now := time.Now()

	status := "active"
	if !success {
		// Check how many consecutive failures
		var failedRequests int64
		query := `SELECT failed_requests FROM proxies WHERE id = $1`
		if err := t.repo.GetDB().Pool.QueryRow(ctx, query, proxyID).Scan(&failedRequests); err != nil {
			return err
		}

		// Mark as failed after 3 consecutive failures
		if failedRequests >= 2 {
			status = "failed"
		}
	}

	query := `
		UPDATE proxies
		SET
			last_check = $1,
			last_error = $2,
			status = $3,
			updated_at = NOW()
		WHERE id = $4
	`

	var lastError *string
	if errorMsg != "" {
		lastError = &errorMsg
	}

	_, err := t.repo.GetDB().Pool.Exec(ctx, query, now, lastError, status, proxyID)
	return err
}

// GetRecentRequests retrieves recent requests for a proxy
func (t *UsageTracker) GetRecentRequests(ctx context.Context, proxyID int, limit int) ([]RequestRecord, error) {
	if t.repo.GetDB().IsMongo() {
		docs, err := t.repo.GetRecentRequestsMongo(ctx, proxyID, limit)
		if err != nil {
			return nil, err
		}
		records := make([]RequestRecord, 0, len(docs))
		for _, d := range docs {
			record := RequestRecord{ProxyID: proxyID}
			if v, ok := d["method"].(string); ok {
				record.Method = v
			}
			if v, ok := d["url"].(string); ok {
				record.RequestedURL = v
			}
			if v, ok := d["response_time"].(int32); ok {
				record.ResponseTime = int(v)
			} else if v, ok := d["response_time"].(int); ok {
				record.ResponseTime = v
			}
			if v, ok := d["error"].(string); ok {
				record.ErrorMessage = v
			}
			if v, ok := d["success"].(bool); ok {
				record.Success = v
			}
			if v, ok := d["timestamp"].(time.Time); ok {
				record.Timestamp = v
			}
			records = append(records, record)
		}
		return records, nil
	}

	query := `
		SELECT
			proxy_id, method, url, status, response_time,
			COALESCE(error_message, '') as error_message, timestamp
		FROM proxy_requests
		WHERE proxy_id = $1
		ORDER BY timestamp DESC
		LIMIT $2
	`

	rows, err := t.repo.GetDB().Pool.Query(ctx, query, proxyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]RequestRecord, 0, limit)
	for rows.Next() {
		var record RequestRecord
		var status string

		err := rows.Scan(
			&record.ProxyID,
			&record.Method,
			&record.RequestedURL,
			&status,
			&record.ResponseTime,
			&record.ErrorMessage,
			&record.Timestamp,
		)
		if err != nil {
			return nil, err
		}

		record.Success = (status == "success")
		records = append(records, record)
	}

	return records, nil
}

func shouldIgnoreUsageDBError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "closed connection pool") ||
		strings.Contains(msg, "client is disconnected") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "timed out while checking out a connection from connection pool")
}
