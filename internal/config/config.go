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
	// SRVMON_SELF_TOKEN_FILE: when set, the monitoring host registers itself
	// as server SRVMON_SELF_NAME (default "monitor") and writes its agent
	// token to this file for the agent in the same compose stack.
	SelfTokenFile string
	SelfName      string

	// MasterKeyFile (SRVMON_MASTER_KEY_FILE, default <data>/master.key)
	// encrypts SSH private keys stored in the database.
	MasterKeyFile string
	// AgentImage (SRVMON_AGENT_IMAGE) is installed on servers by the add
	// wizard. Optional SRVMON_AGENT_REGISTRY_USER/_TOKEN log in for the pull
	// when the image is private.
	AgentImage         string
	AgentRegistryUser  string
	AgentRegistryToken string

	// AI agent (srvmon-ai container). Both empty disables the agent.
	// SRVMON_AI_URL is the srvmon-ai base URL, SRVMON_AI_TOKEN the bearer
	// token both sides share. The agent's tool API listens on
	// SRVMON_AI_TOOLS_LISTEN (default :8081), which must stay inside the
	// compose network.
	AIURL         string
	AIToken       string
	AIToolsListen string
}

// AIEnabled reports whether the AI agent is configured.
func (c Config) AIEnabled() bool { return c.AIURL != "" && c.AIToken != "" }

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "srvmon.db") }

// FromEnv loads the config from the environment.
func FromEnv() (Config, error) {
	c := Config{
		Listen:        env("SRVMON_LISTEN", ":8080"),
		DataDir:       env("SRVMON_DATA_DIR", "./data"),
		VMURL:         env("SRVMON_VM_URL", ""),
		AuthLog:       env("SRVMON_AUTH_LOG", ""),
		PublicOrigin:  env("SRVMON_PUBLIC_ORIGIN", ""),
		SelfTokenFile: env("SRVMON_SELF_TOKEN_FILE", ""),
		SelfName:      env("SRVMON_SELF_NAME", "monitor"),

		AgentImage:         env("SRVMON_AGENT_IMAGE", "ghcr.io/askidmobile/srvmon-agent:latest"),
		AgentRegistryUser:  env("SRVMON_AGENT_REGISTRY_USER", ""),
		AgentRegistryToken: env("SRVMON_AGENT_REGISTRY_TOKEN", ""),

		AIURL:         env("SRVMON_AI_URL", ""),
		AIToken:       env("SRVMON_AI_TOKEN", ""),
		AIToolsListen: env("SRVMON_AI_TOOLS_LISTEN", ":8081"),
	}
	if c.AIToken != "" && len(c.AIToken) < 32 {
		return c, fmt.Errorf("SRVMON_AI_TOKEN: at least 32 characters")
	}
	c.MasterKeyFile = env("SRVMON_MASTER_KEY_FILE", filepath.Join(c.DataDir, "master.key"))
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
