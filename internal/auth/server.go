package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloudchat/internal/apierror"
	"cloudchat/internal/config"
	"cloudchat/internal/httpjson"
	"cloudchat/internal/uuid"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

const (
	productionSessionCookie = "__Host-chat_session"
	productionLoginCookie   = "__Host-chat_login"
	localSessionCookie      = "chat_session"
	localLoginCookie        = "chat_login"
	sessionAbsoluteTimeout  = 8 * time.Hour
	loginAttemptTimeout     = 5 * time.Minute
)

type Server struct {
	config       config.Config
	pool         *pgxpool.Pool
	sessionRules SessionRules
	oauth        oauth2.Config
	verifier     *oidc.IDTokenVerifier
	keys         keyRing
	client       *http.Client
}

type tokenBundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	CSRFToken    string `json:"csrf_token"`
}

type activeSession struct {
	principal         Principal
	csrfTokenHash     []byte
	tokensCiphertext  []byte
	encryptionKeyID   string
	expiresAt         time.Time
	idleExpiresAt     time.Time
	tokenExpiresAt    time.Time
	refreshGeneration int64
	identityID        string
}

type activeSessionKey struct{}

// NewServer creates a backend OIDC client and shared PostgreSQL session service.
// It performs issuer discovery during startup so an invalid issuer fails closed.
func NewServer(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, sessionRules SessionRules) (*Server, error) {
	if pool == nil {
		return nil, errors.New("session database is required")
	}
	if sessionRules.pool != pool {
		return nil, errors.New("session rules must use the auth database")
	}
	if cfg.Environment != config.EnvironmentLocal && cfg.Environment != config.EnvironmentProduction {
		return nil, errors.New("APP_ENV must be local or production")
	}
	if cfg.AppOrigin == "" || cfg.OIDCIssuer == "" || cfg.OIDCClientID == "" || cfg.OIDCClientSecret == "" || cfg.OIDCRedirectURL == "" {
		return nil, errors.New("OIDC session configuration is incomplete")
	}
	origin, err := url.Parse(cfg.AppOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.String() != cfg.AppOrigin {
		return nil, errors.New("APP_ORIGIN must be an exact origin")
	}
	issuerURL, err := url.Parse(cfg.OIDCIssuer)
	if err != nil || issuerURL.Scheme == "" || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return nil, errors.New("OIDC_ISSUER must be an absolute URL")
	}
	if cfg.Environment == config.EnvironmentProduction && (origin.Scheme != "https" || issuerURL.Scheme != "https") {
		return nil, errors.New("production application and OIDC issuer URLs must use HTTPS")
	}
	if cfg.OIDCRedirectURL != cfg.AppOrigin+"/auth/callback" {
		return nil, errors.New("OIDC callback URL must match the configured application origin")
	}
	keys, err := newKeyRing(cfg)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	discoveryContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	discoveryContext = context.WithValue(discoveryContext, oauth2.HTTPClient, client)
	metadata, err := discoverOIDC(discoveryContext, client, cfg.OIDCIssuer, cfg.Environment == config.EnvironmentProduction)
	if err != nil {
		return nil, fmt.Errorf("discover configured OIDC issuer: %w", err)
	}
	verifierContext := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	keySet := oidc.NewRemoteKeySet(verifierContext, metadata.JWKSURI)
	verifier := oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{ClientID: cfg.OIDCClientID})
	oauthConfig := oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		RedirectURL:  cfg.OIDCRedirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:   metadata.AuthorizationEndpoint,
			TokenURL:  metadata.TokenEndpoint,
			AuthStyle: oauth2.AuthStyleInHeader,
		},
		Scopes: []string{oidc.ScopeOpenID, "profile"},
	}
	return &Server{
		config:       cfg,
		pool:         pool,
		sessionRules: sessionRules,
		oauth:        oauthConfig,
		verifier:     verifier,
		keys:         keys,
		client:       client,
	}, nil
}

