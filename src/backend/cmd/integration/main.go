package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

var lgThinQLogLevel = strings.ToLower(strings.TrimSpace(os.Getenv("LG_THINQ_LOG_LEVEL")))

func shouldLogDebug() bool {
	return lgThinQLogLevel == "debug"
}

func logInfof(format string, args ...any) {
	log.Printf("[INFO] "+format, args...)
}

func logWarnf(format string, args ...any) {
	log.Printf("[WARN] "+format, args...)
}

func logDebugf(format string, args ...any) {
	if shouldLogDebug() {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func maskToken(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "<empty>"
	}
	if len(v) <= 8 {
		return "***"
	}
	return v[:4] + "..." + v[len(v)-4:]
}

func summarizeSetupForLog(cfg setupConfig) string {
	return fmt.Sprintf("mode=%s region=%s country=%s api=%s pat=%s",
		strings.TrimSpace(cfg.Mode),
		strings.TrimSpace(cfg.AccountRegion),
		strings.TrimSpace(cfg.Country),
		strings.TrimSpace(cfg.APIBaseURL),
		maskToken(cfg.PATToken),
	)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying response writer does not implement http.Hijacker")
	}
	return h.Hijack()
}

func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := r.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		logInfof("http method=%s path=%s status=%d bytes=%d remote=%s duration_ms=%d",
			r.Method,
			r.URL.Path,
			rec.status,
			rec.bytes,
			r.RemoteAddr,
			time.Since(start).Milliseconds(),
		)
	})
}

type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

type adminAuth struct {
	pubKey  *rsa.PublicKey
	enabled bool
}

func newAdminAuthFromEnv() (*adminAuth, error) {
	path := strings.TrimSpace(os.Getenv("JWT_PUBLIC_KEY_PATH"))
	inlineKey := strings.TrimSpace(os.Getenv("JWT_PUBLIC_KEY"))
	if path == "" && inlineKey == "" {
		return &adminAuth{enabled: false}, nil
	}
	var keyData []byte
	if inlineKey != "" {
		keyData = []byte(inlineKey)
	} else {
		var err error
		keyData, err = os.ReadFile(path) // #nosec G304
		if err != nil {
			return nil, err
		}
	}
	pubKey, err := jwt.ParseRSAPublicKeyFromPEM(keyData)
	if err != nil {
		return nil, err
	}
	return &adminAuth{pubKey: pubKey, enabled: true}, nil
}

func tokenFromRequest(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if len(authz) > 7 && strings.HasPrefix(authz, "Bearer ") {
		logDebugf("auth token source=authorization path=%s", r.URL.Path)
		return authz[7:]
	}
	if cookie, err := r.Cookie("auth_token"); err == nil {
		logDebugf("auth token source=cookie path=%s", r.URL.Path)
		return cookie.Value
	}
	logWarnf("auth token missing path=%s method=%s", r.URL.Path, r.Method)
	return ""
}

func roleAtLeast(required, actual string) bool {
	rank := map[string]int{"public": 0, "user": 1, "resident": 2, "admin": 3, "service": 4}
	return rank[strings.ToLower(strings.TrimSpace(actual))] >= rank[required]
}

func (a *adminAuth) roleFromRequest(r *http.Request) (string, error) {
	if a == nil || !a.enabled || a.pubKey == nil {
		return "", errors.New("auth not configured")
	}
	tok := tokenFromRequest(r)
	if tok == "" {
		return "", errors.New("missing token")
	}
	parsed, err := jwt.ParseWithClaims(tok, &claims{}, func(token *jwt.Token) (any, error) {
		return a.pubKey, nil
	})
	if err != nil || !parsed.Valid {
		return "", errors.New("invalid token")
	}
	c, ok := parsed.Claims.(*claims)
	if !ok {
		return "", errors.New("invalid claims")
	}
	return strings.ToLower(strings.TrimSpace(c.Role)), nil
}

func (a *adminAuth) requireRole(w http.ResponseWriter, r *http.Request, role string) bool {
	if a == nil || !a.enabled || a.pubKey == nil {
		logWarnf("auth unavailable required_role=%s path=%s", role, r.URL.Path)
		writeJSONError(w, http.StatusServiceUnavailable, "auth not configured")
		return false
	}
	actual, err := a.roleFromRequest(r)
	if err != nil {
		logWarnf("auth failed required_role=%s path=%s err=%v", role, r.URL.Path, err)
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return false
	}
	if !roleAtLeast(role, actual) {
		logWarnf("auth forbidden required_role=%s actual_role=%s path=%s", role, actual, r.URL.Path)
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return false
	}
	logDebugf("auth ok required_role=%s actual_role=%s path=%s", role, actual, r.URL.Path)
	return true
}

type setupConfig struct {
	Mode                  string   `json:"mode"`
	AccountEmail          string   `json:"account_email,omitempty"`
	AccountRegion         string   `json:"account_region"`
	APIBaseURL            string   `json:"api_base_url"`
	PATToken              string   `json:"pat_token"`
	APIKey                string   `json:"api_key"`
	Country               string   `json:"country"`
	ServicePhase          string   `json:"service_phase"`
	ClientID              string   `json:"client_id"`
	SyncIntervalSec       int      `json:"sync_interval_sec"`
	AccessToken           string   `json:"access_token,omitempty"`
	RefreshToken          string   `json:"refresh_token,omitempty"`
	AuthPasswordURL       string   `json:"auth_password_url,omitempty"`
	OAuthAuthURL          string   `json:"oauth_auth_url,omitempty"`
	OAuthTokenURL         string   `json:"oauth_token_url,omitempty"`
	OAuthClientID         string   `json:"oauth_client_id,omitempty"`
	OAuthClientSecret     string   `json:"oauth_client_secret,omitempty"`
	OAuthScopes           string   `json:"oauth_scopes,omitempty"`
	RealtimeEnabled       bool     `json:"realtime_enabled"`
	RealtimeTransport     string   `json:"realtime_transport"`
	RealtimeReconnectSec  int      `json:"realtime_reconnect_sec"`
	RealtimeClientCertPEM string   `json:"realtime_client_cert_pem,omitempty"`
	RealtimeClientKeyPEM  string   `json:"realtime_client_key_pem,omitempty"`
	RealtimeSubscriptions []string `json:"realtime_subscriptions,omitempty"`
	RealtimeCertExpiresAt string   `json:"realtime_cert_expires_at,omitempty"`
}

type setupStore struct {
	path string
	mu   sync.Mutex
}

const defaultSetupFileName = "lg-thinq.setup.json"

func setupFileName() string {
	if v := strings.TrimSpace(os.Getenv("LG_THINQ_SETUP_FILE")); v != "" {
		return strings.Trim(strings.TrimSpace(v), "/")
	}
	return defaultSetupFileName
}

func pathLooksLikeDirectory(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator)) {
		return true
	}
	base := filepath.Base(p)
	return !strings.Contains(base, ".")
}

func (s *setupStore) resolvePath() string {
	p := strings.TrimSpace(s.path)
	if p == "" {
		return ""
	}
	if pathLooksLikeDirectory(p) {
		return filepath.Join(p, setupFileName())
	}
	st, err := os.Stat(p)
	if err == nil && st.IsDir() {
		return filepath.Join(p, setupFileName())
	}
	return p
}

