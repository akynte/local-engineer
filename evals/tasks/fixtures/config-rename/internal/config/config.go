package config

import "os"

// Config is loaded from the environment.
type Config struct {
	DatabaseURL string
	ListenAddr  string
}

// Load reads the configuration.
func Load() Config {
	return Config{
		DatabaseURL: os.Getenv("DB_URL"),
		ListenAddr:  os.Getenv("LISTEN_ADDR"),
	}
}
