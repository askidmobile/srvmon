// Package config reads srvmon-server settings from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds srvmon-server settings.
type Config struct {
	Listen       string        // SRVMON_LISTEN, default ":8080"
	DataDir      string        // SRVMON_DATA_DIR, default "./data"
	VMURL        string        // SRVMON_VM_URL, VictoriaMetrics base URL; empty disables writes
	AuthLog      string        // SRVMON_AUTH_LOG, file for fail2ban; empty = stderr only
	PublicOrigin string        // SRVMON_PUBLIC_ORIGIN, e.g. https://mon.example.com
	TrustProxy   bool          // SRVMON_TRUST_PROXY, take client IP from X-Forwarded-For
	SecureCookie bool          // SRVMON_SECURE_COOKIE, default true
	SessionTTL   time.Duration // SRVMON_SESSION_TTL, default 12h
}

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "srvmon.db") }

// FromEnv loads the config from the environment.
func FromEnv() (Config, error) {
	c := Config{
		Listen:       env("SRVMON_LISTEN", ":8080"),
		DataDir:      env("SRVMON_DATA_DIR", "./data"),
		VMURL:        env("SRVMON_VM_URL", ""),
		AuthLog:      env("SRVMON_AUTH_LOG", ""),
		PublicOrigin: env("SRVMON_PUBLIC_ORIGIN", ""),
	}
	var err error
	if c.TrustProxy, err = boolEnv("SRVMON_TRUST_PROXY", false); err != nil {
		return c, err
	}
	if c.SecureCookie, err = boolEnv("SRVMON_SECURE_COOKIE", true); err != nil {
		return c, err
	}
	ttl := env("SRVMON_SESSION_TTL", "12h")
	if c.SessionTTL, err = time.ParseDuration(ttl); err != nil {
		return c, fmt.Errorf("SRVMON_SESSION_TTL: %w", err)
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