func ensureSetupFilePath(resolved string) string {
	if strings.TrimSpace(resolved) == "" {
		return ""
	}
	st, err := os.Stat(resolved)
	if err == nil && st.IsDir() {
		return filepath.Join(resolved, setupFileName())
	}
	return resolved
}

func defaultSetupPath() string {
	if v := strings.TrimSpace(os.Getenv("LG_THINQ_SETUP_PATH")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("INTEGRATION_SECRETS_PATH")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("INTEGRATIONS_ROOT")); v != "" {
		return filepath.Join(v, "integrations", "secrets")
	}
	return filepath.Join("config")
}

func normalizeThinQRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "us", "kr", "eu", "global":
		return strings.ToLower(strings.TrimSpace(region))
	default:
		return "eu"
	}
}

func apiBaseURLForRegion(region string) string {
	switch normalizeThinQRegion(region) {
	case "us":
		return "https://api-aic.lgthinq.com"
	case "kr":
		return "https://api-kic.lgthinq.com"
	case "global":
		return "https://api-aic.lgthinq.com"
	default:
		return "https://api-eic.lgthinq.com"
	}
}

func normalizeAPIBaseURL(baseURL, region string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return apiBaseURLForRegion(region)
	}
	lower := strings.ToLower(trimmed)
	if lower == "https://api.smartthinq.com" || lower == "http://api.smartthinq.com" {
		return apiBaseURLForRegion(region)
	}
	return trimmed
}

func countryForRegion(region string) string {
	switch normalizeThinQRegion(region) {
	case "us":
		return "US"
	case "kr":
		return "KR"
	case "global":
		return "US"
	default:
		return "GB"
	}
}

func applySetupDefaults(cfg setupConfig) setupConfig {
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = "cloud"
	}
	if strings.TrimSpace(cfg.AccountRegion) == "" {
		cfg.AccountRegion = strings.TrimSpace(os.Getenv("LG_THINQ_ACCOUNT_REGION"))
	}
	if strings.TrimSpace(cfg.AccountRegion) == "" {
		cfg.AccountRegion = "eu"
	}
	cfg.AccountRegion = normalizeThinQRegion(cfg.AccountRegion)
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = strings.TrimSpace(os.Getenv("LG_THINQ_API_BASE_URL"))
	}
	cfg.APIBaseURL = normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion)
	if strings.TrimSpace(cfg.PATToken) == "" {
		cfg.PATToken = strings.TrimSpace(os.Getenv("LG_THINQ_PAT_TOKEN"))
	}
	if strings.TrimSpace(cfg.PATToken) == "" {
		cfg.PATToken = strings.TrimSpace(cfg.AccessToken)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = strings.TrimSpace(os.Getenv("LG_THINQ_API_KEY"))
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = "v6GFvkweNo7DK7yD3ylIZ9w52aKBU0eJ7wLXkSR3"
	}
	if strings.TrimSpace(cfg.Country) == "" {
		cfg.Country = strings.TrimSpace(os.Getenv("LG_THINQ_COUNTRY"))
	}
	if strings.TrimSpace(cfg.Country) == "" {
		cfg.Country = countryForRegion(cfg.AccountRegion)
	}
	cfg.Country = strings.ToUpper(strings.TrimSpace(cfg.Country))
	if strings.TrimSpace(cfg.ServicePhase) == "" {
		cfg.ServicePhase = strings.TrimSpace(os.Getenv("LG_THINQ_SERVICE_PHASE"))
	}
	if strings.TrimSpace(cfg.ServicePhase) == "" {
		cfg.ServicePhase = "OP"
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		cfg.ClientID = strings.TrimSpace(os.Getenv("LG_THINQ_CLIENT_ID"))
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		cfg.ClientID = "homenavi-lg-thinq-client"
	}
	if cfg.SyncIntervalSec <= 0 {
		cfg.SyncIntervalSec = 180
	}
	if cfg.SyncIntervalSec < 120 {
		cfg.SyncIntervalSec = 120
	}
	if cfg.SyncIntervalSec > 3600 {
		cfg.SyncIntervalSec = 3600
	}
	if strings.TrimSpace(cfg.RealtimeTransport) == "" {
		cfg.RealtimeTransport = strings.TrimSpace(os.Getenv("LG_THINQ_REALTIME_TRANSPORT"))
	}
	if strings.TrimSpace(cfg.RealtimeTransport) == "" {
		cfg.RealtimeTransport = "mqtt"
	}
	cfg.RealtimeTransport = strings.ToLower(strings.TrimSpace(cfg.RealtimeTransport))
	if cfg.RealtimeTransport != "mqtt" && cfg.RealtimeTransport != "ws" {
		cfg.RealtimeTransport = "mqtt"
	}
	if !cfg.RealtimeEnabled {
		cfg.RealtimeEnabled = strings.EqualFold(strings.TrimSpace(os.Getenv("LG_THINQ_REALTIME_ENABLED")), "true")
	}
	if cfg.RealtimeReconnectSec <= 0 {
		cfg.RealtimeReconnectSec = 15
	}
	if cfg.RealtimeReconnectSec < 5 {
		cfg.RealtimeReconnectSec = 5
	}
	if cfg.RealtimeReconnectSec > 300 {
		cfg.RealtimeReconnectSec = 300
	}
	return cfg
}

func (s *setupStore) load() (setupConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *setupStore) loadUnlocked() (setupConfig, error) {
	var cfg setupConfig
	resolved := ensureSetupFilePath(s.resolvePath())
	if resolved == "" {
		logWarnf("setup load using defaults because resolved path is empty")
		return applySetupDefaults(cfg), nil
	}
	logDebugf("setup load path=%s", resolved)
	data, err := os.ReadFile(resolved) // #nosec G304
	if err != nil {
		if os.IsNotExist(err) {
			logWarnf("setup file missing path=%s; using defaults", resolved)
			return applySetupDefaults(cfg), nil
		}
		logWarnf("setup load failed path=%s err=%v", resolved, err)
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		logWarnf("setup json invalid path=%s; falling back to defaults err=%v", resolved, err)
		return applySetupDefaults(setupConfig{}), nil
	}
	if cfg.SyncIntervalSec <= 0 {
		cfg.SyncIntervalSec = 180
	}
	return applySetupDefaults(cfg), nil
}

