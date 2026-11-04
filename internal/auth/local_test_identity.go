package auth

import (
	"net/http"

	"cloudchat/internal/config"
	"cloudchat/internal/demo"
)

const LocalTestUserHeader = "X-Local-Test-User-ID"

// LocalTestIdentityMiddleware accepts a seeded demo identity only when local
// mode has explicitly enabled the adapter. Outside local mode the header is
// ignored, even if a Config value is constructed incorrectly by a caller.
func LocalTestIdentityMiddleware(cfg config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Environment == config.EnvironmentLocal && cfg.EnableLocalTestIdentity {
			if user, ok := demo.UserByID(r.Header.Get(LocalTestUserHeader)); ok {
				principal := Principal{UserID: user.ID, DisplayName: user.DisplayName}
				r = r.WithContext(ContextWithPrincipal(r.Context(), principal))
			}
		}
		next.ServeHTTP(w, r)
	})
}
