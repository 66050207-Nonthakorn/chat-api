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
	// SessionID is populated by server-managed session authentication. Local
	// test identities intentionally have no session ID.
	SessionID string
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
				r = r.WithContext(ContextWithPrincipal(r.Context(), principal))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ContextWithPrincipal stores an authenticated identity for downstream API
// handlers. Authentication middleware uses this to share its verified result.
func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}
