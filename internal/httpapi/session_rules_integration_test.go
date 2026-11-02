package httpapi_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/demo"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionRulesRecognizesPersistedSession(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, _ := app.login(t, demo.UserAID, "session-rules-current")
	principal := principalForSessionCookie(t, app.base.pool, cookie.Value)

	rules := auth.NewSessionRules(app.base.pool)
	current, err := rules.Current(context.Background(), principal)
	if err != nil {
		t.Fatalf("check persisted session: %v", err)
	}
	if !current {
		t.Fatal("persisted session is not current")
	}
}

func TestSessionRulesRejectsIneligiblePersistedSessions(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, _ := app.login(t, demo.UserAID, "session-rules-ineligible")
	principal := principalForSessionCookie(t, app.base.pool, cookie.Value)
	rules := auth.NewSessionRules(app.base.pool)

	assertNotCurrent := func(name string, candidate auth.Principal) {
		t.Helper()
		current, err := rules.Current(context.Background(), candidate)
		if err != nil {
			t.Fatalf("check %s session: %v", name, err)
		}
		if current {
			t.Errorf("%s session is current", name)
		}
	}
	update := func(statement string, args ...any) {
		t.Helper()
		if _, err := app.base.pool.Exec(context.Background(), statement, args...); err != nil {
			t.Fatalf("update session fixture: %v", err)
		}
	}

	wrongUser := principal
	wrongUser.UserID = demo.UserBID
	assertNotCurrent("different user", wrongUser)
	wrongSession := principal
	wrongSession.SessionID = "00000000-0000-0000-0000-000000000000"
	assertNotCurrent("different session", wrongSession)

	update("UPDATE app_users SET is_active = false WHERE user_id = $1::uuid", principal.UserID)
	assertNotCurrent("inactive user", principal)
	update("UPDATE app_users SET is_active = true WHERE user_id = $1::uuid", principal.UserID)

	update(`UPDATE app_sessions SET auth_epoch = auth_epoch + 1 WHERE session_id = $1::uuid`, principal.SessionID)
	assertNotCurrent("stale auth epoch", principal)
	update(`UPDATE app_sessions SET auth_epoch = users.auth_epoch FROM app_users users WHERE app_sessions.session_id = $1::uuid AND users.user_id = $2::uuid`, principal.SessionID, principal.UserID)

	update("UPDATE app_sessions SET revoked_at = clock_timestamp() WHERE session_id = $1::uuid", principal.SessionID)
	assertNotCurrent("revoked", principal)
	update("UPDATE app_sessions SET revoked_at = NULL WHERE session_id = $1::uuid", principal.SessionID)

	update(`
		UPDATE app_sessions
		SET created_at = clock_timestamp() - interval '1 hour',
		    expires_at = clock_timestamp() - interval '1 second',
		    idle_expires_at = clock_timestamp() - interval '2 seconds'
		WHERE session_id = $1::uuid`, principal.SessionID)
	assertNotCurrent("absolute-expired", principal)
	update(`
		UPDATE app_sessions
		SET expires_at = clock_timestamp() + interval '1 hour',
		    idle_expires_at = clock_timestamp() + interval '30 minutes'
		WHERE session_id = $1::uuid`, principal.SessionID)

	update("UPDATE app_sessions SET idle_expires_at = clock_timestamp() - interval '1 second' WHERE session_id = $1::uuid", principal.SessionID)
	assertNotCurrent("idle-expired", principal)
}

func TestSessionRulesTouchIdleRenewsOnlyCurrentSessionsAndHonorsAbsoluteExpiry(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, _ := app.login(t, demo.UserAID, "session-rules-touch-idle")
	principal := principalForSessionCookie(t, app.base.pool, cookie.Value)
	rules := auth.NewSessionRules(app.base.pool)

	if _, err := app.base.pool.Exec(context.Background(), `
		UPDATE app_sessions
		SET created_at = clock_timestamp() - interval '1 hour',
		    expires_at = clock_timestamp() + interval '1 hour',
		    idle_expires_at = clock_timestamp() - interval '1 second'
		WHERE session_id = $1::uuid`, principal.SessionID); err != nil {
		t.Fatalf("expire idle session fixture: %v", err)
	}
	touched, err := rules.TouchIdle(context.Background(), principal)
	if err != nil {
		t.Fatalf("touch expired session: %v", err)
	}
	if touched {
		t.Fatal("expired session was renewed")
	}

	if _, err := app.base.pool.Exec(context.Background(), `
		UPDATE app_sessions
		SET expires_at = clock_timestamp() + interval '3 seconds',
		    idle_expires_at = clock_timestamp() + interval '100 milliseconds'
		WHERE session_id = $1::uuid`, principal.SessionID); err != nil {
		t.Fatalf("prepare session expiry fixture: %v", err)
	}
	touched, err = rules.TouchIdle(context.Background(), principal)
	if err != nil {
		t.Fatalf("touch current session: %v", err)
	}
	if !touched {
		t.Fatal("current session was not renewed")
	}

	time.Sleep(200 * time.Millisecond)
	current, err := rules.Current(context.Background(), principal)
	if err != nil {
		t.Fatalf("check renewed session: %v", err)
	}
	if !current {
		t.Fatal("renewed session expired at its previous idle deadline")
	}

	time.Sleep(3 * time.Second)
	current, err = rules.Current(context.Background(), principal)
	if err != nil {
		t.Fatalf("check absolutely expired session: %v", err)
	}
	if current {
		t.Fatal("idle renewal extended the session past its absolute expiry")
	}
}

func TestSessionRulesHonorCancellationAndReturnDatabaseErrors(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, _ := app.login(t, demo.UserAID, "session-rules-errors")
	principal := principalForSessionCookie(t, app.base.pool, cookie.Value)
	rules := auth.NewSessionRules(app.base.pool)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rules.Current(ctx, principal); !errors.Is(err, context.Canceled) {
		t.Fatalf("Current error = %v, want context canceled", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := rules.TouchIdle(ctx, principal); !errors.Is(err, context.Canceled) {
		t.Fatalf("TouchIdle error = %v, want context canceled", err)
	}

	closedPool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/unused")
	if err != nil {
		t.Fatalf("create closed PostgreSQL pool fixture: %v", err)
	}
	closedPool.Close()
	closedRules := auth.NewSessionRules(closedPool)
	if _, err := closedRules.Current(context.Background(), principal); err == nil {
		t.Fatal("Current succeeded with a closed PostgreSQL pool")
	}
	if _, err := closedRules.TouchIdle(context.Background(), principal); err == nil {
		t.Fatal("TouchIdle succeeded with a closed PostgreSQL pool")
	}
}

func principalForSessionCookie(t *testing.T, pool *pgxpool.Pool, cookieValue string) auth.Principal {
	t.Helper()
	tokenHash := sha256.Sum256([]byte(cookieValue))
	var principal auth.Principal
	if err := pool.QueryRow(context.Background(), `
		SELECT session_id::text, user_id::text
		FROM app_sessions
		WHERE token_hash = $1`, tokenHash[:]).Scan(&principal.SessionID, &principal.UserID); err != nil {
		t.Fatalf("find session principal: %v", err)
	}
	return principal
}