// Middleware authenticates session cookies for HTTP API and logout requests.
// It refreshes valid idle deadlines in PostgreSQL and checks Origin/CSRF before
// any state-changing request.
func (s *Server) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal, ok := PrincipalFromContext(r.Context()); ok && principal.SessionID == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/auth/logout" && r.URL.Path != "/auth/logout-all" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(s.sessionCookieName())
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		refreshTokens := r.URL.Path != "/auth/logout" && r.URL.Path != "/auth/logout-all"
		session, err := s.authenticate(r.Context(), cookie.Value, refreshTokens)
		if err != nil {
			apierror.Write(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
			return
		}
		if session == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := ContextWithPrincipal(r.Context(), session.principal)
		ctx = context.WithValue(ctx, activeSessionKey{}, session)
		r = r.WithContext(ctx)
		if isStateChangingMethod(r.Method) {
			origins := r.Header.Values("Origin")
			if len(origins) != 1 || origins[0] != s.config.AppOrigin {
				apierror.Write(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Origin is not allowed", false, nil)
				return
			}
			csrfTokens := r.Header.Values("X-CSRF-Token")
			if len(csrfTokens) != 1 || csrfTokens[0] == "" {
				apierror.Write(w, http.StatusForbidden, "CSRF_INVALID", "A valid CSRF token is required", false, nil)
				return
			}
			csrf := sha256.Sum256([]byte(csrfTokens[0]))
			if subtle.ConstantTimeCompare(csrf[:], session.csrfTokenHash) != 1 {
				apierror.Write(w, http.StatusForbidden, "CSRF_INVALID", "A valid CSRF token is required", false, nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ServeHTTP handles the backend-owned authentication and session endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/auth/login":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.login(w, r)
	case "/auth/callback":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.callback(w, r)
	case "/auth/logout":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.logout(w, r, false)
	case "/auth/logout-all":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.logout(w, r, true)
	case "/api/v1/session":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.getSession(w, r)
	default:
		apierror.Write(w, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state, err := randomOpaque(32)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	nonce, err := randomOpaque(32)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	verifier := oauth2.GenerateVerifier()
	verifierKeyID, verifierCiphertext, err := s.keys.seal([]byte(verifier))
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	binding := ""
	if cookie, cookieErr := r.Cookie(s.loginCookieName()); cookieErr == nil {
		if decoded, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value); decodeErr == nil && len(decoded) == 32 {
			binding = cookie.Value
		}
	}
	if binding == "" {
		binding, err = randomOpaque(32)
		if err != nil {
			dependencyUnavailable(w)
			return
		}
	}
	stateHash := sha256.Sum256([]byte(state))
	bindingHash := sha256.Sum256([]byte(binding))
	nonceHash := sha256.Sum256([]byte(nonce))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO oauth_login_attempts (
			state_hash, browser_binding_hash, nonce_hash,
			pkce_verifier_ciphertext, encryption_key_id, expires_at
		) VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '5 minutes')`,
		stateHash[:], bindingHash[:], nonceHash[:], verifierCiphertext, verifierKeyID)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	params := oauth2.SetAuthURLParam("nonce", nonce)
	redirect := s.oauth.AuthCodeURL(state, params, oauth2.S256ChallengeOption(verifier))
	http.SetCookie(w, s.loginCookie(binding))
	http.Redirect(w, r, redirect, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	states := query["state"]
	codes := query["code"]
	if len(states) != 1 || len(codes) != 1 || states[0] == "" || codes[0] == "" {
		apierror.Write(w, http.StatusUnauthorized, "OAUTH_STATE_INVALID", "Login could not be completed", false, nil)
		return
	}
	state, code := states[0], codes[0]
	bindingCookie, err := r.Cookie(s.loginCookieName())
	if err != nil || bindingCookie.Value == "" {
		apierror.Write(w, http.StatusUnauthorized, "OAUTH_STATE_INVALID", "Login could not be completed", false, nil)
		return
	}
	stateHash := sha256.Sum256([]byte(state))
	bindingHash := sha256.Sum256([]byte(bindingCookie.Value))
	var attempt loginAttempt
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	err = s.pool.QueryRow(ctx, `
		UPDATE oauth_login_attempts
		SET consumed_at = clock_timestamp()
		WHERE state_hash = $1
		  AND browser_binding_hash = $2
		  AND consumed_at IS NULL
		  AND expires_at > clock_timestamp()
		RETURNING nonce_hash, pkce_verifier_ciphertext, encryption_key_id`,
		stateHash[:], bindingHash[:]).Scan(&attempt.nonceHash, &attempt.verifierCiphertext, &attempt.keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		apierror.Write(w, http.StatusUnauthorized, "OAUTH_STATE_INVALID", "Login could not be completed", false, nil)
		return
	}
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	// The UPDATE statement commits before the network exchange. A failed exchange
	// therefore consumes this state and requires a new authorization attempt.
	verifierBytes, err := s.keys.open(attempt.keyID, attempt.verifierCiphertext)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	token, err := s.oauth.Exchange(s.oauthContext(ctx), code, oauth2.VerifierOption(string(verifierBytes)))
	if err != nil {
		apierror.Write(w, http.StatusUnauthorized, "OIDC_EXCHANGE_FAILED", "Login could not be completed", false, nil)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		apierror.Write(w, http.StatusUnauthorized, "OIDC_TOKEN_INVALID", "Login could not be completed", false, nil)
		return
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		apierror.Write(w, http.StatusUnauthorized, "OIDC_TOKEN_INVALID", "Login could not be completed", false, nil)
		return
	}
	var claims struct {
		Nonce string `json:"nonce"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Nonce == "" {
		apierror.Write(w, http.StatusUnauthorized, "OIDC_TOKEN_INVALID", "Login could not be completed", false, nil)
		return
	}
	actualNonceHash := sha256.Sum256([]byte(claims.Nonce))
	if subtle.ConstantTimeCompare(actualNonceHash[:], attempt.nonceHash) != 1 {
		apierror.Write(w, http.StatusUnauthorized, "OIDC_NONCE_INVALID", "Login could not be completed", false, nil)
		return
	}
	identity, err := s.preparedIdentity(ctx, idToken.Issuer, idToken.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		apierror.Write(w, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "This account is not invited to the application", false, nil)
		return
	}
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.Expiry.IsZero() {
		apierror.Write(w, http.StatusUnauthorized, "OIDC_TOKEN_INVALID", "Login could not be completed", false, nil)
		return
	}
	csrf, err := randomOpaque(32)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	bundle := tokenBundle{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, CSRFToken: csrf}
	ciphertext, keyID, err := s.encryptBundle(bundle)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	sessionSecret, err := randomOpaque(32)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	if err := s.revokePreviousCookie(ctx, r); err != nil {
		dependencyUnavailable(w)
		return
	}
	if err := s.createSession(ctx, identity, sessionSecret, csrf, ciphertext, keyID, token.Expiry); err != nil {
		dependencyUnavailable(w)
		return
	}
	http.SetCookie(w, s.sessionCookie(sessionSecret))
	http.SetCookie(w, s.expiredLoginCookie())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) oauthContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, s.client)
}

type loginAttempt struct {
	nonceHash          []byte
	verifierCiphertext []byte
	keyID              string
}

type preparedUser struct {
	identityID  string
	userID      string
	displayName string
}

func (s *Server) preparedIdentity(ctx context.Context, issuer, subject string) (preparedUser, error) {
	var user preparedUser
	err := s.pool.QueryRow(ctx, `
		SELECT identities.identity_id::text, users.user_id::text, users.display_name
		FROM user_identities identities
		JOIN app_users users ON users.user_id = identities.user_id
		WHERE identities.issuer = $1 AND identities.subject = $2 AND users.is_active`,
		issuer, subject).Scan(&user.identityID, &user.userID, &user.displayName)
	return user, err
}

func (s *Server) createSession(ctx context.Context, identity preparedUser, tokenValue, csrf string, ciphertext []byte, keyID string, tokenExpiry time.Time) error {
	tokenHash := sha256.Sum256([]byte(tokenValue))
	csrfHash := sha256.Sum256([]byte(csrf))
	sessionID, err := uuid.New()
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var authEpoch int64
	if err := tx.QueryRow(ctx, `SELECT auth_epoch FROM app_users WHERE user_id = $1::uuid AND is_active FOR SHARE`, identity.userID).Scan(&authEpoch); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO app_sessions (
			session_id, token_hash, csrf_token_hash, user_id, identity_id, auth_epoch,
			tokens_ciphertext, encryption_key_id, token_expires_at,
			expires_at, idle_expires_at
		) VALUES (
			$1::uuid, $2, $3, $4::uuid, $5::uuid, $6,
			$7, $8, $9, clock_timestamp() + interval '8 hours',
			clock_timestamp() + interval '30 minutes'
		)`, sessionID, tokenHash[:], csrfHash[:], identity.userID, identity.identityID, authEpoch, ciphertext, keyID, tokenExpiry)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	session, ok := r.Context().Value(activeSessionKey{}).(*activeSession)
	if !ok {
		apierror.Write(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}
	bundle, err := s.decryptBundle(session.encryptionKeyID, session.tokensCiphertext)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{
		"user":       map[string]string{"user_id": session.principal.UserID, "display_name": session.principal.DisplayName},
		"session":    map[string]time.Time{"expires_at": session.expiresAt.UTC(), "idle_expires_at": session.idleExpiresAt.UTC()},
		"csrf_token": bundle.CSRFToken,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, all bool) {
	session, ok := r.Context().Value(activeSessionKey{}).(*activeSession)
	if !ok {
		apierror.Write(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err == nil {
		err = lockCurrentSession(ctx, tx, session)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if tx != nil {
			_ = tx.Rollback(ctx)
		}
		apierror.Write(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}
	if err == nil && all {
		_, err = tx.Exec(ctx, `UPDATE app_users SET auth_epoch = auth_epoch + 1 WHERE user_id = $1::uuid`, session.principal.UserID)
	}
	if err == nil {
		if all {
			_, err = tx.Exec(ctx, `UPDATE app_sessions SET revoked_at = COALESCE(revoked_at, clock_timestamp()) WHERE user_id = $1::uuid`, session.principal.UserID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE app_sessions SET revoked_at = COALESCE(revoked_at, clock_timestamp()) WHERE session_id = $1::uuid AND user_id = $2::uuid`, session.principal.SessionID, session.principal.UserID)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	} else if tx != nil {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	http.SetCookie(w, s.expiredSessionCookie())
	w.WriteHeader(http.StatusNoContent)
}

