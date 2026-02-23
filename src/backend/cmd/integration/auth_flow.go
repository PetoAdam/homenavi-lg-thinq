package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type authStatus struct {
	Success     bool      `json:"success"`
	Provider    string    `json:"provider"`
	Mode        string    `json:"mode"`
	Message     string    `json:"message"`
	DeviceCount int       `json:"device_count"`
	Time        time.Time `json:"time"`
}

type authTokens struct {
	AccessToken  string
	RefreshToken string
}

type pendingOAuth struct {
	Provider string
	Expires  time.Time
}

type authFlowManager struct {
	mu      sync.Mutex
	status  authStatus
	pending map[string]pendingOAuth
}

func newAuthFlowManager() *authFlowManager {
	return &authFlowManager{pending: map[string]pendingOAuth{}}
}

func (m *authFlowManager) set(s authStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = s
}

func (m *authFlowManager) get() authStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *authFlowManager) issue(provider string, ttl time.Duration) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(buf)
	m.mu.Lock()
	m.pending[state] = pendingOAuth{Provider: provider, Expires: time.Now().UTC().Add(ttl)}
	m.mu.Unlock()
	return state, nil
}

func (m *authFlowManager) consume(state string) (pendingOAuth, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.pending[state]
	if !ok {
		return pendingOAuth{}, false
	}
	delete(m.pending, state)
	if time.Now().UTC().After(item.Expires) {
		return pendingOAuth{}, false
	}
	return item, true
}

func buildOAuthStartURL(r *http.Request, cfg setupConfig, mgr *authFlowManager, provider string) (string, string, error) {
	authBase := strings.TrimSpace(cfg.OAuthAuthURL)
	clientID := strings.TrimSpace(cfg.OAuthClientID)
	if authBase == "" || clientID == "" {
		return "", "", fmt.Errorf("oauth_auth_url and oauth_client_id are required")
	}
	u, err := url.Parse(authBase)
	if err != nil {
		return "", "", err
	}
	state, err := mgr.issue(provider, 10*time.Minute)
	if err != nil {
		return "", "", err
	}
	scheme := "http"
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || r.TLS != nil {
		scheme = "https"
	}
	redirectURI := fmt.Sprintf("%s://%s/api/admin/auth/oauth/callback", scheme, r.Host)
	q := u.Query()
	if strings.Contains(strings.ToLower(strings.TrimSpace(u.Path)), "login/sign_in") {
		country, language := thinqLocaleForRequest(r, cfg.AccountRegion)
		q.Set("country", country)
		q.Set("language", language)
		q.Set("svcCode", "SVC202")
		q.Set("authSvr", "oauth2")
		q.Set("client_id", clientID)
		q.Set("division", "ha")
		q.Set("grant_type", "password")
		q.Set("redirect_uri", redirectURI)
		q.Set("state", state)
		if p := strings.TrimSpace(strings.ToLower(provider)); p != "" && p != "email" {
			q.Set("provider", p)
			q.Set("idp", p)
		}
	} else {
		q.Set("response_type", "code")
		q.Set("client_id", clientID)
		q.Set("redirect_uri", redirectURI)
		q.Set("scope", firstNonEmpty(strings.TrimSpace(cfg.OAuthScopes), "openid profile email"))
		q.Set("state", state)
		if p := strings.TrimSpace(strings.ToLower(provider)); p != "" {
			q.Set("provider", p)
			q.Set("idp", p)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), state, nil
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request, setup *setupStore, mgr *authFlowManager, cloudProvider thinqProvider) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	accessToken := strings.TrimSpace(r.URL.Query().Get("access_token"))
	refreshToken := strings.TrimSpace(r.URL.Query().Get("refresh_token"))
	errCode := strings.TrimSpace(r.URL.Query().Get("error"))
	errDesc := strings.TrimSpace(r.URL.Query().Get("error_description"))
	item, ok := mgr.consume(state)
	if !ok || errCode != "" || (code == "" && accessToken == "") {
		msg := "OAuth login failed"
		if errCode != "" {
			if errDesc != "" {
				msg = "OAuth error: " + errCode + " - " + errDesc
			} else {
				msg = "OAuth error: " + errCode
			}
		}
		mgr.set(authStatus{Success: false, Provider: item.Provider, Mode: "cloud", Message: msg, Time: time.Now().UTC()})
		writeOAuthCallbackHTML(w, false, msg)
		return
	}
	cfg, err := setup.load()
	if err != nil {
		mgr.set(authStatus{Success: false, Provider: item.Provider, Mode: "cloud", Message: err.Error(), Time: time.Now().UTC()})
		writeOAuthCallbackHTML(w, false, "Failed to load setup")
		return
	}
	tokens := authTokens{AccessToken: accessToken, RefreshToken: refreshToken}
	if tokens.AccessToken == "" {
		var err error
		tokens, err = exchangeOAuthCode(r.Context(), cfg, code, r.Host, r)
		if err != nil {
			mgr.set(authStatus{Success: false, Provider: item.Provider, Mode: "cloud", Message: err.Error(), Time: time.Now().UTC()})
			writeOAuthCallbackHTML(w, false, "Token exchange failed: "+err.Error())
			return
		}
	}
	cfg.Mode = "cloud"
	if tokens.AccessToken != "" {
		cfg.AccessToken = tokens.AccessToken
	}
	if tokens.RefreshToken != "" {
		cfg.RefreshToken = tokens.RefreshToken
	}
	if err := setup.save(cfg); err != nil {
		mgr.set(authStatus{Success: false, Provider: item.Provider, Mode: cfg.Mode, Message: err.Error(), Time: time.Now().UTC()})
		writeOAuthCallbackHTML(w, false, "Failed to persist tokens")
		return
	}
	count, verifyErr := verifyThinQLogin(r.Context(), cfg, cloudProvider)
	if verifyErr != nil {
		mgr.set(authStatus{Success: false, Provider: item.Provider, Mode: cfg.Mode, Message: verifyErr.Error(), Time: time.Now().UTC()})
		writeOAuthCallbackHTML(w, false, "Login stored but verification failed: "+verifyErr.Error())
		return
	}
	mgr.set(authStatus{Success: true, Provider: item.Provider, Mode: cfg.Mode, Message: "OAuth login succeeded", DeviceCount: count, Time: time.Now().UTC()})
	writeOAuthCallbackHTML(w, true, fmt.Sprintf("Login succeeded. Found %d device(s).", count))
}

