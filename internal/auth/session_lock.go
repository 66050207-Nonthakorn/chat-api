package auth

import (
	"context"
	"errors"

	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
)

// LockActiveSession revalidates an authenticated request while holding the
// user and session rows in the documented lock order. State-changing handlers
// call it inside their transaction so logout-all cannot commit while an
// already-authenticated write is still in progress.
func LockActiveSession(ctx context.Context, tx pgx.Tx, principal Principal) (bool, error) {
	if principal.SessionID == "" {
		return true, nil // local test identities have no persisted session.
	}
	if !uuid.IsCanonical(principal.UserID) || !uuid.IsCanonical(principal.SessionID) {
		return false, nil
	}
	var authEpoch int64
	err := tx.QueryRow(ctx, `SELECT auth_epoch FROM app_users WHERE user_id = $1::uuid AND is_active FOR SHARE`, principal.UserID).Scan(&authEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `
		SELECT session_id
		FROM app_sessions
		WHERE session_id = $1::uuid
		  AND user_id = $2::uuid
		  AND auth_epoch = $3
		  AND revoked_at IS NULL
		  AND expires_at > clock_timestamp()
		  AND idle_expires_at > clock_timestamp()
		FOR SHARE`, principal.SessionID, principal.UserID, authEpoch).Scan(new(string))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