func (s *setupStore) save(cfg setupConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := s.resolvePath()
	if strings.TrimSpace(resolved) == "" {
		return errors.New("setup path is not configured")
	}
	legacyResolved := ensureSetupFilePath(resolved)
	if legacyResolved != resolved {
		logWarnf("setup path target is a directory; writing setup to nested file path=%s", legacyResolved)
	}
	resolved = legacyResolved
	if resolved == "" {
		return errors.New("setup path is not configured")
	}
	if cfg.SyncIntervalSec <= 0 {
		cfg.SyncIntervalSec = 180
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(resolved, b, 0o600); err != nil {
		logWarnf("setup save failed path=%s err=%v", resolved, err)
		return err
	}
	logInfof("setup saved path=%s summary=%s", resolved, summarizeSetupForLog(cfg))
	return nil
}

type setupStatus struct {
	AccountRegion          string `json:"account_region"`
	APIBaseURL             string `json:"api_base_url"`
	HasPATToken            bool   `json:"has_pat_token"`
	APIKey                 string `json:"api_key"`
	Country                string `json:"country"`
	ServicePhase           string `json:"service_phase"`
	ClientID               string `json:"client_id"`
	SyncIntervalSec        int    `json:"sync_interval_sec"`
	RealtimeEnabled        bool   `json:"realtime_enabled"`
	RealtimeTransport      string `json:"realtime_transport"`
	RealtimeReconnectSec   int    `json:"realtime_reconnect_sec"`
	HasRealtimeCertificate bool   `json:"has_realtime_certificate"`
	RealtimeSubscriptions  int    `json:"realtime_subscriptions"`
	RealtimeCertExpiresAt  string `json:"realtime_cert_expires_at,omitempty"`
}

func statusFromConfig(cfg setupConfig) setupStatus {
	return setupStatus{
		AccountRegion:          strings.TrimSpace(cfg.AccountRegion),
		APIBaseURL:             strings.TrimSpace(cfg.APIBaseURL),
		HasPATToken:            strings.TrimSpace(cfg.PATToken) != "",
		APIKey:                 strings.TrimSpace(cfg.APIKey),
		Country:                strings.TrimSpace(cfg.Country),
		ServicePhase:           strings.TrimSpace(cfg.ServicePhase),
		ClientID:               strings.TrimSpace(cfg.ClientID),
		SyncIntervalSec:        cfg.SyncIntervalSec,
		RealtimeEnabled:        cfg.RealtimeEnabled,
		RealtimeTransport:      strings.TrimSpace(cfg.RealtimeTransport),
		RealtimeReconnectSec:   cfg.RealtimeReconnectSec,
		HasRealtimeCertificate: strings.TrimSpace(cfg.RealtimeClientCertPEM) != "" && strings.TrimSpace(cfg.RealtimeClientKeyPEM) != "",
		RealtimeSubscriptions:  len(cfg.RealtimeSubscriptions),
		RealtimeCertExpiresAt:  strings.TrimSpace(cfg.RealtimeCertExpiresAt),
	}
}

type wsHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

func newWSHub() *wsHub {
	return &wsHub{clients: map[*websocket.Conn]struct{}{}}
}

func (h *wsHub) add(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = struct{}{}
}

func (h *wsHub) remove(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, conn)
}

func (h *wsHub) broadcast(payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			_ = conn.Close()
			delete(h.clients, conn)
		}
	}
}

type automationExecuteRequest struct {
	ActionID string         `json:"action_id"`
	Input    map[string]any `json:"input"`
	RunID    string         `json:"run_id"`
	NodeID   string         `json:"node_id"`
}

const (
	hdpStateTopic                = "homenavi/hdp/device/state/"
	hdpMetaTopic                 = "homenavi/hdp/device/metadata/"
	hdpCmdTopic                  = "homenavi/hdp/device/command/lgthinq/#"
	hdpCmdResTopic               = "homenavi/hdp/device/command_result/"
	commandStateGateTimeout      = 20 * time.Second
	commandStateGateFreezeWindow = 3 * time.Second
	commandStateGatePostGrace    = 1 * time.Second
	commandStateGateLogInterval  = 2 * time.Second
)

type pendingStateExpectation struct {
	Corr        string
	Expected    map[string]any
	Baseline    map[string]any
	FreezeUntil time.Time
	Expires     time.Time
	LastLog     time.Time
}

type commandStateGate struct {
	mu      sync.Mutex
	pending map[string]pendingStateExpectation
	timeout time.Duration
	freeze  time.Duration
}

func newCommandStateGate(timeout time.Duration) *commandStateGate {
	if timeout <= 0 {
		timeout = commandStateGateTimeout
	}
	return &commandStateGate{
		pending: map[string]pendingStateExpectation{},
		timeout: timeout,
		freeze:  commandStateGateFreezeWindow,
	}
}

func (g *commandStateGate) track(deviceID, corr string, expected map[string]any) {
	g.trackWithBaseline(deviceID, corr, expected, nil)
}

func (g *commandStateGate) trackWithBaseline(deviceID, corr string, expected map[string]any, baseline map[string]any) {
	if g == nil {
		return
	}
	id := sanitizeDeviceID(deviceID)
	now := time.Now().UTC()
	freeze := g.freeze
	if freeze < 0 {
		freeze = 0
	}
	if freeze > g.timeout {
		freeze = g.timeout
	}
	g.mu.Lock()
	g.pending[id] = pendingStateExpectation{
		Corr:        strings.TrimSpace(corr),
		Expected:    cloneAnyMap(expected),
		Baseline:    cloneAnyMap(baseline),
		FreezeUntil: now.Add(freeze),
		Expires:     now.Add(g.timeout),
	}
	g.mu.Unlock()
}

func (g *commandStateGate) allow(deviceID string, state map[string]any, optimistic bool) bool {
	if g == nil {
		return true
	}
	if optimistic {
		return true
	}
	id := sanitizeDeviceID(deviceID)
	now := time.Now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[id]
	if !ok {
		return true
	}
	if now.After(p.Expires) {
		delete(g.pending, id)
		return true
	}
	if now.Before(p.FreezeUntil) {
		return false
	}

	if len(p.Expected) > 0 {
		if expectedStateSatisfied(state, p.Expected) {
			delete(g.pending, id)
			return true
		}
		return false
	}

	// For commands without an expected patch, require *some* change from the baseline
	// before allowing synced state through. This prevents a refresh/realtime update
	// with the old value from immediately clearing the gate.
	if p.Baseline == nil || stateMapsDiffer(state, p.Baseline) {
		delete(g.pending, id)
		return true
	}
	return false
}

func (g *commandStateGate) cancel(deviceID, corr string) {
	if g == nil {
		return
	}
	id := sanitizeDeviceID(deviceID)
	corr = strings.TrimSpace(corr)
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[id]
	if !ok {
		return
	}
	if corr == "" || strings.EqualFold(strings.TrimSpace(p.Corr), corr) {
		delete(g.pending, id)
	}
}

func (g *commandStateGate) extendFreeze(deviceID, corr string, d time.Duration) {
	if g == nil {
		return
	}
	if d <= 0 {
		return
	}
	id := sanitizeDeviceID(deviceID)
	corr = strings.TrimSpace(corr)
	now := time.Now().UTC()
	until := now.Add(d)

	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[id]
	if !ok {
		return
	}
	if corr != "" && !strings.EqualFold(strings.TrimSpace(p.Corr), corr) {
		return
	}
	if until.After(p.FreezeUntil) {
		p.FreezeUntil = until
		g.pending[id] = p
	}
}

