package config_test

import (
	"encoding/base64"
	"testing"
	"time"

	"cloudchat/internal/config"
)

func TestShutdownGraceUsesBoundedDefaultAndConfiguredSeconds(t *testing.T) {
	setValidRuntimeEnv(t, "local")
	t.Setenv("SHUTDOWN_GRACE_SECONDS", "")
	defaultConfig, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with default shutdown grace: %v", err)
	}
	if defaultConfig.ShutdownGrace != 20*time.Second {
		t.Fatalf("default shutdown grace = %s, want 20s", defaultConfig.ShutdownGrace)
	}

	t.Setenv("SHUTDOWN_GRACE_SECONDS", "12")
	configured, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with configured shutdown grace: %v", err)
	}
	if configured.ShutdownGrace != 12*time.Second {
		t.Fatalf("configured shutdown grace = %s, want 12s", configured.ShutdownGrace)
	}
}

func TestShutdownGraceRejectsValuesOutsideContainerStopBudget(t *testing.T) {
	for _, value := range []string{"0", "26", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			setValidRuntimeEnv(t, "local")
			t.Setenv("SHUTDOWN_GRACE_SECONDS", value)
			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() accepted shutdown grace %q", value)
			}
		})
	}
}

func TestLocalTestIdentityCannotBeEnabledOutsideLocalMode(t *testing.T) {
	setValidRuntimeEnv(t, "production")
	t.Setenv("ENABLE_LOCAL_TEST_IDENTITY", "true")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load succeeded with local test identity enabled outside local mode")
	}
}

func TestLocalTestIdentityIsDisabledByDefault(t *testing.T) {
	setValidRuntimeEnv(t, "local")
	t.Setenv("ENABLE_LOCAL_TEST_IDENTITY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.EnableLocalTestIdentity {
		t.Fatal("local test identity enabled without explicit opt-in")
	}
}

func TestLocalTestIdentityCanBeExplicitlyEnabledLocally(t *testing.T) {
	setValidRuntimeEnv(t, "local")
	t.Setenv("ENABLE_LOCAL_TEST_IDENTITY", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.EnableLocalTestIdentity {
		t.Fatal("local test identity not enabled in local mode")
	}
}

func TestUnknownEnvironmentFailsClosed(t *testing.T) {
	setValidRuntimeEnv(t, "development")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load succeeded with unknown APP_ENV")
	}
}

func TestProductionRequiresStrongCursorSigningKey(t *testing.T) {
	setValidRuntimeEnv(t, "production")
	t.Setenv("CURSOR_HMAC_KEY", "short")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load succeeded with a short cursor signing key")
	}
}

func TestProductionRequiresOIDCAndSessionEncryptionConfig(t *testing.T) {
	setValidRuntimeEnv(t, "production")
	t.Setenv("APP_ORIGIN", "")
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	t.Setenv("OIDC_REDIRECT_URL", "")
	t.Setenv("SESSION_ENCRYPTION_CURRENT_KEY_ID", "")
	t.Setenv("SESSION_ENCRYPTION_KEYS", "")
	if _, err := config.Load(); err == nil {
		t.Fatal("production configuration loaded without backend-managed OIDC session settings")
	}
}

func TestProductionWorkerConfigDoesNotRequireAPIAuthenticationSecrets(t *testing.T) {
	setValidRuntimeEnv(t, "production")
	t.Setenv("CURSOR_HMAC_KEY", "")
	for _, name := range []string{
		"APP_ORIGIN",
		"OIDC_ISSUER",
		"OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET",
		"OIDC_REDIRECT_URL",
		"SESSION_ENCRYPTION_CURRENT_KEY_ID",
		"SESSION_ENCRYPTION_KEYS",
	} {
		t.Setenv(name, "")
	}

	cfg, err := config.LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker() without API-only secrets: %v", err)
	}
	if cfg.Environment != config.EnvironmentProduction || cfg.DatabaseURL == "" || cfg.ValkeyURL == "" {
		t.Fatalf("worker config = %+v, want production with database and Valkey only", cfg)
	}
	if cfg.CursorHMACKey != "" || cfg.OIDCClientSecret != "" || len(cfg.SessionEncryptionKeys) != 0 {
		t.Fatalf("worker config loaded API-only secrets: %+v", cfg)
	}
}

