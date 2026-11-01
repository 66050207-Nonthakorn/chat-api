package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Environment string

const (
	EnvironmentLocal      Environment = "local"
	EnvironmentProduction Environment = "production"
)

// Config contains runtime values that are safe to log only by field name.
type Config struct {
	Environment             Environment
	DatabaseURL             string
	ValkeyURL               string
	HTTPAddr                string
	EnableLocalTestIdentity bool
}

// Load reads process configuration and fails closed for unsupported modes or
// unsafe local-only features.
func Load() (Config, error) {
	environment := strings.TrimSpace(os.Getenv("APP_ENV"))
	if environment == "" {
		environment = string(EnvironmentProduction)
	}

	cfg := Config{Environment: Environment(environment)}
	if cfg.Environment != EnvironmentLocal && cfg.Environment != EnvironmentProduction {
		return Config{}, fmt.Errorf("APP_ENV must be local or production")
	}

	localIdentityValue := strings.TrimSpace(os.Getenv("ENABLE_LOCAL_TEST_IDENTITY"))
	if localIdentityValue != "" {
		enabled, err := strconv.ParseBool(localIdentityValue)
		if err != nil {
			return Config{}, fmt.Errorf("ENABLE_LOCAL_TEST_IDENTITY must be a boolean")
		}
		cfg.EnableLocalTestIdentity = enabled
	}
	if cfg.EnableLocalTestIdentity && cfg.Environment != EnvironmentLocal {
		return Config{}, fmt.Errorf("local test identity is available only when APP_ENV=local")
	}

	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	cfg.ValkeyURL = strings.TrimSpace(os.Getenv("VALKEY_URL"))
	if cfg.Environment == EnvironmentLocal {
		if cfg.DatabaseURL == "" {
			cfg.DatabaseURL = "postgres://chat:chat@localhost:5432/chat?sslmode=disable"
		}
		if cfg.ValkeyURL == "" {
			cfg.ValkeyURL = "redis://localhost:6379/0"
		}
	} else {
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required in production")
		}
		if cfg.ValkeyURL == "" {
			return Config{}, fmt.Errorf("VALKEY_URL is required in production")
		}
	}

	cfg.HTTPAddr = strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}
	return cfg, nil
}
