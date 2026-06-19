package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all application configuration
type Config struct {
	ProxyPort int
	APIPort   int
	LogLevel  string
	Database  DatabaseConfig
	AdminUser string
	AdminPass string

	// Per-machine routing defaults. When set, the proxy port treats any
	// request without a Proxy-Authorization header as if it carried these
	// routing hints. Lets scrapers point to plain `localhost:8006` instead
	// of `machine_id:country@localhost:8006` — the machine identity is
	// configured once in this machine's `.env` file.
	RoutingDefaultMachine string
	RoutingDefaultCountry string

	// Per-country aux listeners. Each entry opens a dedicated port that
	// injects the (RoutingDefaultMachine, Country) routing credentials and
	// forwards to the main proxy port. Lets scrapers say
	// `proxy="localhost:8011"` for India, `proxy="localhost:8012"` for
	// Taiwan, etc. — works with clients (Chrome under SB UC mode) that
	// won't send Proxy-Authorization preemptively.
	//
	// Configured via the `AUX_LISTENERS` env var:
	//   AUX_LISTENERS=India:8011,Taiwan:8012,United States:8013
	//
	// Each entry may also override the machine for that listener using the
	// `machine_id/country:port` form, which is handy when one Rota instance
	// fronts scrapers running as different fleet machines:
	//   AUX_LISTENERS=Greece:8015,mini_pc_03/Greece:8016
	// Entries with no `machine_id/` prefix fall back to RoutingDefaultMachine.
	AuxListeners []AuxListenerConfig

	// Sheet-managed aux listeners, parsed from `AUX_LISTENERS_SHEET`. Same
	// format as `AUX_LISTENERS` but rewritten by the dashboard's Aux
	// Listeners CRUD page. Kept separate so manual entries in
	// `AUX_LISTENERS` are never overwritten. The startup wiring concatenates
	// both lists.
	AuxListenersSheet []AuxListenerConfig

	// Bind address for aux listeners. Defaults to `127.0.0.1` so listeners
	// are reachable only from the local box. Set to `0.0.0.0` (LAN-wide) or a
	// specific NIC IP when scrapers run on remote machines. Configured via
	// `AUX_LISTEN_ADDR`.
	AuxListenAddr string

	// Absolute path of the .env file the server loaded. Used by the Aux
	// Listeners page to rewrite `AUX_LISTENERS_SHEET` in place. Empty when
	// no .env file was found at startup (the mutation endpoints will
	// refuse to write in that case).
	EnvFilePath string
}

// AuxListenerConfig describes one (machine, country)→port aux listener.
// MachineID is empty when the listener should use RoutingDefaultMachine.
type AuxListenerConfig struct {
	MachineID string
	Country   string
	Port      int
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	Driver   string
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
	MongoURI string
	MongoDB  string
}

// DSN returns the database connection string
func (d *DatabaseConfig) DSN() string {
	if d.Driver == "mongo" {
		return d.MongoURI
	}

	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// Load reads configuration from environment variables
func Load() (*Config, error) {
	envPath := loadDotEnv()

	cfg := &Config{
		ProxyPort: getEnvAsInt("PROXY_PORT", 8000),
		APIPort:   getEnvAsInt("API_PORT", 8001),
		LogLevel:  getEnv("LOG_LEVEL", "info"),
		Database: DatabaseConfig{
			Driver:   getEnv("DB_DRIVER", "postgres"),
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnvAsInt("DB_PORT", 5432),
			User:     getEnv("DB_USER", "rota"),
			Password: getEnv("DB_PASSWORD", "rota_password"),
			Name:     getEnv("DB_NAME", "rota"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
			MongoURI: getEnv("MONGO_URI", ""),
			MongoDB:  getEnv("MONGO_DB", "rota"),
		},
		AdminUser:             getEnv("ROTA_ADMIN_USER", "admin"),
		AdminPass:             getEnv("ROTA_ADMIN_PASSWORD", "admin"),
		RoutingDefaultMachine: getEnv("ROUTING_DEFAULT_MACHINE", ""),
		RoutingDefaultCountry: getEnv("ROUTING_DEFAULT_COUNTRY", ""),
		AuxListeners:          parseAuxListeners(getEnv("AUX_LISTENERS", "")),
		AuxListenersSheet:     parseAuxListeners(getEnv("AUX_LISTENERS_SHEET", "")),
		AuxListenAddr:         getEnv("AUX_LISTEN_ADDR", "127.0.0.1"),
		EnvFilePath:           envPath,
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.ProxyPort < 1 || c.ProxyPort > 65535 {
		return fmt.Errorf("invalid proxy port: %d", c.ProxyPort)
	}
	if c.APIPort < 1 || c.APIPort > 65535 {
		return fmt.Errorf("invalid API port: %d", c.APIPort)
	}
	if c.ProxyPort == c.APIPort {
		return fmt.Errorf("proxy port and API port cannot be the same: %d", c.ProxyPort)
	}

	validLogLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", c.LogLevel)
	}

	if c.Database.Driver != "postgres" && c.Database.Driver != "mongo" {
		return fmt.Errorf("invalid DB_DRIVER: %s (must be postgres or mongo)", c.Database.Driver)
	}

	if c.Database.Driver == "mongo" && c.Database.MongoURI == "" {
		return fmt.Errorf("MONGO_URI is required when DB_DRIVER=mongo")
	}

	return nil
}

// loadDotEnv loads variables from a .env file without overriding any that
// are already set in the real environment. It searches the current working
// directory and walks up a few levels so it works whether the binary is run
// from the repo root or from inside core/. Returns the absolute path of the
// file actually loaded so the listener-sync feature can rewrite it later;
// returns "" when no .env was found.
func loadDotEnv() string {
	candidates := []string{
		".env",
		filepath.Join("core", ".env"),
		filepath.Join("..", ".env"),
		filepath.Join("..", "..", ".env"),
		filepath.Join("..", "..", "..", ".env"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			_ = godotenv.Load(path)
			if abs, err := filepath.Abs(path); err == nil {
				return abs
			}
			return path
		}
	}
	return ""
}

// parseAuxListeners turns a comma-separated list into structured configs.
// Each entry has the form `[machine_id/]country:port`. The machine_id prefix
// is optional — when omitted, the listener falls back to RoutingDefaultMachine
// at startup. Whitespace around fields is trimmed; bad entries are skipped
// with a stderr warning so a typo in one entry doesn't kill startup.
func parseAuxListeners(raw string) []AuxListenerConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make([]AuxListenerConfig, 0)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		i := strings.LastIndex(entry, ":")
		if i <= 0 || i == len(entry)-1 {
			fmt.Fprintf(os.Stderr, "AUX_LISTENERS: skipping malformed entry %q (want [machine_id/]country:port)\n", entry)
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(entry[i+1:]))
		if err != nil || port < 1 || port > 65535 {
			fmt.Fprintf(os.Stderr, "AUX_LISTENERS: skipping %q — bad port\n", entry)
			continue
		}
		left := strings.TrimSpace(entry[:i])
		machineID := ""
		country := left
		if slash := strings.Index(left, "/"); slash >= 0 {
			machineID = strings.TrimSpace(left[:slash])
			country = strings.TrimSpace(left[slash+1:])
		}
		if country == "" {
			fmt.Fprintf(os.Stderr, "AUX_LISTENERS: skipping %q — empty country\n", entry)
			continue
		}
		out = append(out, AuxListenerConfig{MachineID: machineID, Country: country, Port: port})
	}
	return out
}

// getEnv retrieves an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAsInt retrieves an environment variable as an integer or returns a default value
func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
