package auth

import (
	"context"
	"net/http"

	"cloudchat/internal/config"
	"cloudchat/internal/demo"
)

const LocalTestUserHeader = "X-Local-Test-User-ID"

type Principal struct {
	UserID      string
	DisplayName string
}

type principalKey struct{}

// LocalTestIdentityMiddleware accepts a seeded demo identity only when local
// mode has explicitly enabled the adapter. Outside local mode the header is
// ignored, even if a Config value is constructed incorrectly by a caller.
func LocalTestIdentityMiddleware(cfg config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Environment == config.EnvironmentLocal && cfg.EnableLocalTestIdentity {
			if user, ok := demo.UserByID(r.Header.Get(LocalTestUserHeader)); ok {
				principal := Principal{UserID: user.ID, DisplayName: user.DisplayName}
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}
