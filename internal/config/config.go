// Package config loads Phoenix runtime configuration from environment
// variables. The same binary runs locally and in any deployment (Docker, Fly,
// K8s) with zero code change — only env differs.
package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	Port              string
	DatabaseURL       string
	RedisURL          string
	JWTSecret         string
	JWTAccessTTL      time.Duration
	JWTRefreshTTL     time.Duration
	HeartbeatInterval time.Duration
	ReconnectWindow   time.Duration
}

// Load reads config from the environment, applying sane defaults for local dev.
// It returns an error only for values that cannot have a safe default.
func Load() (*Config, error) {
	c := &Config{
		Port:              env("PORT", "8080"),
		DatabaseURL:       env("DATABASE_URL", "postgres://phoenix:phoenix@127.0.0.1:55432/phoenix?sslmode=disable"),
		RedisURL:          env("REDIS_URL", "redis://127.0.0.1:63790/0"),
		JWTSecret:         env("JWT_SECRET", "dev-secret-change-me"),
		JWTAccessTTL:      envDur("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL:     envDur("JWT_REFRESH_TTL", 720*time.Hour),
		HeartbeatInterval: envDur("HEARTBEAT_INTERVAL", 30*time.Second),
		ReconnectWindow:   envDur("RECONNECT_WINDOW", 30*time.Second),
	}
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET must be set")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
