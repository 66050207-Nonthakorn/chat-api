package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Environment string

const (
	EnvironmentLocal      Environment = "local"
	EnvironmentProduction Environment = "production"
	localCursorHMACKey                = "local-only-room-cursor-signing-key-change-in-production"
)

// Config contains runtime values that are safe to log only by field name.
type Config struct {
	Environment                   Environment
	DatabaseURL                   string
	ValkeyURL                     string
	HTTPAddr                      string
	EnableLocalTestIdentity       bool
	CursorHMACKey                 string
	AppOrigin                     string
	OIDCIssuer                    string
	OIDCClientID                  string
	OIDCClientSecret              string
	OIDCRedirectURL               string
	SessionEncryptionCurrentKeyID string
	SessionEncryptionKeys         map[string][]byte
	ImageRegion                   string
	ImageBucket                   string
	ImageEndpoint                 string
	ImageAccessKey                string
	ImageSecretKey                string
	ShutdownGrace                 time.Duration
}

// Load reads API configuration and fails closed when production auth settings
// or unsafe local-only features are missing.
func Load() (Config, error) {
	return load(true)
}

// LoadWorker reads only the settings needed by the outbox worker. Production
// identity and cursor secrets are intentionally API-only.
func LoadWorker() (Config, error) {
	return load(false)
}

func load(apiProcess bool) (Config, error) {
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
	cfg.ShutdownGrace = 20 * time.Second
	if configuredGrace := strings.TrimSpace(os.Getenv("SHUTDOWN_GRACE_SECONDS")); configuredGrace != "" {
		seconds, err := strconv.Atoi(configuredGrace)
		if err != nil || seconds < 1 || seconds > 25 {
			return Config{}, fmt.Errorf("SHUTDOWN_GRACE_SECONDS must be an integer between 1 and 25")
		}
		cfg.ShutdownGrace = time.Duration(seconds) * time.Second
	}

	if apiProcess {
		cfg.CursorHMACKey = strings.TrimSpace(os.Getenv("CURSOR_HMAC_KEY"))
		if cfg.Environment == EnvironmentLocal && cfg.CursorHMACKey == "" {
			cfg.CursorHMACKey = localCursorHMACKey
		}
		if len([]byte(cfg.CursorHMACKey)) < 32 {
			if cfg.Environment == EnvironmentProduction {
				return Config{}, fmt.Errorf("CURSOR_HMAC_KEY must contain at least 32 bytes in production")
			}
			return Config{}, fmt.Errorf("CURSOR_HMAC_KEY must contain at least 32 bytes")
		}
	}

	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	cfg.ValkeyURL = strings.TrimSpace(os.Getenv("VALKEY_URL"))
	cfg.ImageRegion = strings.TrimSpace(os.Getenv("AWS_REGION"))
	cfg.ImageBucket = strings.TrimSpace(os.Getenv("IMAGE_BUCKET"))
	cfg.ImageEndpoint = strings.TrimSpace(os.Getenv("IMAGE_ENDPOINT"))
	cfg.ImageAccessKey = strings.TrimSpace(os.Getenv("IMAGE_ACCESS_KEY"))
	cfg.ImageSecretKey = strings.TrimSpace(os.Getenv("IMAGE_SECRET_KEY"))
	if cfg.Environment == EnvironmentLocal {
		if cfg.DatabaseURL == "" {
			cfg.DatabaseURL = "postgres://chat:chat@localhost:5432/chat?sslmode=disable"
		}
		if cfg.ValkeyURL == "" {
			cfg.ValkeyURL = "redis://localhost:6379/0"
		}
		if cfg.ImageRegion == "" {
			cfg.ImageRegion = "us-east-1"
		}
		if cfg.ImageBucket == "" {
			cfg.ImageBucket = "cloudchat-images"
		}
		if cfg.ImageEndpoint == "" {
			cfg.ImageEndpoint = "http://localhost:8333"
		}
		if cfg.ImageAccessKey == "" {
			cfg.ImageAccessKey = "admin"
		}
		if cfg.ImageSecretKey == "" {
			cfg.ImageSecretKey = "secret"
		}
	} else {
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required in production")
		}
		if cfg.ValkeyURL == "" {
			return Config{}, fmt.Errorf("VALKEY_URL is required in production")
		}
		if cfg.ImageRegion == "" || cfg.ImageBucket == "" {
			return Config{}, fmt.Errorf("AWS_REGION and IMAGE_BUCKET are required in production")
		}
		if cfg.ImageEndpoint != "" || cfg.ImageAccessKey != "" || cfg.ImageSecretKey != "" {
			return Config{}, fmt.Errorf("IMAGE_ENDPOINT and static image credentials are available only in local development")
		}
	}
	if apiProcess {
		if err := loadAuthConfig(&cfg); err != nil {
			return Config{}, err
		}
	}

	cfg.HTTPAddr = strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}
	return cfg, nil
}