func (g *commandStateGate) logBlocked(deviceID string) {
	if g == nil {
		return
	}
	id := sanitizeDeviceID(deviceID)
	now := time.Now().UTC()

	g.mu.Lock()
	p, ok := g.pending[id]
	if !ok {
		g.mu.Unlock()
		return
	}
	if !p.LastLog.IsZero() && now.Sub(p.LastLog) < commandStateGateLogInterval {
		g.mu.Unlock()
		return
	}
	p.LastLog = now
	g.pending[id] = p
	g.mu.Unlock()

	if now.Before(p.FreezeUntil) {
		remaining := p.FreezeUntil.Sub(now)
		logDebugf("state gated device_id=%s reason=freeze remaining_ms=%d corr=%s", id, remaining.Milliseconds(), p.Corr)
		return
	}
	if len(p.Expected) == 0 {
		expiresIn := p.Expires.Sub(now)
		if expiresIn < 0 {
			expiresIn = 0
		}
		logDebugf("state gated device_id=%s reason=waiting_change expires_in_ms=%d corr=%s", id, expiresIn.Milliseconds(), p.Corr)
		return
	}
	expiresIn := p.Expires.Sub(now)
	if expiresIn < 0 {
		expiresIn = 0
	}
	logDebugf("state gated device_id=%s reason=waiting_expected expires_in_ms=%d corr=%s", id, expiresIn.Milliseconds(), p.Corr)
}

func stateMapsDiffer(state map[string]any, baseline map[string]any) bool {
	if state == nil {
		state = map[string]any{}
	}
	if baseline == nil {
		baseline = map[string]any{}
	}
	if len(state) == 0 && len(baseline) == 0 {
		return false
	}
	keys := make(map[string]struct{}, len(state)+len(baseline))
	for k := range state {
		keys[k] = struct{}{}
	}
	for k := range baseline {
		keys[k] = struct{}{}
	}
	for k := range keys {
		got, gok := state[k]
		want, wok := baseline[k]
		if gok != wok {
			return true
		}
		if !gok {
			continue
		}
		if !stateValueMatches(got, want) {
			return true
		}
	}
	return false
}

func expectedStateSatisfied(state map[string]any, expected map[string]any) bool {
	if len(expected) == 0 {
		return true
	}
	if state == nil {
		return false
	}
	for key, want := range expected {
		got, ok := state[key]
		if !ok {
			return false
		}
		if !stateValueMatches(got, want) {
			return false
		}
	}
	return true
}

func stateValueMatches(got any, want any) bool {
	if gs, ok := got.(string); ok {
		if ws, ok := want.(string); ok {
			return strings.EqualFold(strings.TrimSpace(gs), strings.TrimSpace(ws))
		}
	}
	if gb, ok := got.(bool); ok {
		if wb, ok := want.(bool); ok {
			return gb == wb
		}
	}
	gn, gok := asNumeric(got)
	wn, wok := asNumeric(want)
	if gok && wok {
		return gn == wn
	}
	return fmt.Sprintf("%v", got) == fmt.Sprintf("%v", want)
}

func asNumeric(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint64:
		return float64(t), true
	case uint32:
		return float64(t), true
	default:
		return 0, false
	}
}

func expectedStateForCommand(dev thinqDevice, cmd thinqCommand) map[string]any {
	if strings.TrimSpace(cmd.Name) == "" {
		return nil
	}
	deviceType := normalizeDeviceType(dev.Type)
	switch deviceType {
	case "tv":
		switch cmd.Name {
		case "set_power":
			return map[string]any{"power": normalizePower(cmd.Params["power"])}
		case "set_mute":
			return map[string]any{"muted": asBool(cmd.Params["muted"])}
		case "set_volume":
			return map[string]any{"volume": clamp(asInt(cmd.Params["volume"], 0), 0, 100)}
		case "set_input":
			return map[string]any{"input": firstNonEmpty(asString(cmd.Params["input"]), "hdmi1")}
		}
	case "washer":
		opMode := ""
		if operation, ok := cmd.Params["operation"].(map[string]any); ok {
			opMode = strings.ToUpper(strings.TrimSpace(asString(operation["washerOperationMode"])))
		}
		switch cmd.Name {
		case "start":
			return map[string]any{"run_state": "running"}
		case "stop":
			return map[string]any{"run_state": "idle"}
		case "set_power":
			if opMode == "POWER_ON" {
				return map[string]any{"power": "on"}
			}
			if opMode == "POWER_OFF" {
				return map[string]any{"power": "off"}
			}
		case "set_operation_mode":
			switch opMode {
			case "POWER_ON":
				return map[string]any{"power": "on"}
			case "POWER_OFF":
				return map[string]any{"power": "off"}
			case "START":
				return map[string]any{"run_state": "running"}
			case "STOP":
				return map[string]any{"run_state": "idle"}
			}
		}
	}
	return nil
}

func mqttServerFromBroker(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "mqtt", "tcp", "":
		return "tcp://" + u.Host, nil
	case "ssl", "tls":
		return "ssl://" + u.Host, nil
	case "ws", "wss":
		return u.Scheme + "://" + u.Host + u.Path, nil
	default:
		return "", nil
	}
}

