package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// LogRepository handles log database operations
type LogRepository struct {
	db *database.DB
}

var lastLogID atomic.Int64

// NewLogRepository creates a new LogRepository
func NewLogRepository(db *database.DB) *LogRepository {
	return &LogRepository{db: db}
}

// Create creates a new log entry
func (r *LogRepository) Create(ctx context.Context, level, message string, details *string, metadata map[string]any) error {
	if r.db.IsMongo() {
		nextID := nextRuntimeLogID()
		_, err := r.db.MongoDB().Collection("logs").InsertOne(ctx, bson.M{
			"id":        nextID,
			"timestamp": time.Now(),
			"level":     level,
			"message":   message,
			"details":   details,
			"metadata":  metadata,
		})
		if err != nil {
			return fmt.Errorf("failed to create log: %w", err)
		}
		return nil
	}

	query := `
		INSERT INTO logs (timestamp, level, message, details, metadata)
		VALUES ($1, $2, $3, $4, $5)
	`

	var metadataJSON []byte
	var err error
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	_, err = r.db.Pool.Exec(ctx, query, time.Now(), level, message, details, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to create log: %w", err)
	}

	return nil
}

// List retrieves logs with pagination and filters
func (r *LogRepository) List(ctx context.Context, page, limit int, level, search, source string, startTime, endTime *time.Time) ([]models.Log, int, error) {
	if r.db.IsMongo() {
		filter := bson.M{}
		if level != "" {
			filter["level"] = level
		}
		if search != "" {
			filter["message"] = bson.M{"$regex": search, "$options": "i"}
		}
		if source != "" {
			filter["metadata.source"] = source
		}
		if startTime != nil || endTime != nil {
			ts := bson.M{}
			if startTime != nil {
				ts["$gte"] = *startTime
			}
			if endTime != nil {
				ts["$lte"] = *endTime
			}
			filter["timestamp"] = ts
		}

		total64, err := r.db.MongoDB().Collection("logs").CountDocuments(ctx, filter)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count logs: %w", err)
		}

		offset := (page - 1) * limit
		cursor, err := r.db.MongoDB().Collection("logs").Find(
			ctx,
			filter,
			options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetSkip(int64(offset)).SetLimit(int64(limit)),
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to list logs: %w", err)
		}
		defer cursor.Close(ctx)

		logs := []models.Log{}
		for cursor.Next(ctx) {
			var doc struct {
				ID        int64          `bson:"id"`
				Timestamp time.Time      `bson:"timestamp"`
				Level     string         `bson:"level"`
				Message   string         `bson:"message"`
				Details   *string        `bson:"details"`
				Metadata  map[string]any `bson:"metadata"`
			}
			if err := cursor.Decode(&doc); err != nil {
				return nil, 0, fmt.Errorf("failed to decode log: %w", err)
			}
			logs = append(logs, models.Log{
				ID:        doc.ID,
				Timestamp: doc.Timestamp,
				Level:     doc.Level,
				Message:   doc.Message,
				Details:   doc.Details,
				Metadata:  doc.Metadata,
			})
		}
		return logs, int(total64), nil
	}

	// Build WHERE clause
	whereClauses := []string{}
	args := []any{}
	argPos := 1

	if level != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("level = $%d", argPos))
		args = append(args, level)
		argPos++
	}

	if search != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("message ILIKE $%d", argPos))
		args = append(args, "%"+search+"%")
		argPos++
	}

	if source != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("metadata->>'source' = $%d", argPos))
		args = append(args, source)
		argPos++
	}

	if startTime != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("timestamp >= $%d", argPos))
		args = append(args, *startTime)
		argPos++
	}

	if endTime != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("timestamp <= $%d", argPos))
		args = append(args, *endTime)
		argPos++
	}

	whereClause := ""
	if len(whereClauses) > 0 {
		whereClause = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM logs %s", whereClause)
	var total int
	if err := r.db.Pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count logs: %w", err)
	}

	// Get logs
	offset := (page - 1) * limit
	query := fmt.Sprintf(`
		SELECT id, timestamp, level, message, details, metadata
		FROM logs
		%s
		ORDER BY timestamp DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argPos, argPos+1)

	args = append(args, limit, offset)

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list logs: %w", err)
	}
	defer rows.Close()

	logs := []models.Log{}
	for rows.Next() {
		var l models.Log
		var metadataJSON []byte

		err := rows.Scan(&l.ID, &l.Timestamp, &l.Level, &l.Message, &l.Details, &metadataJSON)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan log: %w", err)
		}

		if metadataJSON != nil {
			if err := json.Unmarshal(metadataJSON, &l.Metadata); err != nil {
				return nil, 0, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		logs = append(logs, l)
	}

	return logs, total, nil
}

// GetNewLogs retrieves logs with ID greater than lastID for streaming
func (r *LogRepository) GetNewLogs(ctx context.Context, lastID int64, limit int, source string) ([]models.Log, int, error) {
	if r.db.IsMongo() {
		filter := bson.M{"id": bson.M{"$gt": lastID}}
		if source != "" {
			filter["metadata.source"] = source
		}

		total64, err := r.db.MongoDB().Collection("logs").CountDocuments(ctx, filter)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count logs: %w", err)
		}

		cursor, err := r.db.MongoDB().Collection("logs").Find(
			ctx,
			filter,
			options.Find().SetSort(bson.D{{Key: "id", Value: 1}}).SetLimit(int64(limit)),
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to list logs: %w", err)
		}
		defer cursor.Close(ctx)

		logs := []models.Log{}
		for cursor.Next(ctx) {
			var doc struct {
				ID        int64          `bson:"id"`
				Timestamp time.Time      `bson:"timestamp"`
				Level     string         `bson:"level"`
				Message   string         `bson:"message"`
				Details   *string        `bson:"details"`
				Metadata  map[string]any `bson:"metadata"`
			}
			if err := cursor.Decode(&doc); err != nil {
				return nil, 0, fmt.Errorf("failed to decode log: %w", err)
			}
			logs = append(logs, models.Log{
				ID:        doc.ID,
				Timestamp: doc.Timestamp,
				Level:     doc.Level,
				Message:   doc.Message,
				Details:   doc.Details,
				Metadata:  doc.Metadata,
			})
		}
		return logs, int(total64), nil
	}

	// Build WHERE clause
	whereClauses := []string{fmt.Sprintf("id > $1")}
	args := []any{lastID}
	argPos := 2

	if source != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("metadata->>'source' = $%d", argPos))
		args = append(args, source)
		argPos++
	}

	whereClause := "WHERE " + strings.Join(whereClauses, " AND ")

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM logs %s", whereClause)
	var total int
	if err := r.db.Pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count logs: %w", err)
	}

	// Get logs ordered by ID ascending to get them in chronological order
	query := fmt.Sprintf(`
		SELECT id, timestamp, level, message, details, metadata
		FROM logs
		%s
		ORDER BY id ASC
		LIMIT $%d
	`, whereClause, argPos)

	args = append(args, limit)

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list logs: %w", err)
	}
	defer rows.Close()

	logs := []models.Log{}
	for rows.Next() {
		var l models.Log
		var metadataJSON []byte

		err := rows.Scan(&l.ID, &l.Timestamp, &l.Level, &l.Message, &l.Details, &metadataJSON)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan log: %w", err)
		}

		if metadataJSON != nil {
			if err := json.Unmarshal(metadataJSON, &l.Metadata); err != nil {
				return nil, 0, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		logs = append(logs, l)
	}

	return logs, total, nil
}

// DeleteOlderThan deletes logs older than the specified duration
func (r *LogRepository) DeleteOlderThan(ctx context.Context, duration time.Duration) (int64, error) {
	if r.db.IsMongo() {
		cutoff := time.Now().Add(-duration)
		result, err := r.db.MongoDB().Collection("logs").DeleteMany(ctx, bson.M{"timestamp": bson.M{"$lt": cutoff}})
		if err != nil {
			return 0, fmt.Errorf("failed to delete old logs: %w", err)
		}
		return result.DeletedCount, nil
	}

	query := `DELETE FROM logs WHERE timestamp < $1`
	cutoff := time.Now().Add(-duration)

	result, err := r.db.Pool.Exec(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old logs: %w", err)
	}

	return result.RowsAffected(), nil
}

func nextRuntimeLogID() int64 {
	now := time.Now().UnixNano()
	for {
		prev := lastLogID.Load()
		next := now
		if prev >= now {
			next = prev + 1
		}
		if lastLogID.CompareAndSwap(prev, next) {
			return next
		}
	}
}
