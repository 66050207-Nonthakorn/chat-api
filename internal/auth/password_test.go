package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudchat/internal/config"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/testutil"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
)

type allowPublicAuth struct{}

func (allowPublicAuth) Allow(context.Context, ratelimit.Operation, string) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestSignupSendsCognitoSecretHashAndAttributes(t *testing.T) {
	server := &Server{
		config:        config.Config{AppOrigin: "https://chat.example.com", OIDCIssuer: "https://cognito-idp.ap-southeast-2.amazonaws.com/pool", OIDCClientID: "client", OIDCClientSecret: "secret"},
		publicLimiter: allowPublicAuth{},
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://cognito-idp.ap-southeast-2.amazonaws.com/" || r.Header.Get("X-Amz-Target") != "AWSCognitoIdentityProviderService.SignUp" {
				t.Errorf("unexpected Cognito request %s %s", r.URL, r.Header.Get("X-Amz-Target"))
			}
			var body struct {
				ClientID   string `json:"ClientId"`
				Username   string `json:"Username"`
				SecretHash string `json:"SecretHash"`
				Attributes []struct {
					Name  string
					Value string
				} `json:"UserAttributes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mac := hmac.New(sha256.New, []byte("secret"))
			_, _ = mac.Write([]byte("alice@example.comclient"))
			if body.ClientID != "client" || body.Username != "alice@example.com" || body.SecretHash != base64.StdEncoding.EncodeToString(mac.Sum(nil)) || len(body.Attributes) != 2 || body.Attributes[1].Value != "Alice" {
				t.Errorf("Cognito signup request did not preserve verified input")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})},
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(`{"email":"Alice@Example.com","password":"Password1!","name":"Alice"}`))
	req.Header.Set("Origin", "https://chat.example.com")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("signup status = %d, body = %s", response.Code, response.Body)
	}
}

func TestPasswordAuthRejectsCrossOrigin(t *testing.T) {
	server := &Server{config: config.Config{AppOrigin: "https://chat.example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", strings.NewReader(`{"email":"victim@example.com","password":"password"}`))
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login status = %d, want 403", response.Code)
	}
}

func TestFindOrCreateIdentityConcurrentAndDisabled(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	pool := testutil.IsolatedDatabase(t, databaseURL, "public_auth_")
	server := &Server{pool: pool}
	const issuer = "https://example.com"
	const subject = "subject-123"
	const workers = 8
	var wg sync.WaitGroup
	users := make(chan preparedUser, workers)
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user, err := server.findOrCreateIdentity(context.Background(), issuer, subject, "Alice")
			if err != nil {
				failures <- err
				return
			}
			users <- user
		}()
	}
	wg.Wait()
	close(users)
	close(failures)
	for err := range failures {
		t.Errorf("concurrent identity provisioning: %v", err)
	}
	var first preparedUser
	for user := range users {
		if first.userID == "" {
			first = user
		}
		if user.userID != first.userID || user.identityID != first.identityID || user.displayName != "Alice" {
			t.Fatalf("concurrent login produced different identity: %#v vs %#v", first, user)
		}
	}
	if first.userID == "" {
		t.Fatal("no user created")
	}
	if _, err := pool.Exec(context.Background(), "UPDATE app_users SET is_active = false WHERE user_id = $1::uuid", first.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.findOrCreateIdentity(context.Background(), issuer, subject, "Another"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled identity returned %v, want pgx.ErrNoRows", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM user_identities WHERE issuer=$1 AND subject=$2", issuer, subject).Scan(&count); err != nil || count != 1 {
		t.Fatalf("identity count = %d, error = %v", count, err)
	}
}

func TestPasswordLoginUnverifiedEmailAndSession(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	pool := testutil.IsolatedDatabase(t, databaseURL, "password_login_")
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://cognito-idp.ap-southeast-2.amazonaws.com/pool"
	cfg := config.Config{
		Environment: config.EnvironmentProduction, AppOrigin: "https://chat.example.com",
		OIDCIssuer: issuer, OIDCClientID: "client", OIDCClientSecret: "secret",
		SessionEncryptionCurrentKeyID: "key", SessionEncryptionKeys: map[string][]byte{"key": []byte("0123456789abcdef0123456789abcdef")},
	}
	keys, err := newKeyRing(cfg)
	if err != nil {
		t.Fatal(err)
	}
	verifiedEmail := true
	server := &Server{
		config: cfg, pool: pool, keys: keys, sessionRules: NewSessionRules(pool),
		publicLimiter: allowPublicAuth{},
		verifier:      oidc.NewVerifier(issuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&privateKey.PublicKey}}, &oidc.Config{ClientID: "client"}),
	}
	server.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
		claims, _ := json.Marshal(map[string]any{
			"iss": issuer, "sub": "new-user", "aud": "client",
			"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
			"token_use": "id", "email": "alice@example.com", "email_verified": verifiedEmail, "name": "Alice",
		})
		unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(unsigned))
		sig, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"AuthenticationResult": map[string]any{
			"IdToken":     unsigned + "." + base64.RawURLEncoding.EncodeToString(sig),
			"AccessToken": "access-token", "RefreshToken": "refresh-token", "ExpiresIn": 3600,
		}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}
	login := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/auth/password/login", strings.NewReader(`{"email":"alice@example.com","password":"Password1!"}`))
		req.Header.Set("Origin", cfg.AppOrigin)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		return response
	}
	verifiedEmail = false
	if got := login().Code; got != http.StatusOK {
		t.Fatalf("unverified login = %d, want 200", got)
	}
	verifiedEmail = true
	response := login()
	if response.Code != http.StatusOK {
		t.Fatalf("verified login = %d, %s", response.Code, response.Body)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != productionSessionCookie || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}
	if strings.Contains(response.Body.String(), "access-token") || strings.Contains(response.Body.String(), "refresh-token") {
		t.Fatal("response exposed Cognito tokens")
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	sessionRequest.AddCookie(cookies[0])
	sessionResponse := httptest.NewRecorder()
	server.Middleware(server).ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK || !strings.Contains(sessionResponse.Body.String(), "Alice") {
		t.Fatalf("session = %d, %s", sessionResponse.Code, sessionResponse.Body)
	}
	if got := login().Code; got != http.StatusOK {
		t.Fatalf("repeat login = %d", got)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM user_identities WHERE issuer = $1 AND subject = $2", issuer, "new-user").Scan(&count); err != nil || count != 1 {
		t.Fatalf("identity count = %d, error = %v", count, err)
	}
}