func lockCurrentSession(ctx context.Context, tx pgx.Tx, session *activeSession) error {
	var authEpoch int64
	if err := tx.QueryRow(ctx, `SELECT auth_epoch FROM app_users WHERE user_id = $1::uuid AND is_active FOR UPDATE`, session.principal.UserID).Scan(&authEpoch); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `
		SELECT session_id FROM app_sessions
		WHERE session_id = $1::uuid AND user_id = $2::uuid AND auth_epoch = $3
		  AND revoked_at IS NULL AND expires_at > clock_timestamp() AND idle_expires_at > clock_timestamp()
		FOR UPDATE`, session.principal.SessionID, session.principal.UserID, authEpoch).Scan(new(string))
}

func (s *Server) authenticate(ctx context.Context, cookieValue string, refreshTokens bool) (*activeSession, error) {
	ctx, cancel := s.sessionRules.contextWithTimeout(ctx)
	defer cancel()
	session, err := s.sessionRules.authenticateCookie(ctx, cookieValue)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	if refreshTokens {
		if err := s.refreshIfNeeded(ctx, session); err != nil {
			if errors.Is(err, errSessionRevoked) || errors.Is(err, errSessionExpired) {
				return nil, nil
			}
			return nil, err
		}
	}
	return session, nil
}

