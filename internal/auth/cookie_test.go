package auth

import (
	"net/http"
	"testing"

	"cloudchat/internal/config"
)

func TestProductionSessionAndLoginCookiesAreHostOnlyAndSecure(t *testing.T) {
	server := &Server{config: config.Config{Environment: config.EnvironmentProduction}}
	for _, cookie := range []*http.Cookie{server.sessionCookie("opaque-session"), server.loginCookie("opaque-binding")} {
		if cookie.Name != "__Host-chat_session" && cookie.Name != "__Host-chat_login" {
			t.Errorf("production cookie name = %q, want __Host prefix", cookie.Name)
		}
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Errorf("production cookie attributes = %+v, want Secure HttpOnly SameSite=Lax Path=/ without Domain", cookie)
		}
	}
}

func TestLocalCookiesAreNotHostPrefixedOrSecure(t *testing.T) {
	server := &Server{config: config.Config{Environment: config.EnvironmentLocal}}
	cookie := server.sessionCookie("opaque-session")
	if cookie.Name != localSessionCookie || cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("local session cookie attributes = %+v", cookie)
	}
}
