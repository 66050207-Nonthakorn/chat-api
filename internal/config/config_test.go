package config_test

import (
	"testing"

	"cloudchat/internal/config"
)

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

func setValidRuntimeEnv(t *testing.T, environment string) {
	t.Helper()
	t.Setenv("APP_ENV", environment)
	t.Setenv("DATABASE_URL", "postgres://chat:chat@localhost:5432/chat?sslmode=disable")
	t.Setenv("VALKEY_URL", "redis://localhost:6379/0")
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
}