func TestProductionLoadsSessionEncryptionKeyRing(t *testing.T) {
	setValidRuntimeEnv(t, "production")
	t.Setenv("APP_ORIGIN", "https://chat.example.test")
	t.Setenv("OIDC_ISSUER", "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_example")
	t.Setenv("OIDC_CLIENT_ID", "example-client")
	t.Setenv("OIDC_CLIENT_SECRET", "example-secret")
	t.Setenv("OIDC_REDIRECT_URL", "https://chat.example.test/auth/callback")
	t.Setenv("SESSION_ENCRYPTION_CURRENT_KEY_ID", "current")
	t.Setenv("SESSION_ENCRYPTION_KEYS", `{"current":"`+base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))+`","previous":"`+base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))+`"}`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SessionEncryptionCurrentKeyID != "current" || len(cfg.SessionEncryptionKeys) != 2 || len(cfg.SessionEncryptionKeys["current"]) != 32 {
		t.Fatalf("loaded encryption key ring = current %q, keys %d", cfg.SessionEncryptionCurrentKeyID, len(cfg.SessionEncryptionKeys))
	}
}

func TestProductionRequiresHTTPSAndFixedOIDCCallback(t *testing.T) {
	setValidRuntimeEnv(t, "production")
	t.Setenv("APP_ORIGIN", "http://chat.example.test")
	t.Setenv("OIDC_ISSUER", "https://issuer.example.test")
	t.Setenv("OIDC_CLIENT_ID", "example-client")
	t.Setenv("OIDC_CLIENT_SECRET", "example-secret")
	t.Setenv("OIDC_REDIRECT_URL", "http://chat.example.test/auth/callback")
	t.Setenv("SESSION_ENCRYPTION_CURRENT_KEY_ID", "current")
	t.Setenv("SESSION_ENCRYPTION_KEYS", `{"current":"`+base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))+`"}`)
	if _, err := config.Load(); err == nil {
		t.Fatal("production configuration loaded with an insecure application origin")
	}

	t.Setenv("APP_ORIGIN", "https://chat.example.test")
	t.Setenv("OIDC_REDIRECT_URL", "https://evil.example.test/auth/callback")
	if _, err := config.Load(); err == nil {
		t.Fatal("production configuration loaded with a callback on a different origin")
	}
}

func TestLocalModeHasDevelopmentCursorSigningKey(t *testing.T) {
	setValidRuntimeEnv(t, "local")
	t.Setenv("CURSOR_HMAC_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.CursorHMACKey) < 32 {
		t.Fatal("local cursor signing key is too short")
	}
}

func setValidRuntimeEnv(t *testing.T, environment string) {
	t.Helper()
	t.Setenv("APP_ENV", environment)
	t.Setenv("DATABASE_URL", "postgres://chat:chat@localhost:5432/chat?sslmode=disable")
	t.Setenv("VALKEY_URL", "redis://localhost:6379/0")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("IMAGE_BUCKET", "cloudchat-test-media")
	t.Setenv("IMAGE_ENDPOINT", "")
	t.Setenv("IMAGE_ACCESS_KEY", "")
	t.Setenv("IMAGE_SECRET_KEY", "")
	t.Setenv("IMAGE_MESSAGES_ENABLED", "")
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("CURSOR_HMAC_KEY", "test-room-cursor-signing-key-0123456789")
	t.Setenv("APP_ORIGIN", "")
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	t.Setenv("OIDC_REDIRECT_URL", "")
	t.Setenv("SESSION_ENCRYPTION_CURRENT_KEY_ID", "")
	t.Setenv("SESSION_ENCRYPTION_KEYS", "")
}