func thinqLocaleForRequest(r *http.Request, region string) (string, string) {
	region = normalizeThinQRegion(region)
	defaultCountry := "US"
	defaultLanguage := "en-US"
	switch region {
	case "kr":
		defaultCountry = "KR"
		defaultLanguage = "ko-KR"
	case "eu":
		defaultCountry = "HU"
		defaultLanguage = "en-GB"
	}

	accept := strings.TrimSpace(r.Header.Get("Accept-Language"))
	if accept == "" {
		return defaultCountry, defaultLanguage
	}
	primary := strings.Split(accept, ",")[0]
	primary = strings.TrimSpace(strings.Split(primary, ";")[0])
	if primary == "" {
		return defaultCountry, defaultLanguage
	}
	langParts := strings.Split(primary, "-")
	lang := strings.ToLower(strings.TrimSpace(langParts[0]))
	country := defaultCountry
	if len(langParts) > 1 {
		candidate := strings.ToUpper(strings.TrimSpace(langParts[1]))
		if len(candidate) == 2 {
			country = candidate
		}
	}
	if lang == "" {
		lang = strings.ToLower(strings.Split(defaultLanguage, "-")[0])
	}
	return country, lang + "-" + country
}

func writeOAuthCallbackHTML(w http.ResponseWriter, success bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	escaped, _ := json.Marshal(message)
	status := "failure"
	if success {
		status = "success"
	}
	_, _ = w.Write([]byte(`<!doctype html><html><body style="font-family:system-ui;padding:20px;background:#0f172a;color:#e2e8f0;">` +
		`<h2>LG ThinQ Login ` + status + `</h2><p id="m"></p><script>` +
		`const msg=` + string(escaped) + `;document.getElementById('m').textContent=msg;` +
		`if(window.opener){window.opener.postMessage({type:'lgthinq-auth-result',success:` + map[bool]string{true: "true", false: "false"}[success] + `,message:msg}, '*');window.close();}` +
		`</script></body></html>`))
}

