package config

import "os"

// Config is loaded from the environment. Once the configuration analyzer
// lands, each Getenv call below becomes a config_key node with a reads_config
// edge to this function, so `le graph impact DATABASE_URL --change config`
// finds every consumer and every deployment manifest that sets it.
type Config struct {
	DatabaseURL string
	ListenAddr  string
	Environment string
}

func Load() Config {
	return Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		ListenAddr:  envOr("LISTEN_ADDR", ":8080"),
		Environment: envOr("ENVIRONMENT", "development"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
