package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sessionIdleTimeout       = 30 * time.Minute
	sessionRulesQueryTimeout = 5 * time.Second
)

const persistedSessionEligibility = `
	sessions.user_id = users.user_id
	AND users.is_active
	AND sessions.auth_epoch = users.auth_epoch
	AND sessions.revoked_at IS NULL
	AND sessions.expires_at > clock_timestamp()
	AND sessions.idle_expires_at > clock_timestamp()`

const persistedSessionIdleRenewal = `
	last_seen_at = clock_timestamp(),
	idle_expires_at = LEAST(sessions.expires_at, clock_timestamp() + $2 * interval '1 second')`

// SessionRules applies the persisted-session eligibility and idle-expiry rules
// shared by HTTP authentication and WebSocket authorization.
type SessionRules struct {
	pool *pgxpool.Pool
}

// NewSessionRules binds the persisted-session rules to their PostgreSQL store.
func NewSessionRules(pool *pgxpool.Pool) SessionRules {
	return SessionRules{pool: pool}
}

func (rules SessionRules) validPrincipal(principal Principal) (bool, error) {
	if rules.pool == nil {
		return false, errors.New("session rules require PostgreSQL")
	}
	return uuid.IsCanonical(principal.UserID) && uuid.IsCanonical(principal.SessionID), nil
}

// Current reports whether principal refers to an active persisted session.
// Principals without a session ID are handled by the local test identity path.
func (rules SessionRules) Current(parent context.Context, principal Principal) (bool, error) {
	if valid, err := rules.validPrincipal(principal); !valid || err != nil {
		return false, err
	}
	ctx, cancel := rules.contextWithTimeout(parent)
	defer cancel()

	var current bool
	err := rules.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM app_sessions sessions
			JOIN app_users users ON users.user_id = sessions.user_id
			WHERE sessions.session_id = $1::uuid
			  AND sessions.user_id = $2::uuid
			  AND `+persistedSessionEligibility+`
		)`, principal.SessionID, principal.UserID).Scan(&current)
	return current, err
}

// TouchIdle atomically checks a persisted session and renews its idle expiry.
// The new idle deadline never extends beyond the absolute expiry.
func (rules SessionRules) TouchIdle(parent context.Context, principal Principal) (bool, error) {
	if valid, err := rules.validPrincipal(principal); !valid || err != nil {
		return false, err
	}
	ctx, cancel := rules.contextWithTimeout(parent)
	defer cancel()

	var sessionID string
	err := rules.pool.QueryRow(ctx, `
		UPDATE app_sessions sessions
		SET `+persistedSessionIdleRenewal+`
		FROM app_users users
		WHERE sessions.session_id = $1::uuid
		  AND sessions.user_id = $3::uuid
		  AND `+persistedSessionEligibility+`
		RETURNING sessions.session_id::text`, principal.SessionID, int64(sessionIdleTimeout.Seconds()), principal.UserID).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (rules SessionRules) authenticateCookie(parent context.Context, cookieValue string) (*activeSession, error) {
	if rules.pool == nil {
		return nil, errors.New("session rules require PostgreSQL")
	}
	ctx, cancel := rules.contextWithTimeout(parent)
	defer cancel()
	tokenHash := sha256.Sum256([]byte(cookieValue))
	var session activeSession
	var csrfHash []byte
	err := rules.pool.QueryRow(ctx, `
		UPDATE app_sessions sessions
		SET `+persistedSessionIdleRenewal+`
		FROM app_users users
		WHERE sessions.token_hash = $1
		  AND sessions.user_id = users.user_id
		  AND `+persistedSessionEligibility+`
		RETURNING sessions.session_id::text, sessions.user_id::text, users.display_name,
		          sessions.csrf_token_hash, sessions.tokens_ciphertext, sessions.encryption_key_id,
		          sessions.expires_at, sessions.idle_expires_at, sessions.token_expires_at,
		          sessions.refresh_generation, sessions.identity_id::text`,
		tokenHash[:], int64(sessionIdleTimeout.Seconds())).Scan(
		&session.principal.SessionID, &session.principal.UserID, &session.principal.DisplayName,
		&csrfHash, &session.tokensCiphertext, &session.encryptionKeyID,
		&session.expiresAt, &session.idleExpiresAt, &session.tokenExpiresAt,
		&session.refreshGeneration, &session.identityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session.csrfTokenHash = csrfHash
	return &session, nil
}

func (rules SessionRules) contextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, sessionRulesQueryTimeout)
}
