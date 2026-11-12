package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"cloudchat/internal/apierror"
	"cloudchat/internal/httpjson"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/uuid"

	"github.com/jackc/pgx/v5"
)

type passwordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Code     string `json:"code"`
}

type cognitoResponse struct {
	Type                 string `json:"__type"`
	ChallengeName        string `json:"ChallengeName"`
	AuthenticationResult struct {
		AccessToken  string `json:"AccessToken"`
		IDToken      string `json:"IdToken"`
		RefreshToken string `json:"RefreshToken"`
		ExpiresIn    int    `json:"ExpiresIn"`
	} `json:"AuthenticationResult"`
}

func (s *Server) passwordAuth(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != s.config.AppOrigin {
		apierror.Write(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Origin is not allowed", false, nil)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		apierror.Write(w, http.StatusUnsupportedMediaType, "INVALID_REQUEST", "JSON is required", false, nil)
		return
	}
	var input passwordRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request", false, nil)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request", false, nil)
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 {
		apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Enter a valid email address", false, nil)
		return
	}
	if s.publicLimiter == nil {
		dependencyUnavailable(w)
		return
	}
	for _, limit := range []struct {
		op  ratelimit.Operation
		key string
	}{
		{ratelimit.PublicAuthGlobal, "all"},
		{ratelimit.PublicAuthAccount, input.Email},
	} {
		decision, err := s.publicLimiter.Allow(r.Context(), limit.op, limit.key)
		if err != nil {
			dependencyUnavailable(w)
			return
		}
		if !decision.Allowed {
			w.Header().Set("Retry-After", fmt.Sprint(decision.RetryAfterSeconds))
			apierror.Write(w, http.StatusTooManyRequests, "RATE_LIMITED", "Please try again later", true, nil)
			return
		}
	}
	mac := hmac.New(sha256.New, []byte(s.config.OIDCClientSecret))
	_, _ = mac.Write([]byte(input.Email + s.config.OIDCClientID))
	secretHash := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	var operation string
	body := map[string]any{"ClientId": s.config.OIDCClientID, "SecretHash": secretHash, "Username": input.Email}
	switch r.URL.Path {
	case "/auth/signup":
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 || len(input.Password) < 8 || len(input.Password) > 256 {
			apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Enter a display name and a valid password", false, nil)
			return
		}
		operation = "SignUp"
		body["Password"] = input.Password
		body["UserAttributes"] = []map[string]string{{"Name": "email", "Value": input.Email}, {"Name": "name", "Value": input.Name}}
	case "/auth/signup/confirm":
		if len(input.Code) != 6 || strings.Trim(input.Code, "0123456789") != "" {
			apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Enter the six-digit verification code", false, nil)
			return
		}
		operation = "ConfirmSignUp"
		body["ConfirmationCode"] = input.Code
	case "/auth/signup/resend":
		operation = "ResendConfirmationCode"
	case "/auth/password/login":
		if input.Password == "" || len(input.Password) > 256 {
			apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Enter your email and password", false, nil)
			return
		}
		operation = "InitiateAuth"
		body = map[string]any{
			"ClientId": s.config.OIDCClientID, "AuthFlow": "USER_PASSWORD_AUTH",
			"AuthParameters": map[string]string{"USERNAME": input.Email, "PASSWORD": input.Password, "SECRET_HASH": secretHash},
		}
	}
	result, status, err := s.cognitoRequest(r.Context(), operation, body)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	if status != http.StatusOK {
		code := result.Type
		if index := strings.LastIndex(code, "#"); index >= 0 {
			code = code[index+1:]
		}
		switch code {
		case "UserNotConfirmedException":
			apierror.Write(w, http.StatusForbidden, "EMAIL_NOT_VERIFIED", "Verify your email before signing in", false, nil)
		case "UsernameExistsException":
			apierror.Write(w, http.StatusConflict, "ACCOUNT_EXISTS", "An account with this email already exists", false, nil)
		case "InvalidPasswordException":
			apierror.Write(w, http.StatusBadRequest, "INVALID_PASSWORD", "Password must include uppercase, lowercase, a number, and a symbol", false, nil)
		case "CodeMismatchException", "ExpiredCodeException":
			apierror.Write(w, http.StatusBadRequest, "INVALID_CODE", "The verification code is invalid or expired", false, nil)
		case "TooManyRequestsException", "LimitExceededException":
			apierror.Write(w, http.StatusTooManyRequests, "RATE_LIMITED", "Please try again later", true, nil)
		case "NotAuthorizedException", "UserNotFoundException":
			apierror.Write(w, http.StatusUnauthorized, "LOGIN_FAILED", "Email or password is incorrect", false, nil)
		default:
			dependencyUnavailable(w)
		}
		return
	}
	if operation != "InitiateAuth" {
		httpjson.Write(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	tokens := result.AuthenticationResult
	if result.ChallengeName != "" || tokens.IDToken == "" || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ExpiresIn <= 0 {
		apierror.Write(w, http.StatusUnauthorized, "LOGIN_FAILED", "This account requires an additional sign-in step", false, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	idToken, err := s.verifier.Verify(ctx, tokens.IDToken)
	if err != nil {
		apierror.Write(w, http.StatusUnauthorized, "LOGIN_FAILED", "Login could not be completed", false, nil)
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		TokenUse string `json:"token_use"`
	}
	if idToken.Claims(&claims) != nil || claims.TokenUse != "id" || !strings.EqualFold(claims.Email, input.Email) || idToken.Subject == "" {
		apierror.Write(w, http.StatusUnauthorized, "LOGIN_FAILED", "Login could not be completed", false, nil)
		return
	}
	identity, err := s.findOrCreateIdentity(ctx, idToken.Issuer, idToken.Subject, claims.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		apierror.Write(w, http.StatusForbidden, "ACCOUNT_DISABLED", "This account is disabled", false, nil)
		return
	}
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	csrf, err := randomOpaque(32)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	ciphertext, keyID, err := s.encryptBundle(tokenBundle{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, CSRFToken: csrf})
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	sessionSecret, err := randomOpaque(32)
	if err != nil {
		dependencyUnavailable(w)
		return
	}
	if err = s.revokePreviousCookie(ctx, r); err != nil {
		dependencyUnavailable(w)
		return
	}
	if err = s.createSession(ctx, identity, sessionSecret, csrf, ciphertext, keyID, time.Now().Add(time.Duration(tokens.ExpiresIn)*time.Second)); err != nil {
		dependencyUnavailable(w)
		return
	}
	http.SetCookie(w, s.sessionCookie(sessionSecret))
	httpjson.Write(w, http.StatusOK, map[string]any{"user": map[string]string{"user_id": identity.userID, "display_name": identity.displayName}, "csrf_token": csrf})
}

func (s *Server) cognitoRequest(ctx context.Context, operation string, value any) (cognitoResponse, int, error) {
	var result cognitoResponse
	issuer, err := url.Parse(s.config.OIDCIssuer)
	if err != nil || issuer.Scheme != "https" || !strings.HasPrefix(issuer.Hostname(), "cognito-idp.") || !strings.HasSuffix(issuer.Hostname(), ".amazonaws.com") || issuer.Port() != "" {
		return result, 0, errors.New("Cognito issuer is invalid")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return result, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+issuer.Hostname()+"/", bytes.NewReader(payload))
	if err != nil {
		return result, 0, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+operation)
	resp, err := s.client.Do(req)
	if err != nil {
		return result, 0, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	return result, resp.StatusCode, err
}

func (s *Server) findOrCreateIdentity(ctx context.Context, issuer, subject, name string) (preparedUser, error) {
	var user preparedUser
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return user, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", issuer+"|"+subject); err != nil {
		return user, err
	}
	var active bool
	err = tx.QueryRow(ctx, `
        SELECT i.identity_id::text, u.user_id::text, u.display_name, u.is_active
        FROM user_identities i JOIN app_users u ON u.user_id = i.user_id
        WHERE i.issuer = $1 AND i.subject = $2`, issuer, subject).Scan(&user.identityID, &user.userID, &user.displayName, &active)
	if err == nil {
		if !active {
			return preparedUser{}, pgx.ErrNoRows
		}
		return user, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return user, err
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		name = "Chat user"
	}
	user.userID, err = uuid.New()
	if err != nil {
		return user, err
	}
	user.identityID, err = uuid.New()
	if err != nil {
		return user, err
	}
	user.displayName = name
	if _, err = tx.Exec(ctx, "INSERT INTO app_users (user_id, display_name) VALUES ($1::uuid, $2)", user.userID, name); err != nil {
		return preparedUser{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO user_identities (identity_id, user_id, issuer, subject) VALUES ($1::uuid, $2::uuid, $3, $4)", user.identityID, user.userID, issuer, subject); err != nil {
		return preparedUser{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return preparedUser{}, err
	}
	return user, nil
}