func (s *Server) revokePreviousCookie(ctx context.Context, r *http.Request) error {
	cookie, err := r.Cookie(s.sessionCookieName())
	if err != nil || cookie.Value == "" {
		return nil
	}
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	_, err = s.pool.Exec(ctx, `UPDATE app_sessions SET revoked_at = COALESCE(revoked_at, clock_timestamp()) WHERE token_hash = $1`, tokenHash[:])
	return err
}

func (s *Server) encryptBundle(bundle tokenBundle) ([]byte, string, error) {
	plaintext, err := json.Marshal(bundle)
	if err != nil {
		return nil, "", err
	}
	keyID, ciphertext, err := s.keys.seal(plaintext)
	return ciphertext, keyID, err
}

func (s *Server) decryptBundle(keyID string, ciphertext []byte) (tokenBundle, error) {
	plaintext, err := s.keys.open(keyID, ciphertext)
	if err != nil {
		return tokenBundle{}, err
	}
	var bundle tokenBundle
	if err := json.Unmarshal(plaintext, &bundle); err != nil {
		return tokenBundle{}, err
	}
	return bundle, nil
}

func (s *Server) sessionCookieName() string {
	if s.config.Environment == config.EnvironmentProduction {
		return productionSessionCookie
	}
	return localSessionCookie
}

func (s *Server) loginCookieName() string {
	if s.config.Environment == config.EnvironmentProduction {
		return productionLoginCookie
	}
	return localLoginCookie
}

func (s *Server) secureCookies() bool { return s.config.Environment == config.EnvironmentProduction }

func (s *Server) sessionCookie(value string) *http.Cookie {
	return &http.Cookie{Name: s.sessionCookieName(), Value: value, Path: "/", MaxAge: int(sessionAbsoluteTimeout.Seconds()), HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode}
}

func (s *Server) loginCookie(value string) *http.Cookie {
	return &http.Cookie{Name: s.loginCookieName(), Value: value, Path: "/", MaxAge: int(loginAttemptTimeout.Seconds()), HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode}
}

func (s *Server) expiredSessionCookie() *http.Cookie {
	return &http.Cookie{Name: s.sessionCookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode}
}

func (s *Server) expiredLoginCookie() *http.Cookie {
	return &http.Cookie{Name: s.loginCookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode}
}

func randomOpaque(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	apierror.Write(w, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
}

func dependencyUnavailable(w http.ResponseWriter) {
	apierror.Write(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
}
