package auth

import (
	"context"
	"errors"
	"time"

	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const (
	refreshLeadTime      = time.Minute
	refreshLeaseDuration = 30 * time.Second
	refreshWaitDuration  = 5 * time.Second
	refreshPollInterval  = 50 * time.Millisecond
)

var (
	errSessionRevoked = errors.New("session was revoked")
	errSessionExpired = errors.New("session expired")
	errRefreshBusy    = errors.New("session refresh is still in progress")
)

type refreshClaim struct {
	leaseToken       string
	userID           string
	sessionID        string
	identityID       string
	authEpoch        int64
	generation       int64
	tokenExpiresAt   time.Time
	expiresAt        time.Time
	idleExpiresAt    time.Time
	tokensCiphertext []byte
	encryptionKeyID  string
	csrfTokenHash    []byte
	owner            bool
}

func (s *Server) refreshIfNeeded(ctx context.Context, session *activeSession) error {
	if session.tokenExpiresAt.After(time.Now().Add(refreshLeadTime)) {
		return nil
	}
	waitContext, cancel := context.WithTimeout(ctx, refreshWaitDuration)
	defer cancel()
	for {
		claim, err := s.claimRefresh(waitContext, session)
		if err != nil {
			return err
		}
		if claim.tokenExpiresAt.After(time.Now().Add(refreshLeadTime)) && !claim.owner {
			s.applyRefreshClaim(session, claim)
			return nil
		}
		if claim.owner {
			if err := s.performRefresh(waitContext, session, claim); err != nil {
				return err
			}
			return nil
		}
		if !claim.owner && claim.leaseToken == "" {
			s.applyRefreshClaim(session, claim)
			return nil
		}
		select {
		case <-waitContext.Done():
			return errRefreshBusy
		case <-time.After(refreshPollInterval):
		}
		// The next claim checks whether the first replica refreshed or released
		// the lease, then either observes its result or takes over an expired one.
	}
}

func (s *Server) claimRefresh(ctx context.Context, session *activeSession) (refreshClaim, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return refreshClaim{}, err
	}
	defer tx.Rollback(ctx)
	var currentEpoch int64
	var active bool
	err = tx.QueryRow(ctx, `SELECT auth_epoch, is_active FROM app_users WHERE user_id = $1::uuid FOR SHARE`, session.principal.UserID).Scan(&currentEpoch, &active)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return refreshClaim{}, errSessionRevoked
	}
	if err != nil {
		return refreshClaim{}, err
	}
	var claim refreshClaim
	claim.userID = session.principal.UserID
	claim.sessionID = session.principal.SessionID
	claim.authEpoch = currentEpoch
	var sessionEpoch int64
	var leaseToken *string
	var leaseUntil *time.Time
	var fresh bool
	var leaseActive bool
	err = tx.QueryRow(ctx, `
		SELECT identity_id::text, auth_epoch, refresh_generation, token_expires_at,
		       expires_at, idle_expires_at, tokens_ciphertext, encryption_key_id,
		       csrf_token_hash, refresh_lease_token::text, refresh_lease_until,
		       token_expires_at > clock_timestamp() + interval '1 minute',
		       COALESCE(refresh_lease_until > clock_timestamp(), false)
		FROM app_sessions
		WHERE session_id = $1::uuid AND user_id = $2::uuid
		FOR UPDATE`, session.principal.SessionID, session.principal.UserID).Scan(
		&claim.identityID, &sessionEpoch, &claim.generation, &claim.tokenExpiresAt,
		&claim.expiresAt, &claim.idleExpiresAt, &claim.tokensCiphertext,
		&claim.encryptionKeyID, &claim.csrfTokenHash, &leaseToken, &leaseUntil, &fresh, &leaseActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshClaim{}, errSessionRevoked
	}
	if err != nil {
		return refreshClaim{}, err
	}
	if sessionEpoch != currentEpoch {
		return refreshClaim{}, errSessionRevoked
	}
	var stillValid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_sessions WHERE session_id = $1::uuid AND revoked_at IS NULL AND expires_at > clock_timestamp() AND idle_expires_at > clock_timestamp())`, claim.sessionID).Scan(&stillValid); err != nil {
		return refreshClaim{}, err
	}
	if !stillValid {
		return refreshClaim{}, errSessionExpired
	}
	if fresh {
		if err := tx.Commit(ctx); err != nil {
			return refreshClaim{}, err
		}
		return claim, nil
	}
	if leaseActive && leaseToken != nil && leaseUntil != nil {
		claim.leaseToken = *leaseToken
		if err := tx.Commit(ctx); err != nil {
			return refreshClaim{}, err
		}
		return claim, nil
	}
	leaseTokenValue, err := uuid.New()
	if err != nil {
		return refreshClaim{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE app_sessions SET refresh_lease_token = $2::uuid, refresh_lease_until = clock_timestamp() + $3 * interval '1 second' WHERE session_id = $1::uuid`, claim.sessionID, leaseTokenValue, int64(refreshLeaseDuration.Seconds()))
	if err != nil {
		return refreshClaim{}, err
	}
	claim.leaseToken = leaseTokenValue
	claim.owner = true
	if err := tx.Commit(ctx); err != nil {
		return refreshClaim{}, err
	}
	return claim, nil
}

