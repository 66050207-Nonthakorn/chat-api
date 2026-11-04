package auth

import "context"

type Principal struct {
	UserID      string
	DisplayName string
	// SessionID is populated by server-managed session authentication. Local
	// test identities intentionally have no session ID.
	SessionID string
}

type principalKey struct{}

// ContextWithPrincipal stores an authenticated identity for downstream API
// handlers. Authentication middleware uses this to share its verified result.
func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}