func newMQTTClient(broker string) (mqtt.Client, error) {
	server, err := mqttServerFromBroker(broker)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(server) == "" {
		return nil, nil
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(server)
	opts.SetClientID("homenavi-lg-thinq-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	u, _ := url.Parse(strings.TrimSpace(broker))
	if u != nil && u.User != nil {
		pw, _ := u.User.Password()
		opts.SetUsername(u.User.Username())
		opts.SetPassword(pw)
	}
	if u != nil && (u.Scheme == "ssl" || u.Scheme == "tls" || u.Scheme == "wss") {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if host := u.Hostname(); strings.TrimSpace(host) != "" {
			tlsConfig.ServerName = host
		}
		opts.SetTLSConfig(tlsConfig)
	}
	cli := mqtt.NewClient(opts)
	tok := cli.Connect()
	if tok.Wait() && tok.Error() != nil {
		return nil, tok.Error()
	}
	return cli, nil
}

func newMQTTClientWithRetry(broker string, maxWait time.Duration, retryEvery time.Duration) (mqtt.Client, error) {
	if maxWait <= 0 {
		return newMQTTClient(broker)
	}
	if retryEvery <= 0 {
		retryEvery = 3 * time.Second
	}

	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		cli, err := newMQTTClient(broker)
		if err == nil {
			return cli, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		logWarnf("mqtt connect failed; retrying in %s err=%v", retryEvery.String(), err)
		time.Sleep(retryEvery)
	}
	return nil, lastErr
}

func publishJSON(cli mqtt.Client, topic string, retain bool, payload map[string]any) {
	if cli == nil {
		return
	}
	payload["schema"] = "hdp.v1"
	payload["ts"] = time.Now().UTC().UnixMilli()
	b, _ := json.Marshal(payload)
	t := cli.Publish(topic, 0, retain, b)
	_ = t.WaitTimeout(5 * time.Second)
}

func publishMetadata(cli mqtt.Client, store *bridgeStore) {
	for _, dev := range store.list() {
		meta := mapThinQToHDPMetadata(dev)
		publishJSON(cli, hdpMetaTopic+sanitizeDeviceID(dev.ID), true, meta)
	}
}

func publishDeviceState(cli mqtt.Client, dev thinqDevice, gate *commandStateGate, optimistic bool) {
	state := mapThinQToHDPState(dev)
	if gate != nil && !gate.allow(dev.ID, state, optimistic) {
		gate.logBlocked(dev.ID)
		return
	}
	publishJSON(cli, hdpStateTopic+sanitizeDeviceID(dev.ID), true, map[string]any{"type": "state", "device_id": sanitizeDeviceID(dev.ID), "state": state})
}

func publishState(cli mqtt.Client, store *bridgeStore, gate *commandStateGate, optimistic bool) {
	for _, dev := range store.list() {
		publishDeviceState(cli, dev, gate, optimistic)
	}
}

func clearRetainedDevice(cli mqtt.Client, deviceID string) {
	if cli == nil {
		return
	}
	sanitized := sanitizeDeviceID(deviceID)
	tMeta := cli.Publish(hdpMetaTopic+sanitized, 0, true, []byte{})
	_ = tMeta.WaitTimeout(5 * time.Second)
	tState := cli.Publish(hdpStateTopic+sanitized, 0, true, []byte{})
	_ = tState.WaitTimeout(5 * time.Second)
}

func publishCommandResult(cli mqtt.Client, deviceID string, corr string, success bool, status string, errMsg string) {
	payload := map[string]any{"type": "command_result", "device_id": deviceID, "corr": corr, "success": success, "status": status}
	if strings.TrimSpace(errMsg) != "" {
		payload["error"] = errMsg
	}
	publishJSON(cli, hdpCmdResTopic+deviceID, false, payload)
}

func runSync(ctx context.Context, setup *setupStore, provider thinqProvider, store *bridgeStore, cli mqtt.Client, hub *wsHub, source string, gate *commandStateGate) error {
	started := time.Now()
	cfg, err := setup.load()
	if err != nil {
		logWarnf("sync source=%s load setup failed err=%v", source, err)
		return err
	}
	if provider == nil {
		logWarnf("sync source=%s provider missing", source)
		return errors.New("provider is not configured")
	}
	logInfof("sync start source=%s provider=%s config=%s", source, provider.Name(), summarizeSetupForLog(cfg))
	devices, err := provider.ListDevices(ctx, cfg)
	if err != nil {
		logWarnf("sync source=%s provider=%s list devices failed err=%v", source, provider.Name(), err)
		return err
	}
	removed := store.replace(devices)
	publishMetadata(cli, store)
	publishState(cli, store, gate, false)
	for _, deviceID := range removed {
		clearRetainedDevice(cli, deviceID)
	}
	logInfof("sync completed source=%s provider=%s devices=%d removed=%d duration_ms=%d", source, provider.Name(), len(devices), len(removed), time.Since(started).Milliseconds())
	hub.broadcast(map[string]any{"type": "sync_completed", "source": source, "provider": provider.Name(), "count": len(devices), "ts": time.Now().UTC().UnixMilli(), "devices": store.allSnapshot()})
	return nil
}

func startRefreshLoop(ctx context.Context, setup *setupStore, provider thinqProvider, cli mqtt.Client, store *bridgeStore, syncNow <-chan struct{}, hub *wsHub, gate *commandStateGate) {
	logInfof("refresh loop started mode=periodic provider=%s mqtt_enabled=%t", provider.Name(), cli != nil)
	handleSync := func(source string) {
		logDebugf("refresh trigger source=%s", source)
		hub.broadcast(map[string]any{"type": "sync_started", "source": source, "ts": time.Now().UTC().UnixMilli()})
		if err := runSync(ctx, setup, provider, store, cli, hub, source, gate); err != nil {
			hub.broadcast(map[string]any{"type": "sync_failed", "source": source, "error": err.Error(), "ts": time.Now().UTC().UnixMilli()})
		}
	}
	nextInterval := func() time.Duration {
		cfg, err := setup.load()
		if err != nil {
			return 3 * time.Minute
		}
		sec := cfg.SyncIntervalSec
		if sec <= 0 {
			sec = 180
		}
		if sec < 120 {
			sec = 120
		}
		if sec > 3600 {
			sec = 3600
		}
		return time.Duration(sec) * time.Second
	}
	resetTimer := func(timer *time.Timer) {
		interval := nextInterval()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
	}
	handleSync("boot")
	timer := time.NewTimer(nextInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			logInfof("refresh loop stopping due to context cancellation")
			return
		case <-syncNow:
			handleSync("manual")
			resetTimer(timer)
		case <-timer.C:
			handleSync("interval")
			resetTimer(timer)
		}
	}
}

type thinQCountry struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func fetchThinQCountries(ctx context.Context) ([]thinQCountry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://kr.emp.lgsmartplatform.com/emp/v2.0/info/countries?svc_list=SVC202", nil)
	if err != nil {
		return nil, err
	}
	// This EMP endpoint requires the same headers the official ThinQ PAT portal sends.
	// The signature is base64(HMAC-SHA256(appKey, timestampSeconds)).
	appKey := "VSH36EHUTQVBRRUJVHPR0B4C0TGSMJP6"
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(appKey))
	_, _ = mac.Write([]byte(ts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Application-Key", appKey)
	req.Header.Set("X-Device-Country", "KR")
	req.Header.Set("X-Device-Language", "ko-KR")
	req.Header.Set("X-Device-Language-Type", "IETF")
	req.Header.Set("X-Device-Platform", "PC")
	req.Header.Set("X-Device-Publish-Flag", "Y")
	req.Header.Set("X-Device-Type", "P01")
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", sig)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("country list request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Info struct {
			ServiceList []struct {
				SvcCode   string `json:"svcCode"`
				CntryList []struct {
					Code string `json:"cntryCode"`
					Name string `json:"cntryName"`
				} `json:"cntryList"`
			} `json:"serviceList"`
		} `json:"info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	countries := make([]thinQCountry, 0, 200)
	for _, svc := range payload.Info.ServiceList {
		if strings.TrimSpace(strings.ToUpper(svc.SvcCode)) != "SVC202" {
			continue
		}
		for _, item := range svc.CntryList {
			code := strings.ToUpper(strings.TrimSpace(item.Code))
			if code == "" {
				continue
			}
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = code
			}
			countries = append(countries, thinQCountry{Code: code, Name: name})
		}
	}
	sort.Slice(countries, func(i, j int) bool {
		return countries[i].Name < countries[j].Name
	})
	if len(countries) == 0 {
		return nil, fmt.Errorf("country list response did not contain SVC202 countries")
	}
	return countries, nil
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "code": status})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Permissions-Policy", "accelerometer=(), ambient-light-sensor=(), autoplay=(), battery=(), camera=(), clipboard-read=(), clipboard-write=(), display-capture=(), encrypted-media=(), fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(), usb=()")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

func main() {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8099"
	}

	baseDir := "."
	if _, err := os.Stat(filepath.Join(baseDir, "manifest", "homenavi-integration.json")); err != nil {
		baseDir = "/app"
	}

	auth, err := newAdminAuthFromEnv()
	if err != nil {
		log.Printf("auth disabled due to init error: %v", err)
		auth = &adminAuth{enabled: false}
	}

	setup := &setupStore{path: defaultSetupPath()}
	setupCfg, _ := setup.load()
	logInfof("startup config=%s", summarizeSetupForLog(setupCfg))

	brokerURL := strings.TrimSpace(os.Getenv("MQTT_BROKER_URL"))
	if brokerURL == "" {
		brokerURL = "mqtt://mosquitto:1883"
	}
	if setupCfg.SyncIntervalSec <= 0 {
		setupCfg.SyncIntervalSec = 180
	}

	store := newBridgeStore()
	stateGate := newCommandStateGate(commandStateGateTimeout)
	syncNow := make(chan struct{}, 1)
	hub := newWSHub()
	authMgr := newAuthFlowManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cloudProvider := newCloudThinQProvider()
	realtimeBridge := newThinQRealtimeBridge(setup, cloudProvider)

	mqttInitTimeout := 2 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("LG_THINQ_MQTT_INIT_TIMEOUT_SEC")); raw != "" {
		if sec, parseErr := strconv.Atoi(raw); parseErr == nil && sec >= 0 {
			mqttInitTimeout = time.Duration(sec) * time.Second
		}
	}

	mqttClient, err := newMQTTClientWithRetry(brokerURL, mqttInitTimeout, 3*time.Second)
	if err != nil {
		logWarnf("mqtt disabled due to init error: %v", err)
	}
	if mqttClient != nil {
		go realtimeBridge.Run(ctx, mqttClient, syncNow, hub)
		tok := mqttClient.Subscribe(hdpCmdTopic, 0, func(_ mqtt.Client, msg mqtt.Message) {
			logInfof("mqtt command received topic=%s bytes=%d", msg.Topic(), len(msg.Payload()))
			var payload struct {
				DeviceID string         `json:"device_id"`
				Command  string         `json:"command"`
				Args     map[string]any `json:"args"`
				Corr     string         `json:"corr"`
			}
			if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
				logWarnf("mqtt command decode failed: %v", err)
				return
			}
			requestedID := strings.TrimSpace(payload.DeviceID)
			if requestedID == "" {
				requestedID = strings.TrimPrefix(msg.Topic(), "homenavi/hdp/device/command/")
			}
			corr := strings.TrimSpace(payload.Corr)
			if corr == "" {
				corr = "lgthinq-" + strings.ReplaceAll(requestedID, "/", "-") + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
			}

			lookupIDs := []string{requestedID}
			if strings.HasPrefix(requestedID, "lgthinq/") {
				raw := strings.TrimPrefix(requestedID, "lgthinq/")
				if raw != "" {
					lookupIDs = append(lookupIDs, raw)
				}
			} else {
				lookupIDs = append(lookupIDs, sanitizeDeviceID(requestedID))
			}

			var (
				dev thinqDevice
				ok  bool
			)
			for _, lookupID := range lookupIDs {
				if d, found := store.get(lookupID); found {
					dev = d
					ok = true
					break
				}
			}
			if !ok {
				logWarnf("mqtt command device not found requested_id=%s", requestedID)
				publishCommandResult(mqttClient, sanitizeDeviceID(requestedID), corr, false, "failed", "device not found")
				return
			}

			targetID := strings.TrimSpace(dev.ID)
			if targetID == "" {
				targetID = strings.TrimPrefix(requestedID, "lgthinq/")
			}

			cmd, cmdErr := translateHDPCommand(dev, payload.Command, payload.Args)
			if cmdErr != nil {
				logWarnf("mqtt command rejected requested_id=%s command=%s error=%v", requestedID, payload.Command, cmdErr)
				publishCommandResult(mqttClient, sanitizeDeviceID(requestedID), corr, false, "rejected", cmdErr.Error())
				hub.broadcast(map[string]any{"type": "command_failed", "device_id": sanitizeDeviceID(requestedID), "command": payload.Command, "error": cmdErr.Error(), "ts": time.Now().UTC().UnixMilli()})
				return
			}

			expected := expectedStateForCommand(dev, cmd)
			baseline := mapThinQToHDPState(dev)
			stateGate.trackWithBaseline(targetID, corr, expected, baseline)

			cfg, cfgErr := setup.load()
			if cfgErr != nil {
				logWarnf("mqtt command failed loading setup requested_id=%s err=%v", requestedID, cfgErr)
				stateGate.cancel(targetID, corr)
				publishCommandResult(mqttClient, sanitizeDeviceID(requestedID), corr, false, "failed", cfgErr.Error())
				return
			}
			if err := cloudProvider.SendCommand(ctx, cfg, targetID, cmd); err != nil {
				logWarnf("mqtt command provider failed requested_id=%s target_id=%s command=%s error=%v", requestedID, targetID, cmd.Name, err)
				stateGate.cancel(targetID, corr)
				publishCommandResult(mqttClient, sanitizeDeviceID(requestedID), corr, false, "failed", err.Error())
				hub.broadcast(map[string]any{"type": "command_failed", "device_id": sanitizeDeviceID(requestedID), "command": cmd.Name, "provider": cloudProvider.Name(), "error": err.Error(), "ts": time.Now().UTC().UnixMilli()})
				return
			}
			stateGate.extendFreeze(targetID, corr, commandStateGatePostGrace)
			optimisticDev := dev
			optimisticDev.ID = targetID
			optimisticDev.State = cloneAnyMap(dev.State)
			if optimisticDev.State == nil {
				optimisticDev.State = map[string]any{}
			}
			applyOptimisticState(optimisticDev.State, optimisticDev.Type, cmd)
			publishDeviceState(mqttClient, optimisticDev, stateGate, true)
			store.apply(targetID, cmd)
			logInfof("mqtt command applied requested_id=%s target_id=%s command=%s", requestedID, targetID, cmd.Name)
			publishCommandResult(mqttClient, sanitizeDeviceID(requestedID), corr, true, "applied", "")
			hub.broadcast(map[string]any{"type": "command_applied", "device_id": sanitizeDeviceID(requestedID), "command": cmd.Name, "provider": cloudProvider.Name(), "ts": time.Now().UTC().UnixMilli(), "devices": store.allSnapshot()})
		})
		if tok.Wait() && tok.Error() != nil {
			logWarnf("mqtt command subscribe failed: %v", tok.Error())
		} else {
			logInfof("mqtt command subscribe active topic=%s", hdpCmdTopic)
		}
		go startRefreshLoop(ctx, setup, cloudProvider, mqttClient, store, syncNow, hub, stateGate)
		defer mqttClient.Disconnect(250)
	} else {
		go realtimeBridge.Run(ctx, nil, syncNow, hub)
		go startRefreshLoop(ctx, setup, cloudProvider, nil, store, syncNow, hub, stateGate)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})

	noCache := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			next.ServeHTTP(w, r)
		})
	}

	serveFile := func(rel string) http.HandlerFunc {
		abs := filepath.Join(baseDir, rel)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			http.ServeFile(w, r, abs)
		}
	}

	mux.HandleFunc("/.well-known/homenavi-integration.json", serveFile("manifest/homenavi-integration.json"))
	mux.HandleFunc("/.well-known/homenavi-automation-steps.json", serveFile("web/.well-known/homenavi-automation-steps.json"))
	mux.HandleFunc("/.well-known/homenavi-capabilities.json", serveFile("web/.well-known/homenavi-capabilities.json"))

	mux.Handle("/assets/", noCache(http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(baseDir, "web", "assets"))))))
	mux.Handle("/ui/", noCache(http.StripPrefix("/ui/", http.FileServer(http.Dir(filepath.Join(baseDir, "web", "ui"))))))
	mux.Handle("/widgets/", noCache(http.StripPrefix("/widgets/", http.FileServer(http.Dir(filepath.Join(baseDir, "web", "widgets"))))))

	mux.HandleFunc("/api/admin/setup", func(w http.ResponseWriter, r *http.Request) {
		logInfof("api setup method=%s", r.Method)
		if !auth.requireRole(w, r, "admin") {
			return
		}
		if r.Method == http.MethodGet {
			cfg, err := setup.load()
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to load setup")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"setup": statusFromConfig(cfg)})
			return
		}
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Determine whether PAT was explicitly provided by the caller.
		// This lets us preserve stored PAT when the UI omits it or sends an empty string,
		// while still validating a newly entered PAT during save.
		patProvided := false
		{
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(body, &raw); err == nil {
				if _, ok := raw["pat_token"]; ok {
					patProvided = true
				}
			}
		}
		var payload setupConfig
		if err := json.Unmarshal(body, &payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
		existing, existingErr := setup.load()
		if existingErr != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to load existing setup")
			return
		}
		payload.AccountEmail = strings.TrimSpace(payload.AccountEmail)
		payload.AccountRegion = strings.TrimSpace(payload.AccountRegion)
		payload.PATToken = strings.TrimSpace(payload.PATToken)
		payload.APIKey = strings.TrimSpace(payload.APIKey)
		payload.Country = strings.TrimSpace(payload.Country)
		payload.ServicePhase = strings.TrimSpace(payload.ServicePhase)
		payload.ClientID = strings.TrimSpace(payload.ClientID)
		payload.AccessToken = strings.TrimSpace(payload.AccessToken)
		payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
		payload.Mode = strings.ToLower(strings.TrimSpace(payload.Mode))
		if payload.Mode == "" {
			payload.Mode = "cloud"
		}
		payload.APIBaseURL = strings.TrimSpace(payload.APIBaseURL)
		payload.AuthPasswordURL = strings.TrimSpace(payload.AuthPasswordURL)
		payload.OAuthAuthURL = strings.TrimSpace(payload.OAuthAuthURL)
		payload.OAuthTokenURL = strings.TrimSpace(payload.OAuthTokenURL)
		payload.OAuthClientID = strings.TrimSpace(payload.OAuthClientID)
		payload.OAuthClientSecret = strings.TrimSpace(payload.OAuthClientSecret)
		payload.OAuthScopes = strings.TrimSpace(payload.OAuthScopes)
		payload.RealtimeTransport = strings.TrimSpace(payload.RealtimeTransport)
		payload.RealtimeClientCertPEM = strings.TrimSpace(payload.RealtimeClientCertPEM)
		payload.RealtimeClientKeyPEM = strings.TrimSpace(payload.RealtimeClientKeyPEM)
		payload.RealtimeCertExpiresAt = strings.TrimSpace(payload.RealtimeCertExpiresAt)

		if payload.PATToken != "" && strings.TrimSpace(payload.AccessToken) == "" {
			payload.AccessToken = payload.PATToken
		}

		// PAT is intentionally not returned to the UI; if the user leaves it blank when
		// saving changes to other fields, preserve the existing stored PAT.
		if payload.PATToken == "" {
			payload.PATToken = strings.TrimSpace(existing.PATToken)
			if strings.TrimSpace(payload.AccessToken) == "" {
				payload.AccessToken = strings.TrimSpace(existing.AccessToken)
			}
		}
		if payload.RealtimeSubscriptions == nil {
			payload.RealtimeSubscriptions = []string{}
		}
		payload = applySetupDefaults(payload)
		existingPAT := strings.TrimSpace(existing.PATToken)
		newPAT := strings.TrimSpace(payload.PATToken)
		patChanged := patProvided && newPAT != "" && newPAT != existingPAT
		if payload.Mode != "cloud" {
			writeJSONError(w, http.StatusBadRequest, "mode must be cloud")
			return
		}
		if payload.AccountRegion == "" {
			payload.AccountRegion = "eu"
		}
		if payload.SyncIntervalSec <= 0 {
			payload.SyncIntervalSec = 180
		}
		if payload.SyncIntervalSec < 120 {
			payload.SyncIntervalSec = 120
		}
		if payload.SyncIntervalSec > 3600 {
			payload.SyncIntervalSec = 3600
		}
		if payload.RealtimeReconnectSec <= 0 {
			payload.RealtimeReconnectSec = 15
		}
		if payload.RealtimeReconnectSec < 5 {
			payload.RealtimeReconnectSec = 5
		}
		if payload.RealtimeReconnectSec > 300 {
			payload.RealtimeReconnectSec = 300
		}
		if payload.RealtimeTransport != "mqtt" && payload.RealtimeTransport != "ws" {
			writeJSONError(w, http.StatusBadRequest, "realtime_transport must be mqtt or ws")
			return
		}
		if patChanged {
			count, verifyErr := verifyThinQLogin(r.Context(), payload, cloudProvider)
			if verifyErr != nil {
				logWarnf("api setup pat verify failed err=%v", verifyErr)
				authMgr.set(authStatus{Success: false, Provider: "pat", Message: "PAT verify failed: " + verifyErr.Error(), Time: time.Now().UTC(), Mode: payload.Mode})
				writeJSONError(w, http.StatusBadGateway, "PAT token verification failed: "+verifyErr.Error())
				return
			}
			logInfof("api setup pat verified devices=%d", count)
			authMgr.set(authStatus{Success: true, Provider: "pat", Message: "PAT verified", Time: time.Now().UTC(), Mode: payload.Mode, DeviceCount: count})
		}
		if err := setup.save(payload); err != nil {
			logWarnf("api setup save failed err=%v payload=%s", err, summarizeSetupForLog(payload))
			writeJSONError(w, http.StatusInternalServerError, "failed to save setup")
			return
		}
		logInfof("api setup saved payload=%s", summarizeSetupForLog(payload))
		if !patChanged {
			authMgr.set(authStatus{Success: true, Provider: "setup", Message: "Setup saved", Time: time.Now().UTC(), Mode: payload.Mode})
		}
		hub.broadcast(map[string]any{"type": "setup_updated", "ts": time.Now().UTC().UnixMilli(), "setup": statusFromConfig(payload)})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "setup": statusFromConfig(payload)})
	})

	mux.HandleFunc("/api/admin/countries", func(w http.ResponseWriter, r *http.Request) {
		if !auth.requireRole(w, r, "admin") {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		countries, err := fetchThinQCountries(r.Context())
		if err != nil {
			logWarnf("api countries fetch failed err=%v", err)
			writeJSONError(w, http.StatusBadGateway, "failed to fetch ThinQ country list")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"countries": countries})
	})

	mux.HandleFunc("/api/admin/auth/status", func(w http.ResponseWriter, r *http.Request) {
		if !auth.requireRole(w, r, "admin") {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": authMgr.get()})
	})

	mux.HandleFunc("/api/admin/auth/login", func(w http.ResponseWriter, r *http.Request) {
		logInfof("api auth login method=%s", r.Method)
		if !auth.requireRole(w, r, "admin") {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			PATToken string `json:"pat_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
		cfg, err := setup.load()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to load setup")
			return
		}
		pat := strings.TrimSpace(req.PATToken)
		if pat != "" {
			cfg.PATToken = pat
			cfg.AccessToken = pat
		}
		cfg = applySetupDefaults(cfg)
		if strings.TrimSpace(cfg.PATToken) == "" {
			writeJSONError(w, http.StatusBadRequest, "pat_token is required. Generate one at https://connect-pat.lgthinq.com")
			return
		}
		if err := setup.save(cfg); err != nil {
			logWarnf("api auth login failed persisting setup err=%v", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to persist tokens")
			return
		}
		count, verifyErr := verifyThinQLogin(r.Context(), cfg, cloudProvider)
		if verifyErr != nil {
			logWarnf("api auth login verify failed err=%v", verifyErr)
			authMgr.set(authStatus{Success: false, Provider: "pat", Message: "token saved but verify failed: " + verifyErr.Error(), Time: time.Now().UTC(), Mode: cfg.Mode})
			writeJSONError(w, http.StatusBadGateway, "PAT token saved but verification failed: "+verifyErr.Error())
			return
		}
		logInfof("api auth login verified devices=%d", count)
		authMgr.set(authStatus{Success: true, Provider: "pat", Message: "PAT verified", Time: time.Now().UTC(), Mode: cfg.Mode, DeviceCount: count})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "verified": true, "device_count": count, "setup": statusFromConfig(cfg)})
	})

	mux.HandleFunc("/api/admin/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		logInfof("api auth verify method=%s", r.Method)
		if !auth.requireRole(w, r, "admin") {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cfg, err := setup.load()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to load setup")
			return
		}
		count, err := verifyThinQLogin(r.Context(), cfg, cloudProvider)
		if err != nil {
			logWarnf("api auth verify failed err=%v", err)
			authMgr.set(authStatus{Success: false, Provider: cloudProvider.Name(), Message: err.Error(), Time: time.Now().UTC(), Mode: cfg.Mode})
			writeJSONError(w, http.StatusBadGateway, "verification failed: "+err.Error())
			return
		}
		logInfof("api auth verify succeeded devices=%d", count)
		authMgr.set(authStatus{Success: true, Provider: cloudProvider.Name(), Message: "Verification succeeded", Time: time.Now().UTC(), Mode: cfg.Mode, DeviceCount: count})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "verified": true, "device_count": count, "provider": cloudProvider.Name()})
	})

	mux.HandleFunc("/api/admin/auth/oauth/import", func(w http.ResponseWriter, r *http.Request) {
		if !auth.requireRole(w, r, "admin") {
			return
		}
		writeJSONError(w, http.StatusGone, "OAuth flow is deprecated for this integration. Use PAT token setup instead.")
	})

	mux.HandleFunc("/api/admin/auth/oauth/start", func(w http.ResponseWriter, r *http.Request) {
		if !auth.requireRole(w, r, "admin") {
			return
		}
		writeJSONError(w, http.StatusGone, "OAuth flow is deprecated for this integration. Use PAT token setup instead.")
	})

	mux.HandleFunc("/api/admin/auth/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusGone, "OAuth callback is deprecated for this integration. Use PAT token setup instead.")
	})

	mux.HandleFunc("/api/admin/sync-now", func(w http.ResponseWriter, r *http.Request) {
		logInfof("api sync-now method=%s", r.Method)
		if !auth.requireRole(w, r, "resident") {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		select {
		case syncNow <- struct{}{}:
			logInfof("api sync-now queued")
		default:
			logDebugf("api sync-now queue already had pending item")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "queued"})
	})

	mux.HandleFunc("/api/admin/device-command", func(w http.ResponseWriter, r *http.Request) {
		logInfof("api device-command method=%s", r.Method)
		if !auth.requireRole(w, r, "resident") {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			DeviceID string         `json:"device_id"`
			Command  string         `json:"command"`
			Args     map[string]any `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWarnf("api device-command invalid json err=%v", err)
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
		deviceID := strings.TrimSpace(req.DeviceID)
		if deviceID == "" {
			writeJSONError(w, http.StatusBadRequest, "device_id is required")
			return
		}
		dev, ok := store.get(deviceID)
		if !ok {
			logWarnf("api device-command device not found id=%s", deviceID)
			writeJSONError(w, http.StatusNotFound, "device not found")
			return
		}
		cmd, err := translateHDPCommand(dev, req.Command, req.Args)
		if err != nil {
			logWarnf("api device-command translate failed id=%s command=%s err=%v", deviceID, req.Command, err)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		expected := expectedStateForCommand(dev, cmd)
		corr := "admin-" + strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
		baseline := mapThinQToHDPState(dev)
		stateGate.trackWithBaseline(deviceID, corr, expected, baseline)
		cfg, err := setup.load()
		if err != nil {
			logWarnf("api device-command setup load failed id=%s err=%v", deviceID, err)
			stateGate.cancel(deviceID, corr)
			writeJSONError(w, http.StatusInternalServerError, "failed to load setup")
			return
		}
		if err := cloudProvider.SendCommand(r.Context(), cfg, deviceID, cmd); err != nil {
			logWarnf("api device-command provider failed id=%s command=%s err=%v", deviceID, cmd.Name, err)
			stateGate.cancel(deviceID, corr)
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		stateGate.extendFreeze(deviceID, corr, commandStateGatePostGrace)
		optimisticDev := dev
		optimisticDev.ID = deviceID
		optimisticDev.State = cloneAnyMap(dev.State)
		if optimisticDev.State == nil {
			optimisticDev.State = map[string]any{}
		}
		applyOptimisticState(optimisticDev.State, optimisticDev.Type, cmd)
		publishDeviceState(mqttClient, optimisticDev, stateGate, true)
		store.apply(deviceID, cmd)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "queued_sync": false})
	})

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}}
	mux.HandleFunc("/api/realtime/ws", func(w http.ResponseWriter, r *http.Request) {
		logInfof("api realtime ws connect attempt")
		if !auth.requireRole(w, r, "resident") {
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logWarnf("api realtime ws upgrade failed err=%v", err)
			return
		}
		logInfof("api realtime ws connected remote=%s", r.RemoteAddr)
		hub.add(conn)
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_ = conn.WriteJSON(map[string]any{"type": "hello", "ts": time.Now().UTC().UnixMilli(), "devices": store.allSnapshot()})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				hub.remove(conn)
				_ = conn.Close()
				logInfof("api realtime ws disconnected remote=%s err=%v", r.RemoteAddr, err)
				return
			}
		}
	})

	mux.HandleFunc("/api/realtime/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !auth.requireRole(w, r, "resident") {
			return
		}
		cfg, _ := setup.load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": store.allSnapshot(), "setup": statusFromConfig(cfg), "provider": cloudProvider.Name()})
	})

	mux.HandleFunc("/api/automation/execute", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req automationExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid json"})
			return
		}
		actionID := strings.TrimSpace(req.ActionID)
		switch actionID {
		case "integration.lgthinq.sync_now":
			select {
			case syncNow <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"status": "queued", "message": "sync scheduled"}})
		case "integration.lgthinq.account_reconnect":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"status": "queued", "message": "account reconnect requested"}})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "unsupported action_id"})
		}
	})

	addr := ":" + port
	logInfof("homenavi-lg-thinq listening on %s log_level=%s", addr, firstNonEmpty(lgThinQLogLevel, "info"))
	httpSrv := &http.Server{Addr: addr, Handler: accessLog(securityHeaders(mux)), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
