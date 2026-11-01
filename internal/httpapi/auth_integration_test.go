package httpapi_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
	"cloudchat/internal/realtime"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOIDCLoginCreatesOpaqueSessionAndConsumesCallbackOnce(t *testing.T) {
	base := newTestServer(t)
	provider := newFakeOIDC(t)
	const subject = "cognito-subject-a"
	seedOIDCIdentity(t, base.pool, provider.issuer, subject, demo.UserAID)

	cfg := authTestConfig(provider)
	authServer, err := auth.NewServer(context.Background(), cfg, base.pool)
	if err != nil {
		t.Fatalf("create auth server: %v", err)
	}
	app := httptest.NewServer(httpapi.NewHandlerWithAuth(cfg, base.pool, nil, nil, authServer))
	t.Cleanup(app.Close)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	loginResponse, err := client.Get(app.URL + "/auth/login")
	if err != nil {
		t.Fatalf("start login: %v", err)
	}
	defer loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d, want 302", loginResponse.StatusCode)
	}
	authorizeURL, err := url.Parse(loginResponse.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorization redirect: %v", err)
	}
	authorize := authorizeURL.Query()
	state := authorize.Get("state")
	nonce := authorize.Get("nonce")
	challenge := authorize.Get("code_challenge")
	if state == "" || nonce == "" || challenge == "" || authorize.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization parameters omit state, nonce, or S256 PKCE: %v", authorize)
	}
	if authorize.Get("client_id") != cfg.OIDCClientID || authorize.Get("redirect_uri") != cfg.OIDCRedirectURL {
		t.Fatalf("authorization request has wrong client or callback: %v", authorize)
	}
	var loginCookie *http.Cookie
	for _, cookie := range loginResponse.Cookies() {
		if cookie.Name == "chat_login" {
			loginCookie = cookie
		}
	}
	if loginCookie == nil || !loginCookie.HttpOnly || loginCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login browser-binding cookie = %+v, want HttpOnly SameSite=Lax", loginCookie)
	}

	const code = "authorized-code-a"
	provider.allowCode(code, subject, nonce, challenge)
	callbackURL := app.URL + "/auth/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	wrongBindingRequest, _ := http.NewRequest(http.MethodGet, callbackURL, nil)
	wrongBindingRequest.AddCookie(&http.Cookie{Name: loginCookie.Name, Value: "wrong-browser-binding"})
	wrongBindingResponse, err := client.Do(wrongBindingRequest)
	if err != nil {
		t.Fatalf("callback with wrong browser binding: %v", err)
	}
	_ = wrongBindingResponse.Body.Close()
	if wrongBindingResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong browser binding status = %d, want 401", wrongBindingResponse.StatusCode)
	}

	callbackRequest, _ := http.NewRequest(http.MethodGet, callbackURL, nil)
	callbackRequest.AddCookie(loginCookie)
	callbackResponse, err := client.Do(callbackRequest)
	if err != nil {
		t.Fatalf("complete callback: %v", err)
	}
	defer callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusSeeOther || callbackResponse.Header.Get("Location") != "/" {
		t.Fatalf("callback status/location = %d %q, want 303 /", callbackResponse.StatusCode, callbackResponse.Header.Get("Location"))
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callbackResponse.Cookies() {
		if cookie.Name == "chat_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode || len(sessionCookie.Value) < 40 {
		t.Fatalf("session cookie = %+v, want opaque HttpOnly SameSite=Lax cookie", sessionCookie)
	}
	if strings.Contains(sessionCookie.Value, "access-token") || strings.Contains(sessionCookie.Value, "refresh-token") {
		t.Fatal("session cookie contains provider token material")
	}
	if callbackResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("callback Cache-Control = %q, want no-store", callbackResponse.Header.Get("Cache-Control"))
	}

	provider.verifyLastCodeVerifier(t, code, challenge)
	sessionRequest, _ := http.NewRequest(http.MethodGet, app.URL+"/api/v1/session", nil)
	sessionRequest.AddCookie(sessionCookie)
	sessionResponse, err := client.Do(sessionRequest)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	body, _ := io.ReadAll(sessionResponse.Body)
	_ = sessionResponse.Body.Close()
	if sessionResponse.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", sessionResponse.StatusCode, body)
	}
	if strings.Contains(string(body), "access-token") || strings.Contains(string(body), "refresh-token") {
		t.Fatal("session response exposed provider tokens")
	}
	var session struct {
		User struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
		Session struct {
			ExpiresAt     time.Time `json:"expires_at"`
			IdleExpiresAt time.Time `json:"idle_expires_at"`
		} `json:"session"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session.User.UserID != demo.UserAID || session.User.DisplayName == "" || session.CSRFToken == "" || session.Session.ExpiresAt.IsZero() || session.Session.IdleExpiresAt.IsZero() {
		t.Fatalf("session response = %+v", session)
	}

	var ciphertext []byte
	var keyID string
	tokenHash := sha256.Sum256([]byte(sessionCookie.Value))
	if err := base.pool.QueryRow(context.Background(), `SELECT tokens_ciphertext, encryption_key_id FROM app_sessions WHERE token_hash = $1`, tokenHash[:]).Scan(&ciphertext, &keyID); err != nil {
		t.Fatalf("read persisted session token envelope: %v", err)
	}
	if keyID != cfg.SessionEncryptionCurrentKeyID || strings.Contains(string(ciphertext), "access-token") || strings.Contains(string(ciphertext), "refresh-token") {
		t.Fatalf("provider tokens are not stored in the expected encrypted envelope (key ID %q)", keyID)
	}

	replayRequest, _ := http.NewRequest(http.MethodGet, callbackURL, nil)
	replayRequest.AddCookie(loginCookie)
	replayResponse, err := client.Do(replayRequest)
	if err != nil {
		t.Fatalf("replay callback: %v", err)
	}
	_ = replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed callback status = %d, want 401", replayResponse.StatusCode)
	}
}

func TestSessionWritesRequireExactOriginAndBoundCSRF(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, csrf := app.login(t, demo.UserAID, "csrf-session")
	for _, testCase := range []struct {
		name   string
		origin string
		csrf   string
		want   int
	}{
		{name: "missing origin", csrf: csrf, want: http.StatusForbidden},
		{name: "null origin", origin: "null", csrf: csrf, want: http.StatusForbidden},
		{name: "wrong origin", origin: "http://evil.test", csrf: csrf, want: http.StatusForbidden},
		{name: "missing csrf", origin: app.config.AppOrigin, want: http.StatusForbidden},
		{name: "wrong csrf", origin: app.config.AppOrigin, csrf: "another-session-token", want: http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodPost, app.server.URL+"/auth/logout", nil)
			request.AddCookie(cookie)
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			if testCase.csrf != "" {
				request.Header.Set("X-CSRF-Token", testCase.csrf)
			}
			response, err := app.client.Do(request)
			if err != nil {
				t.Fatalf("logout request: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != testCase.want {
				t.Fatalf("logout status = %d, want %d", response.StatusCode, testCase.want)
			}
		})
	}

	request, _ := http.NewRequest(http.MethodPost, app.server.URL+"/auth/logout", nil)
	request.AddCookie(cookie)
	request.Header.Set("Origin", app.config.AppOrigin)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("valid logout request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("valid logout status = %d, want 204", response.StatusCode)
	}
	if len(response.Cookies()) == 0 || response.Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear the session cookie: %+v", response.Cookies())
	}
	if got := app.sessionStatus(t, cookie); got != http.StatusUnauthorized {
		t.Fatalf("session after logout status = %d, want 401", got)
	}
}

func TestIdleAndAbsoluteSessionExpiryAreEnforced(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	idleCookie, _ := app.login(t, demo.UserAID, "idle-session")
	idleHash := sha256.Sum256([]byte(idleCookie.Value))
	if _, err := app.base.pool.Exec(context.Background(), `UPDATE app_sessions SET created_at = clock_timestamp() - interval '1 hour', idle_expires_at = clock_timestamp() - interval '1 second' WHERE token_hash = $1`, idleHash[:]); err != nil {
		t.Fatalf("expire idle session fixture: %v", err)
	}
	if got := app.sessionStatus(t, idleCookie); got != http.StatusUnauthorized {
		t.Fatalf("idle-expired session status = %d, want 401", got)
	}

	absoluteCookie, _ := app.login(t, demo.UserAID, "absolute-session")
	absoluteHash := sha256.Sum256([]byte(absoluteCookie.Value))
	if _, err := app.base.pool.Exec(context.Background(), `
		UPDATE app_sessions
		SET created_at = clock_timestamp() - interval '3 hours',
		    expires_at = clock_timestamp() - interval '1 hour',
		    idle_expires_at = clock_timestamp() - interval '2 hours'
		WHERE token_hash = $1`, absoluteHash[:]); err != nil {
		t.Fatalf("expire absolute session fixture: %v", err)
	}
	if got := app.sessionStatus(t, absoluteCookie); got != http.StatusUnauthorized {
		t.Fatalf("absolute-expired session status = %d, want 401", got)
	}
}

func TestLogoutAllRevokesEverySession(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	firstCookie, firstCSRF := app.login(t, demo.UserAID, "logout-all-first")
	secondCookie, _ := app.login(t, demo.UserAID, "logout-all-second")
	if got := app.sessionStatus(t, secondCookie); got != http.StatusOK {
		t.Fatalf("second session before logout-all status = %d, want 200", got)
	}
	request, _ := http.NewRequest(http.MethodPost, app.server.URL+"/auth/logout-all", nil)
	request.AddCookie(firstCookie)
	request.Header.Set("Origin", app.config.AppOrigin)
	request.Header.Set("X-CSRF-Token", firstCSRF)
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("logout-all request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout-all status = %d, want 204", response.StatusCode)
	}
	if got := app.sessionStatus(t, firstCookie); got != http.StatusUnauthorized {
		t.Fatalf("first session after logout-all status = %d, want 401", got)
	}
	if got := app.sessionStatus(t, secondCookie); got != http.StatusUnauthorized {
		t.Fatalf("second session after logout-all status = %d, want 401", got)
	}
	request, _ = http.NewRequest(http.MethodPost, app.server.URL+"/api/v1/rooms/"+demo.GroupRoomID+"/messages", strings.NewReader(`{"client_message_id":"99999999-9999-4999-8999-999999999999","text":"must not send"}`))
	request.AddCookie(firstCookie)
	request.Header.Set("Origin", app.config.AppOrigin)
	request.Header.Set("X-CSRF-Token", firstCSRF)
	request.Header.Set("Content-Type", "application/json")
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatalf("send after logout-all: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("send after logout-all status = %d, want 401", response.StatusCode)
	}
}

func TestSuccessfulLoginRotatesExistingBrowserSession(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	oldCookie, _ := app.login(t, demo.UserAID, "old-browser-session")
	request, _ := http.NewRequest(http.MethodGet, app.server.URL+"/auth/login", nil)
	request.AddCookie(oldCookie)
	loginResponse, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("start replacement login: %v", err)
	}
	_ = loginResponse.Body.Close()
	params, _ := url.Parse(loginResponse.Header.Get("Location"))
	query := params.Query()
	var binding *http.Cookie
	for _, cookie := range loginResponse.Cookies() {
		if cookie.Name == "chat_login" {
			binding = cookie
		}
	}
	if binding == nil {
		t.Fatal("replacement login did not set browser-binding cookie")
	}
	code := "replacement-browser-code"
	app.provider.allowCode(code, "cognito-subject-a", query.Get("nonce"), query.Get("code_challenge"))
	callbackURL := app.server.URL + "/auth/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(query.Get("state"))
	callback, _ := http.NewRequest(http.MethodGet, callbackURL, nil)
	callback.AddCookie(binding)
	callback.AddCookie(oldCookie)
	response, err := app.client.Do(callback)
	if err != nil {
		t.Fatalf("complete replacement login: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("replacement callback status = %d, want 303", response.StatusCode)
	}
	var newCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == "chat_session" {
			newCookie = cookie
		}
	}
	if newCookie == nil {
		t.Fatal("replacement callback did not set a session cookie")
	}
	if got := app.sessionStatus(t, oldCookie); got != http.StatusUnauthorized {
		t.Fatalf("old browser session status = %d, want 401", got)
	}
	if got := app.sessionStatus(t, newCookie); got != http.StatusOK {
		t.Fatalf("replacement browser session status = %d, want 200", got)
	}
}

func TestCallbackRejectsNonceMismatchAndUnknownIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		invite     bool
		wrongNonce bool
		expired    bool
		want       int
	}{
		{name: "nonce mismatch", invite: true, wrongNonce: true, want: http.StatusUnauthorized},
		{name: "unknown issuer subject", invite: false, want: http.StatusForbidden},
		{name: "expired state", invite: true, expired: true, want: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := newAuthIntegrationApp(t, testCase.invite)
			loginResponse, err := app.client.Get(app.server.URL + "/auth/login")
			if err != nil {
				t.Fatalf("start login: %v", err)
			}
			_ = loginResponse.Body.Close()
			params, _ := url.Parse(loginResponse.Header.Get("Location"))
			query := params.Query()
			var binding *http.Cookie
			for _, cookie := range loginResponse.Cookies() {
				if cookie.Name == "chat_login" {
					binding = cookie
				}
			}
			if binding == nil {
				t.Fatal("login did not set browser-binding cookie")
			}
			subject := "uninvited-subject"
			if testCase.invite {
				subject = "cognito-subject-a"
			}
			nonce := query.Get("nonce")
			if testCase.wrongNonce {
				nonce = nonce + "-changed"
			}
			code := "edge-code-" + strings.ReplaceAll(testCase.name, " ", "-")
			app.provider.allowCode(code, subject, nonce, query.Get("code_challenge"))
			if testCase.expired {
				stateHash := sha256.Sum256([]byte(query.Get("state")))
				if _, err := app.base.pool.Exec(context.Background(), `UPDATE oauth_login_attempts SET created_at = clock_timestamp() - interval '10 minutes', expires_at = clock_timestamp() - interval '1 second' WHERE state_hash = $1`, stateHash[:]); err != nil {
					t.Fatalf("expire OAuth login state fixture: %v", err)
				}
			}
			callbackURL := app.server.URL + "/auth/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(query.Get("state"))
			request, _ := http.NewRequest(http.MethodGet, callbackURL, nil)
			request.AddCookie(binding)
			response, err := app.client.Do(request)
			if err != nil {
				t.Fatalf("callback: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != testCase.want {
				t.Fatalf("callback status = %d, want %d", response.StatusCode, testCase.want)
			}
			var sessions int
			if err := app.base.pool.QueryRow(context.Background(), `SELECT count(*) FROM app_sessions`).Scan(&sessions); err != nil {
				t.Fatalf("count persisted sessions: %v", err)
			}
			if sessions != 0 {
				t.Fatalf("failed callback created %d sessions", sessions)
			}
		})
	}
}

func TestRefreshIsCoordinatedAcrossTwoHTTPReplicas(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, _ := app.login(t, demo.UserAID, "refresh-session")
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	if _, err := app.base.pool.Exec(context.Background(), `UPDATE app_sessions SET token_expires_at = clock_timestamp() - interval '1 second' WHERE token_hash = $1`, tokenHash[:]); err != nil {
		t.Fatalf("expire provider token fixture: %v", err)
	}
	app.provider.mu.Lock()
	app.provider.refreshDelay = 150 * time.Millisecond
	app.provider.mu.Unlock()

	secondAuthServer, err := auth.NewServer(context.Background(), app.config, app.base.pool)
	if err != nil {
		t.Fatalf("create second replica auth service: %v", err)
	}
	secondReplica := httptest.NewServer(httpapi.NewHandlerWithAuth(app.config, app.base.pool, nil, nil, secondAuthServer))
	t.Cleanup(secondReplica.Close)

	status := make(chan int, 2)
	for _, endpoint := range []string{app.server.URL, secondReplica.URL} {
		go func(endpoint string) {
			request, _ := http.NewRequest(http.MethodGet, endpoint+"/api/v1/session", nil)
			request.AddCookie(cookie)
			response, requestErr := app.client.Do(request)
			if requestErr != nil {
				status <- 0
				return
			}
			_ = response.Body.Close()
			status <- response.StatusCode
		}(endpoint)
	}
	for range 2 {
		if got := <-status; got != http.StatusOK {
			t.Fatalf("replica session response status = %d, want 200", got)
		}
	}
	app.provider.mu.Lock()
	refreshCalls := app.provider.refreshCalls
	app.provider.mu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("provider refresh requests = %d, want exactly one across replicas", refreshCalls)
	}
}

func TestLogoutAllDuringRefreshCannotRestoreRevokedSession(t *testing.T) {
	app := newAuthIntegrationApp(t, true)
	cookie, csrf := app.login(t, demo.UserAID, "revoked-refresh-session")
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	if _, err := app.base.pool.Exec(context.Background(), `UPDATE app_sessions SET token_expires_at = clock_timestamp() - interval '1 second' WHERE token_hash = $1`, tokenHash[:]); err != nil {
		t.Fatalf("expire provider token fixture: %v", err)
	}
	app.provider.mu.Lock()
	app.provider.refreshDelay = 250 * time.Millisecond
	app.provider.mu.Unlock()
	refreshResult := make(chan int, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, app.server.URL+"/api/v1/session", nil)
		request.AddCookie(cookie)
		response, err := app.client.Do(request)
		if err != nil {
			refreshResult <- 0
			return
		}
		_ = response.Body.Close()
		refreshResult <- response.StatusCode
	}()
	select {
	case <-app.provider.refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("provider refresh did not start")
	}

	logout, _ := http.NewRequest(http.MethodPost, app.server.URL+"/auth/logout-all", nil)
	logout.AddCookie(cookie)
	logout.Header.Set("Origin", app.config.AppOrigin)
	logout.Header.Set("X-CSRF-Token", csrf)
	response, err := app.client.Do(logout)
	if err != nil {
		t.Fatalf("logout all during refresh: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout-all status = %d, want 204", response.StatusCode)
	}
	if got := <-refreshResult; got != http.StatusUnauthorized {
		t.Fatalf("in-flight refresh response status = %d, want 401 after revocation", got)
	}
	var generation int64
	var revokedAt *time.Time
	if err := app.base.pool.QueryRow(context.Background(), `SELECT refresh_generation, revoked_at FROM app_sessions WHERE token_hash = $1`, tokenHash[:]).Scan(&generation, &revokedAt); err != nil {
		t.Fatalf("read revoked refresh session: %v", err)
	}
	if generation != 0 || revokedAt == nil {
		t.Fatalf("revoked session refresh state = generation %d, revoked_at %v", generation, revokedAt)
	}
}

func TestSessionWebSocketPingTouchesIdleAndLogoutAllClosesIt(t *testing.T) {
	options := realtime.Options{SessionRecheckInterval: 25 * time.Millisecond}
	env := newWebSocketTestEnv(t, options)
	provider := newFakeOIDC(t)
	seedOIDCIdentity(t, env.pool, provider.issuer, "cognito-subject-a", demo.UserAID)
	cfg := authTestConfig(provider)
	cfg.ValkeyURL = strings.TrimSpace(os.Getenv("TEST_VALKEY_URL"))
	authServer, err := auth.NewServer(context.Background(), cfg, env.pool)
	if err != nil {
		t.Fatalf("create auth server: %v", err)
	}
	hub, err := realtime.NewServer(context.Background(), env.pool, env.valkey, cfg, options)
	if err != nil {
		t.Fatalf("create authenticated WebSocket service: %v", err)
	}
	t.Cleanup(hub.Close)
	server := httptest.NewServer(httpapi.NewHandlerWithAuth(cfg, env.pool, nil, hub, authServer))
	t.Cleanup(server.Close)
	app := &authIntegrationApp{
		base: env.test, server: server,
		client: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		config: cfg, provider: provider,
	}
	cookie, csrf := app.login(t, demo.UserAID, "websocket-session")
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	if _, err := env.pool.Exec(context.Background(), `UPDATE app_sessions SET idle_expires_at = clock_timestamp() + interval '1 minute' WHERE token_hash = $1`, tokenHash[:]); err != nil {
		t.Fatalf("prepare WebSocket idle deadline: %v", err)
	}
	var idleBefore time.Time
	if err := env.pool.QueryRow(context.Background(), `SELECT idle_expires_at FROM app_sessions WHERE token_hash = $1`, tokenHash[:]).Scan(&idleBefore); err != nil {
		t.Fatalf("read WebSocket idle deadline: %v", err)
	}

	websocketURL, _ := url.Parse(server.URL)
	websocketURL.Scheme = "ws"
	websocketURL.Path = "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, rejectedResponse, rejectedErr := websocket.Dial(ctx, websocketURL.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": {"http://attacker.test"},
			"Cookie": {cookie.Name + "=" + cookie.Value},
		},
		Subprotocols: []string{"chat.v1"},
	})
	if rejectedErr == nil || rejectedResponse == nil || rejectedResponse.StatusCode != http.StatusForbidden {
		if rejectedResponse != nil && rejectedResponse.Body != nil {
			_ = rejectedResponse.Body.Close()
		}
		t.Fatalf("WebSocket wrong-Origin response = status %v, error %v; want 403", rejectedResponse, rejectedErr)
	}
	_ = rejectedResponse.Body.Close()
	connection, response, err := websocket.Dial(ctx, websocketURL.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": {cfg.AppOrigin},
			"Cookie": {cookie.Name + "=" + cookie.Value},
		},
		Subprotocols: []string{"chat.v1"},
	})
	if err != nil {
		if response != nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			t.Fatalf("dial session WebSocket: %v (status %d, body %s)", err, response.StatusCode, body)
		}
		t.Fatalf("dial session WebSocket: %v", err)
	}
	defer connection.CloseNow()
	if err := connection.Write(ctx, websocket.MessageText, []byte(`{"type":"ping","request_id":"77777777-7777-4777-8777-777777777777"}`)); err != nil {
		t.Fatalf("send WebSocket ping: %v", err)
	}
	_, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatalf("read WebSocket pong: %v", err)
	}
	var pong struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &pong); err != nil || pong.Type != "pong" {
		t.Fatalf("WebSocket ping response = %s, want pong", payload)
	}
	var idleAfter time.Time
	if err := env.pool.QueryRow(context.Background(), `SELECT idle_expires_at FROM app_sessions WHERE token_hash = $1`, tokenHash[:]).Scan(&idleAfter); err != nil {
		t.Fatalf("read refreshed WebSocket idle deadline: %v", err)
	}
	if !idleAfter.After(idleBefore) {
		t.Fatalf("WebSocket ping idle deadline = %s, want after %s", idleAfter, idleBefore)
	}

	logout, _ := http.NewRequest(http.MethodPost, server.URL+"/auth/logout-all", nil)
	logout.AddCookie(cookie)
	logout.Header.Set("Origin", cfg.AppOrigin)
	logout.Header.Set("X-CSRF-Token", csrf)
	logoutResponse, err := app.client.Do(logout)
	if err != nil {
		t.Fatalf("logout all WebSocket session: %v", err)
	}
	_ = logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("logout-all status = %d, want 204", logoutResponse.StatusCode)
	}
	_, _, err = connection.Read(ctx)
	if code := websocket.CloseStatus(err); code != 4401 {
		t.Fatalf("WebSocket close status = %d (error %v), want 4401 after session revocation", code, err)
	}
}

type authIntegrationApp struct {
	base     *testServer
	server   *httptest.Server
	client   *http.Client
	config   config.Config
	provider *fakeOIDC
}

func newAuthIntegrationApp(t *testing.T, invite bool) *authIntegrationApp {
	t.Helper()
	base := newTestServer(t)
	provider := newFakeOIDC(t)
	if invite {
		seedOIDCIdentity(t, base.pool, provider.issuer, "cognito-subject-a", demo.UserAID)
	}
	cfg := authTestConfig(provider)
	authServer, err := auth.NewServer(context.Background(), cfg, base.pool)
	if err != nil {
		t.Fatalf("create auth server: %v", err)
	}
	server := httptest.NewServer(httpapi.NewHandlerWithAuth(cfg, base.pool, nil, nil, authServer))
	t.Cleanup(server.Close)
	return &authIntegrationApp{
		base: base, server: server,
		client: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		config: cfg, provider: provider,
	}
}

func (app *authIntegrationApp) login(t *testing.T, userID, suffix string) (*http.Cookie, string) {
	t.Helper()
	response, err := app.client.Get(app.server.URL + "/auth/login")
	if err != nil {
		t.Fatalf("start login: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d, want 302", response.StatusCode)
	}
	params, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorization redirect: %v", err)
	}
	query := params.Query()
	var binding *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == "chat_login" {
			binding = cookie
		}
	}
	if binding == nil {
		t.Fatal("login did not set browser-binding cookie")
	}
	code := "code-" + suffix
	subject := "cognito-subject-a"
	if userID == "uninvited" {
		subject = "uninvited-subject"
	}
	app.provider.allowCode(code, subject, query.Get("nonce"), query.Get("code_challenge"))
	callback := app.server.URL + "/auth/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(query.Get("state"))
	request, _ := http.NewRequest(http.MethodGet, callback, nil)
	request.AddCookie(binding)
	callbackResponse, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("finish login: %v", err)
	}
	defer callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(callbackResponse.Body)
		t.Fatalf("callback status = %d, body = %s", callbackResponse.StatusCode, body)
	}
	for _, cookie := range callbackResponse.Cookies() {
		if cookie.Name == "chat_session" {
			return cookie, app.sessionCSRF(t, cookie)
		}
	}
	t.Fatal("callback did not set session cookie")
	return nil, ""
}

func (app *authIntegrationApp) sessionCSRF(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, app.server.URL+"/api/v1/session", nil)
	request.AddCookie(cookie)
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("get session CSRF token: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("get session status = %d, body = %s", response.StatusCode, body)
	}
	var body struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	return body.CSRFToken
}

func (app *authIntegrationApp) sessionStatus(t *testing.T, cookie *http.Cookie) int {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, app.server.URL+"/api/v1/session", nil)
	request.AddCookie(cookie)
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate OIDC signing key: %v", err)
	}
	fake := &fakeOIDC{key: key, codes: map[string]fakeCode{}, refreshStarted: make(chan struct{})}
	server := httptest.NewServer(fake)
	fake.issuer = server.URL
	t.Cleanup(server.Close)
	return fake
}

type fakeCode struct {
	subject   string
	nonce     string
	challenge string
}

type fakeOIDC struct {
	issuer             string
	key                *rsa.PrivateKey
	mu                 sync.Mutex
	codes              map[string]fakeCode
	lastCodeVerifier   string
	refreshCalls       int
	refreshDelay       time.Duration
	refreshStarted     chan struct{}
	refreshStartedOnce sync.Once
}

func (fake *fakeOIDC) allowCode(code, subject, nonce, challenge string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.codes[code] = fakeCode{subject: subject, nonce: nonce, challenge: challenge}
}

func (fake *fakeOIDC) verifyLastCodeVerifier(t *testing.T, code, challenge string) {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	verifier := fake.lastCodeVerifier
	digest := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(digest[:])
	if verifier == "" || want != challenge {
		t.Fatalf("token exchange PKCE verifier challenge = %q, want %q", want, challenge)
	}
}

func (fake *fakeOIDC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeFakeJSON(w, map[string]any{
			"issuer":                                fake.issuer,
			"authorization_endpoint":                fake.issuer + "/authorize",
			"token_endpoint":                        fake.issuer + "/token",
			"jwks_uri":                              fake.issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/jwks":
		publicKey := fake.key.PublicKey
		writeFakeJSON(w, map[string]any{"keys": []any{map[string]string{
			"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
		}}})
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("chat-test-client:test-client-secret"))
		if r.Header.Get("Authorization") != wantAuthorization {
			writeFakeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			code := r.Form.Get("code")
			fake.mu.Lock()
			registered, ok := fake.codes[code]
			verifier := r.Form.Get("code_verifier")
			fake.lastCodeVerifier = verifier
			fake.mu.Unlock()
			challengeSum := sha256.Sum256([]byte(verifier))
			if !ok || base64.RawURLEncoding.EncodeToString(challengeSum[:]) != registered.challenge || r.Form.Get("redirect_uri") == "" {
				writeFakeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
				return
			}
			fake.writeTokens(w, registered.subject, registered.nonce)
		case "refresh_token":
			fake.mu.Lock()
			fake.refreshCalls++
			delay := fake.refreshDelay
			fake.mu.Unlock()
			fake.refreshStartedOnce.Do(func() { close(fake.refreshStarted) })
			if delay > 0 {
				time.Sleep(delay)
			}
			fake.writeTokens(w, "cognito-subject-a", "")
		default:
			writeFakeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		}
	default:
		http.NotFound(w, r)
	}
}

func (fake *fakeOIDC) writeTokens(w http.ResponseWriter, subject, nonce string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.writeTokensLocked(w, subject, nonce)
}

func (fake *fakeOIDC) writeTokensLocked(w http.ResponseWriter, subject, nonce string) {
	idToken := fake.signIDToken(subject, nonce)
	writeFakeJSON(w, map[string]any{
		"access_token":  "access-token-for-" + subject,
		"refresh_token": "refresh-token-for-" + subject,
		"id_token":      idToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
}

func (fake *fakeOIDC) signIDToken(subject, nonce string) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	claims := map[string]any{"iss": fake.issuer, "sub": subject, "aud": "chat-test-client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Add(-time.Second).Unix()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, fake.key, crypto.SHA256, digest[:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeFakeJSON(w http.ResponseWriter, value any) {
	writeFakeJSONStatus(w, http.StatusOK, value)
}

func writeFakeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func authTestConfig(provider *fakeOIDC) config.Config {
	return config.Config{
		Environment:                   config.EnvironmentLocal,
		DatabaseURL:                   "postgres://chat:chat@localhost:5432/chat?sslmode=disable",
		ValkeyURL:                     "redis://localhost:6379/0",
		HTTPAddr:                      "127.0.0.1:0",
		AppOrigin:                     "http://chat.test",
		OIDCIssuer:                    provider.issuer,
		OIDCClientID:                  "chat-test-client",
		OIDCClientSecret:              "test-client-secret",
		OIDCRedirectURL:               "http://chat.test/auth/callback",
		SessionEncryptionCurrentKeyID: "test-key-1",
		SessionEncryptionKeys:         map[string][]byte{"test-key-1": []byte("0123456789abcdef0123456789abcdef")},
	}
}

func seedOIDCIdentity(t *testing.T, pool *pgxpool.Pool, issuer, subject, userID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO user_identities (identity_id, user_id, issuer, subject) VALUES ($1::uuid, $2::uuid, $3, $4)`, "88888888-8888-4888-8888-888888888888", userID, issuer, subject)
	if err != nil {
		t.Fatalf("seed invited OIDC identity: %v", err)
	}
}