func (s *Server) performRefresh(ctx context.Context, session *activeSession, claim refreshClaim) error {
	bundle, err := s.decryptBundle(claim.encryptionKeyID, claim.tokensCiphertext)
	if err != nil || bundle.RefreshToken == "" {
		s.revokeRefreshClaim(ctx, claim)
		return errSessionRevoked
	}
	token, err := s.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: bundle.RefreshToken}).Token()
	if err != nil || token.AccessToken == "" || token.Expiry.IsZero() {
		s.revokeRefreshClaim(ctx, claim)
		return errSessionRevoked
	}
	if refreshedIDToken, ok := token.Extra("id_token").(string); ok && refreshedIDToken != "" {
		verified, verifyErr := s.verifier.Verify(ctx, refreshedIDToken)
		if verifyErr != nil {
			s.revokeRefreshClaim(ctx, claim)
			return errSessionRevoked
		}
		var expected struct {
			Issuer  string `json:"issuer"`
			Subject string `json:"subject"`
		}
		verifyErr = s.pool.QueryRow(ctx, `SELECT issuer, subject FROM user_identities WHERE identity_id = $1::uuid AND user_id = $2::uuid`, claim.identityID, claim.userID).Scan(&expected.Issuer, &expected.Subject)
		if verifyErr != nil || verified.Issuer != expected.Issuer || verified.Subject != expected.Subject {
			s.revokeRefreshClaim(ctx, claim)
			return errSessionRevoked
		}
	} else {
		s.revokeRefreshClaim(ctx, claim)
		return errSessionRevoked
	}
	if token.RefreshToken != "" {
		bundle.RefreshToken = token.RefreshToken
	}
	bundle.AccessToken = token.AccessToken
	ciphertext, keyID, err := s.encryptBundle(bundle)
	if err != nil {
		s.revokeRefreshClaim(ctx, claim)
		return err
	}
	if err := s.storeRefreshedTokens(ctx, claim, ciphertext, keyID, token.Expiry, session); err != nil {
		if !errors.Is(err, errSessionRevoked) {
			s.revokeRefreshClaim(ctx, claim)
		}
		return err
	}
	return nil
}

func (s *Server) storeRefreshedTokens(ctx context.Context, claim refreshClaim, ciphertext []byte, keyID string, tokenExpiry time.Time, session *activeSession) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentEpoch int64
	var active bool
	if err := tx.QueryRow(ctx, `SELECT auth_epoch, is_active FROM app_users WHERE user_id = $1::uuid FOR SHARE`, claim.userID).Scan(&currentEpoch, &active); err != nil {
		return err
	}
	if !active || currentEpoch != claim.authEpoch {
		return errSessionRevoked
	}
	var lockedSessionID string
	err = tx.QueryRow(ctx, `SELECT session_id::text FROM app_sessions WHERE session_id=$1::uuid AND user_id=$2::uuid AND refresh_lease_token=$3::uuid AND refresh_generation=$4 AND refresh_lease_until > clock_timestamp() AND revoked_at IS NULL AND expires_at > clock_timestamp() AND idle_expires_at > clock_timestamp() AND auth_epoch=$5 FOR UPDATE`, claim.sessionID, claim.userID, claim.leaseToken, claim.generation, claim.authEpoch).Scan(&lockedSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSessionRevoked
	}
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `
		UPDATE app_sessions
		SET tokens_ciphertext = $2,
		    encryption_key_id = $3,
		    token_expires_at = $4,
		    refresh_generation = refresh_generation + 1,
		    refresh_lease_token = NULL,
		    refresh_lease_until = NULL
		WHERE session_id = $1::uuid
		  AND refresh_lease_token = $5::uuid
		  AND refresh_generation = $6
		  AND refresh_lease_until > clock_timestamp()
		  AND revoked_at IS NULL
		  AND expires_at > clock_timestamp()
		  AND idle_expires_at > clock_timestamp()
		RETURNING refresh_generation, expires_at, idle_expires_at, csrf_token_hash, identity_id::text`,
		claim.sessionID, ciphertext, keyID, tokenExpiry, claim.leaseToken, claim.generation).Scan(
		&session.refreshGeneration, &session.expiresAt, &session.idleExpiresAt,
		&session.csrfTokenHash, &session.identityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSessionRevoked
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	session.tokenExpiresAt = tokenExpiry
	session.tokensCiphertext = ciphertext
	session.encryptionKeyID = keyID
	return nil
}

func (s *Server) revokeRefreshClaim(ctx context.Context, claim refreshClaim) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.pool.Exec(cleanupContext, `
		UPDATE app_sessions
		SET revoked_at = COALESCE(revoked_at, clock_timestamp()),
		    refresh_lease_token = NULL,
		    refresh_lease_until = NULL
		WHERE session_id = $1::uuid AND refresh_lease_token = $2::uuid AND refresh_generation = $3`,
		claim.sessionID, claim.leaseToken, claim.generation)
}

func (s *Server) applyRefreshClaim(session *activeSession, claim refreshClaim) {
	session.tokenExpiresAt = claim.tokenExpiresAt
	session.expiresAt = claim.expiresAt
	session.idleExpiresAt = claim.idleExpiresAt
	session.tokensCiphertext = claim.tokensCiphertext
	session.encryptionKeyID = claim.encryptionKeyID
	session.csrfTokenHash = claim.csrfTokenHash
	session.refreshGeneration = claim.generation
	session.identityID = claim.identityID
}