func loadAuthConfig(cfg *Config) error {
	cfg.AppOrigin = strings.TrimSpace(os.Getenv("APP_ORIGIN"))
	cfg.OIDCIssuer = strings.TrimSpace(os.Getenv("OIDC_ISSUER"))
	cfg.OIDCClientID = strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID"))
	cfg.OIDCClientSecret = strings.TrimSpace(os.Getenv("OIDC_CLIENT_SECRET"))
	cfg.OIDCRedirectURL = strings.TrimSpace(os.Getenv("OIDC_REDIRECT_URL"))
	cfg.SessionEncryptionCurrentKeyID = strings.TrimSpace(os.Getenv("SESSION_ENCRYPTION_CURRENT_KEY_ID"))
	encodedKeys := strings.TrimSpace(os.Getenv("SESSION_ENCRYPTION_KEYS"))

	configured := cfg.AppOrigin != "" || cfg.OIDCIssuer != "" || cfg.OIDCClientID != "" || cfg.OIDCClientSecret != "" || cfg.OIDCRedirectURL != "" || cfg.SessionEncryptionCurrentKeyID != "" || encodedKeys != ""
	if !configured && cfg.Environment == EnvironmentLocal {
		return nil
	}
	if cfg.Environment == EnvironmentProduction || configured {
		for _, setting := range []struct{ name, value string }{
			{name: "APP_ORIGIN", value: cfg.AppOrigin},
			{name: "OIDC_ISSUER", value: cfg.OIDCIssuer},
			{name: "OIDC_CLIENT_ID", value: cfg.OIDCClientID},
			{name: "OIDC_CLIENT_SECRET", value: cfg.OIDCClientSecret},
			{name: "OIDC_REDIRECT_URL", value: cfg.OIDCRedirectURL},
			{name: "SESSION_ENCRYPTION_CURRENT_KEY_ID", value: cfg.SessionEncryptionCurrentKeyID},
			{name: "SESSION_ENCRYPTION_KEYS", value: encodedKeys},
		} {
			if setting.value == "" {
				return fmt.Errorf("%s is required when OIDC sessions are enabled", setting.name)
			}
		}
	}

	if cfg.AppOrigin == "" {
		return nil
	}
	origin, err := url.Parse(cfg.AppOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.String() != cfg.AppOrigin {
		return fmt.Errorf("APP_ORIGIN must be an exact origin without a path")
	}
	if cfg.Environment == EnvironmentProduction && origin.Scheme != "https" {
		return fmt.Errorf("APP_ORIGIN must use https in production")
	}
	if origin.Scheme != "https" && !(cfg.Environment == EnvironmentLocal && origin.Scheme == "http") {
		return fmt.Errorf("APP_ORIGIN must use http or https")
	}
	issuer, err := url.Parse(cfg.OIDCIssuer)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("OIDC_ISSUER must be an absolute issuer URL")
	}
	if cfg.Environment == EnvironmentProduction && issuer.Scheme != "https" {
		return fmt.Errorf("OIDC_ISSUER must use https in production")
	}
	if issuer.Scheme != "https" && !(cfg.Environment == EnvironmentLocal && issuer.Scheme == "http") {
		return fmt.Errorf("OIDC_ISSUER must use http or https")
	}
	redirect, err := url.Parse(cfg.OIDCRedirectURL)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.String() != cfg.AppOrigin+"/auth/callback" {
		return fmt.Errorf("OIDC_REDIRECT_URL must be APP_ORIGIN followed by /auth/callback")
	}
	if cfg.Environment == EnvironmentProduction && redirect.Scheme != "https" {
		return fmt.Errorf("OIDC_REDIRECT_URL must use https in production")
	}
	var keyValues map[string]string
	if err := json.Unmarshal([]byte(encodedKeys), &keyValues); err != nil || len(keyValues) == 0 {
		return fmt.Errorf("SESSION_ENCRYPTION_KEYS must be a JSON object of key IDs to base64 keys")
	}
	cfg.SessionEncryptionKeys = make(map[string][]byte, len(keyValues))
	for id, encodedKey := range keyValues {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("SESSION_ENCRYPTION_KEYS contains an empty key ID")
		}
		key, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(key) != 32 {
			return fmt.Errorf("SESSION_ENCRYPTION_KEYS values must be base64-encoded 32-byte keys")
		}
		cfg.SessionEncryptionKeys[id] = key
	}
	if _, ok := cfg.SessionEncryptionKeys[cfg.SessionEncryptionCurrentKeyID]; !ok {
		return fmt.Errorf("SESSION_ENCRYPTION_CURRENT_KEY_ID must name a configured encryption key")
	}
	return nil
}