func exchangeOAuthCode(ctx context.Context, cfg setupConfig, code, host string, r *http.Request) (authTokens, error) {
	tokenURL := strings.TrimSpace(cfg.OAuthTokenURL)
	if tokenURL == "" {
		return authTokens{}, fmt.Errorf("oauth_token_url is required")
	}
	scheme := "http"
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || r.TLS != nil {
		scheme = "https"
	}
	redirectURI := fmt.Sprintf("%s://%s/api/admin/auth/oauth/callback", scheme, host)
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", strings.TrimSpace(cfg.OAuthClientID))
	if sec := strings.TrimSpace(cfg.OAuthClientSecret); sec != "" {
		form.Set("client_secret", sec)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return authTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return authTokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return authTokens{}, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return authTokens{}, err
	}
	return extractTokens(payload)
}

func runPasswordLogin(ctx context.Context, cfg setupConfig, email, password, provider string) (authTokens, error) {
	if email == "" || password == "" {
		return authTokens{}, fmt.Errorf("email and password are required")
	}
	if loginURL := strings.TrimSpace(cfg.AuthPasswordURL); loginURL != "" {
		body, _ := json.Marshal(map[string]any{"email": email, "password": password, "provider": provider})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(string(body)))
		if err != nil {
			return authTokens{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return authTokens{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return authTokens{}, fmt.Errorf("auth endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return authTokens{}, err
		}
		return extractTokens(payload)
	}
	if strings.TrimSpace(cfg.OAuthTokenURL) == "" {
		return authTokens{}, fmt.Errorf("configure auth_password_url or oauth_token_url")
	}
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", email)
	form.Set("password", password)
	form.Set("client_id", strings.TrimSpace(cfg.OAuthClientID))
	if sec := strings.TrimSpace(cfg.OAuthClientSecret); sec != "" {
		form.Set("client_secret", sec)
	}
	form.Set("scope", firstNonEmpty(strings.TrimSpace(cfg.OAuthScopes), "openid profile email"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(cfg.OAuthTokenURL), strings.NewReader(form.Encode()))
	if err != nil {
		return authTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return authTokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return authTokens{}, fmt.Errorf("password grant status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return authTokens{}, err
	}
	return extractTokens(payload)
}

func extractTokens(payload map[string]any) (authTokens, error) {
	access := strings.TrimSpace(asString(payload["access_token"]))
	refresh := strings.TrimSpace(asString(payload["refresh_token"]))
	if access == "" {
		if tokenObj, ok := payload["token"].(map[string]any); ok {
			access = strings.TrimSpace(asString(tokenObj["access_token"]))
			if refresh == "" {
				refresh = strings.TrimSpace(asString(tokenObj["refresh_token"]))
			}
		}
	}
	if access == "" {
		return authTokens{}, fmt.Errorf("access_token missing in response")
	}
	return authTokens{AccessToken: access, RefreshToken: refresh}, nil
}

func parseTokensFromCallbackURL(raw string) (authTokens, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return authTokens{}, fmt.Errorf("callback url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return authTokens{}, fmt.Errorf("invalid callback url")
	}
	access := strings.TrimSpace(u.Query().Get("access_token"))
	refresh := strings.TrimSpace(u.Query().Get("refresh_token"))
	if access == "" && strings.TrimSpace(u.Fragment) != "" {
		frag, _ := url.ParseQuery(u.Fragment)
		access = strings.TrimSpace(frag.Get("access_token"))
		refresh = strings.TrimSpace(frag.Get("refresh_token"))
	}
	if access == "" {
		return authTokens{}, fmt.Errorf("access_token missing in callback url")
	}
	return authTokens{AccessToken: access, RefreshToken: refresh}, nil
}

func verifyThinQLogin(ctx context.Context, cfg setupConfig, provider thinqProvider) (int, error) {
	if provider == nil {
		return 0, fmt.Errorf("provider not configured")
	}
	devices, err := provider.ListDevices(ctx, cfg)
	if err != nil {
		return 0, err
	}
	return len(devices), nil
}
