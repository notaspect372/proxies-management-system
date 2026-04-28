package database

import (
	"context"
	"fmt"
	"time"

	"github.com/alpkeskin/rota/core/internal/config"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DB wraps the database connection pool
type DB struct {
	Driver string
	Pool   *pgxpool.Pool
	Mongo  *mongo.Client
	DBName string
	logger *logger.Logger
}

// Config holds database pool configuration
type Config struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	ConnectTimeout    time.Duration
}

// DefaultConfig returns default database pool configuration
func DefaultConfig() *Config {
	return &Config{
		MaxConns:          50,
		MinConns:          5,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: time.Minute,
		ConnectTimeout:    10 * time.Second,
	}
}

// New creates a new database connection pool
func New(ctx context.Context, cfg *config.DatabaseConfig, poolCfg *Config, log *logger.Logger) (*DB, error) {
	if cfg.Driver == "mongo" {
		return newMongo(ctx, cfg, poolCfg, log)
	}

	if poolCfg == nil {
		poolCfg = DefaultConfig()
	}

	// Build connection string
	dsn := cfg.DSN()

	// Parse pool config
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	// Set pool configuration
	poolConfig.MaxConns = poolCfg.MaxConns
	poolConfig.MinConns = poolCfg.MinConns
	poolConfig.MaxConnLifetime = poolCfg.MaxConnLifetime
	poolConfig.MaxConnIdleTime = poolCfg.MaxConnIdleTime
	poolConfig.HealthCheckPeriod = poolCfg.HealthCheckPeriod

	// Set connect timeout
	connectCtx, cancel := context.WithTimeout(ctx, poolCfg.ConnectTimeout)
	defer cancel()

	// Create connection pool
	pool, err := pgxpool.NewWithConfig(connectCtx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	log.Info("database connection pool created",
		"host", cfg.Host,
		"port", cfg.Port,
		"database", cfg.Name,
		"max_conns", poolCfg.MaxConns,
		"min_conns", poolCfg.MinConns,
	)

	db := &DB{
		Pool:   pool,
		logger: log,
	}

	// Test connection
	if err := db.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	log.Info("database connection established successfully")

	return db, nil
}

func newMongo(ctx context.Context, cfg *config.DatabaseConfig, poolCfg *Config, log *logger.Logger) (*DB, error) {
	if poolCfg == nil {
		poolCfg = DefaultConfig()
	}

	connectCtx, cancel := context.WithTimeout(ctx, poolCfg.ConnectTimeout)
	defer cancel()

	client, err := mongo.Connect(connectCtx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	db := &DB{
		Driver: "mongo",
		Mongo:  client,
		DBName: cfg.MongoDB,
		logger: log,
	}

	if err := db.Ping(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	log.Info("mongodb connection established successfully",
		"database", cfg.MongoDB,
	)

	return db, nil
}

// Ping checks if the database is reachable
func (db *DB) Ping(ctx context.Context) error {
	if db.IsMongo() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := db.Mongo.Ping(ctx, nil); err != nil {
			return fmt.Errorf("mongodb ping failed: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := db.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}

	return nil
}

// Close closes the database connection pool
func (db *DB) Close() {
	if db.IsMongo() {
		db.logger.Info("closing mongodb connection")
		if err := db.Mongo.Disconnect(context.Background()); err != nil {
			db.logger.Warn("failed to close mongodb connection", "error", err)
		}
		return
	}

	db.logger.Info("closing database connection pool")
	db.Pool.Close()
}

// Stats returns pool statistics
func (db *DB) Stats() *pgxpool.Stat {
	if db.IsMongo() {
		return nil
	}
	return db.Pool.Stat()
}

// Health checks the database health and returns detailed information
func (db *DB) Health(ctx context.Context) (map[string]interface{}, error) {
	if db.IsMongo() {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		start := time.Now()
		if err := db.Mongo.Ping(ctx, nil); err != nil {
			return nil, fmt.Errorf("health check failed: %w", err)
		}
		pingDuration := time.Since(start)

		return map[string]interface{}{
			"status":           "healthy",
			"driver":           "mongo",
			"ping_duration_ms": pingDuration.Milliseconds(),
			"database":         db.DBName,
		}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Ping database
	start := time.Now()
	if err := db.Pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("health check failed: %w", err)
	}
	pingDuration := time.Since(start)

	// Get pool stats
	stats := db.Pool.Stat()

	health := map[string]interface{}{
		"status":                 "healthy",
		"ping_duration_ms":       pingDuration.Milliseconds(),
		"total_conns":            stats.TotalConns(),
		"acquired_conns":         stats.AcquiredConns(),
		"idle_conns":             stats.IdleConns(),
		"max_conns":              stats.MaxConns(),
		"acquire_count":          stats.AcquireCount(),
		"acquire_duration":       stats.AcquireDuration().String(),
		"empty_acquire_count":    stats.EmptyAcquireCount(),
		"canceled_acquire_count": stats.CanceledAcquireCount(),
	}

	return health, nil
}

func (db *DB) IsMongo() bool {
	return db != nil && db.Driver == "mongo"
}

func (db *DB) MongoDB() *mongo.Database {
	if db.Mongo == nil {
		return nil
	}
	return db.Mongo.Database(db.DBName)
}

// MigrateMongo ensures MongoDB indexes and base data.
func (db *DB) MigrateMongo(ctx context.Context) error {
	database := db.MongoDB()
	if database == nil {
		return fmt.Errorf("mongodb database is not initialized")
	}

	_, err := database.Collection("proxies").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "address", Value: 1}, {Key: "protocol", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "status", Value: 1}},
		},
	})
	if err != nil {
		return fmt.Errorf("failed creating proxies indexes: %w", err)
	}

	_, err = database.Collection("logs").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "timestamp", Value: -1}}},
		{Keys: bson.D{{Key: "metadata.source", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("failed creating logs indexes: %w", err)
	}

	_, err = database.Collection("proxy_requests").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "timestamp", Value: -1}}},
		{Keys: bson.D{{Key: "proxy_id", Value: 1}, {Key: "timestamp", Value: -1}}},
		{Keys: bson.D{{Key: "success", Value: 1}, {Key: "timestamp", Value: -1}}},
	})
	if err != nil {
		return fmt.Errorf("failed creating proxy_requests indexes: %w", err)
	}

	_, err = database.Collection("proxy_assignments").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "machine_id", Value: 1}, {Key: "domain", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "machine_id", Value: 1}}},
		{Keys: bson.D{{Key: "proxy_id", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("failed creating proxy_assignments indexes: %w", err)
	}

	return nil
}
