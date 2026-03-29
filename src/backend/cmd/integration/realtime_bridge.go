package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type thinQRealtimeBridge struct {
	setup    *setupStore
	provider *cloudThinQProvider

	mu            sync.Mutex
	lastSyncPulse time.Time
}

type thinQRouteInfo struct {
	APIServer       string
	MQTTServer      string
	WebSocketServer string
}

type thinQCertInfo struct {
	CertificatePEM string
	PrivateKeyPEM  string
	Subscriptions  []string
	ExpiresAt      time.Time
}

const defaultThinQRealtimeSyncMinInterval = 5 * time.Second

func newThinQRealtimeBridge(setup *setupStore, provider *cloudThinQProvider) *thinQRealtimeBridge {
	return &thinQRealtimeBridge{setup: setup, provider: provider}
}

func (b *thinQRealtimeBridge) Run(ctx context.Context, localMQTT mqtt.Client, syncNow chan<- struct{}, hub *wsHub) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cfg, err := b.setup.load()
		if err != nil {
			logWarnf("thinq realtime setup load failed err=%v", err)
			if !sleepWithContext(ctx, 10*time.Second) {
				return
			}
			continue
		}
		if !cfg.RealtimeEnabled {
			if !sleepWithContext(ctx, 15*time.Second) {
				return
			}
			continue
		}

		err = b.runSession(ctx, cfg, localMQTT, syncNow, hub)
		if err != nil {
			logWarnf("thinq realtime session ended err=%v", err)
			if hub != nil {
				hub.broadcast(map[string]any{"type": "thinq_realtime_disconnected", "error": err.Error(), "ts": time.Now().UTC().UnixMilli()})
			}
		}

		reconnect := time.Duration(cfg.RealtimeReconnectSec) * time.Second
		if reconnect < 5*time.Second {
			reconnect = 5 * time.Second
		}
		if reconnect > 5*time.Minute {
			reconnect = 5 * time.Minute
		}
		if !sleepWithContext(ctx, reconnect) {
			return
		}
	}
}

func (b *thinQRealtimeBridge) runSession(ctx context.Context, cfg setupConfig, localMQTT mqtt.Client, syncNow chan<- struct{}, hub *wsHub) error {
	route, err := b.fetchRoute(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fetch route: %w", err)
	}

	if err := b.registerClient(ctx, cfg); err != nil {
		return fmt.Errorf("register client: %w", err)
	}

	cfg, certInfo, err := b.ensureClientCertificate(ctx, cfg)
	if err != nil {
		return fmt.Errorf("ensure client certificate: %w", err)
	}
	if err := b.ensureEventSubscriptions(ctx, cfg); err != nil {
		logWarnf("thinq realtime event subscription check failed err=%v", err)
	}

	endpoint := realtimeEndpointForRoute(route, cfg.RealtimeTransport)
	if endpoint == "" {
		return fmt.Errorf("realtime endpoint missing for transport=%s", cfg.RealtimeTransport)
	}
	serverName := tlsServerNameFromEndpoint(endpoint)

	if hub != nil {
		hub.broadcast(map[string]any{"type": "thinq_realtime_connecting", "transport": cfg.RealtimeTransport, "endpoint": endpoint, "ts": time.Now().UTC().UnixMilli()})
	}
	logInfof("thinq realtime connecting transport=%s endpoint=%s topics=%d", cfg.RealtimeTransport, endpoint, len(certInfo.Subscriptions))

	tlsConfig, err := thinQRealtimeTLSConfig(certInfo.CertificatePEM, certInfo.PrivateKeyPEM, serverName)
	if err != nil {
		return fmt.Errorf("tls config: %w", err)
	}

	lostCh := make(chan error, 1)
	opts := mqtt.NewClientOptions()
	opts.AddBroker(endpoint)
	opts.SetTLSConfig(tlsConfig)
	opts.SetAutoReconnect(false)
	opts.SetCleanSession(true)
	opts.SetProtocolVersion(4)
	opts.SetConnectTimeout(20 * time.Second)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetDialer(&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second})
	opts.SetClientID(firstNonEmpty(strings.TrimSpace(cfg.ClientID), "homenavi-lg-thinq-client"))
	if cfg.RealtimeTransport == "ws" {
		opts.SetHTTPHeaders(http.Header{"Sec-WebSocket-Protocol": []string{"mqtt"}})
	}
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		b.handleRealtimeMessage(cfg, localMQTT, syncNow, hub, msg)
	})
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		select {
		case lostCh <- err:
		default:
		}
	}
	opts.OnConnect = func(client mqtt.Client) {
		for _, topic := range certInfo.Subscriptions {
			tok := client.Subscribe(topic, 1, nil)
			if tok.Wait() && tok.Error() != nil {
				logWarnf("thinq realtime subscribe failed topic=%s err=%v", topic, tok.Error())
			} else {
				logInfof("thinq realtime subscribed topic=%s", topic)
			}
		}
		if hub != nil {
			hub.broadcast(map[string]any{"type": "thinq_realtime_connected", "transport": cfg.RealtimeTransport, "topics": len(certInfo.Subscriptions), "ts": time.Now().UTC().UnixMilli()})
		}
	}

	client := mqtt.NewClient(opts)
	conn := client.Connect()
	if conn.Wait() && conn.Error() != nil {
		return conn.Error()
	}
	defer client.Disconnect(250)

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-lostCh:
			if err == nil {
				err = fmt.Errorf("connection lost")
			}
			return err
		}
	}
}

func (b *thinQRealtimeBridge) handleRealtimeMessage(cfg setupConfig, localMQTT mqtt.Client, syncNow chan<- struct{}, hub *wsHub, msg mqtt.Message) {
	payloadBytes := msg.Payload()
	payloadRaw := string(payloadBytes)

	var parsed map[string]any
	_ = json.Unmarshal(payloadBytes, &parsed)

	deviceID := firstNonEmpty(
		asString(parsed["deviceId"]),
		asString(parsed["device_id"]),
	)
	if eventMap, ok := parsed["event"].(map[string]any); ok {
		deviceID = firstNonEmpty(deviceID, asString(eventMap["deviceId"]))
	}
	if pushMap, ok := parsed["push"].(map[string]any); ok {
		deviceID = firstNonEmpty(deviceID, asString(pushMap["deviceId"]))
	}

	if localMQTT != nil {
		relay := map[string]any{
			"type":      "thinq_realtime",
			"topic":     msg.Topic(),
			"device_id": sanitizeDeviceID(deviceID),
			"payload":   payloadRaw,
		}
		publishJSON(localMQTT, "homenavi/integration/lgthinq/realtime/raw", false, relay)
	}

	if hub != nil {
		hub.broadcast(map[string]any{
			"type":      "thinq_realtime_event",
			"topic":     msg.Topic(),
			"device_id": sanitizeDeviceID(deviceID),
			"ts":        time.Now().UTC().UnixMilli(),
		})
	}

	if syncNow != nil {
		now := time.Now()
		if b.allowRealtimeSync(now) {
			select {
			case syncNow <- struct{}{}:
				logDebugf("thinq realtime sync requested topic=%s device_id=%s transport=%s", msg.Topic(), sanitizeDeviceID(deviceID), cfg.RealtimeTransport)
			default:
				logDebugf("thinq realtime sync already queued topic=%s device_id=%s transport=%s", msg.Topic(), sanitizeDeviceID(deviceID), cfg.RealtimeTransport)
			}
		} else {
			logDebugf("thinq realtime sync throttled topic=%s device_id=%s transport=%s", msg.Topic(), sanitizeDeviceID(deviceID), cfg.RealtimeTransport)
		}
	}
}

func (b *thinQRealtimeBridge) allowRealtimeSync(now time.Time) bool {
	if b == nil {
		return true
	}
	interval := thinQRealtimeSyncMinInterval()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.lastSyncPulse.IsZero() && now.Sub(b.lastSyncPulse) < interval {
		return false
	}
	b.lastSyncPulse = now
	return true
}

func thinQRealtimeSyncMinInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LG_THINQ_REALTIME_SYNC_MIN_SEC"))
	if raw == "" {
		return defaultThinQRealtimeSyncMinInterval
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec <= 0 {
		return defaultThinQRealtimeSyncMinInterval
	}
	return time.Duration(sec) * time.Second
}

func (b *thinQRealtimeBridge) fetchRoute(ctx context.Context, cfg setupConfig) (thinQRouteInfo, error) {
	urlStr := strings.TrimRight(normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion), "/") + "/route"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return thinQRouteInfo{}, err
	}
	b.provider.applyHeaders(req, cfg)
	req.Header.Set("Accept", "application/json")

	resp, err := b.provider.client.Do(req)
	if err != nil {
		return thinQRouteInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return thinQRouteInfo{}, parseThinQAPIError("route", resp)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return thinQRouteInfo{}, err
	}
	response := unwrapThinQPayload(payload)
	return thinQRouteInfo{
		APIServer:       firstNonEmpty(strings.TrimSpace(asString(response["apiServer"])), strings.TrimSpace(asString(response["api_server"]))),
		MQTTServer:      firstNonEmpty(strings.TrimSpace(asString(response["mqttServer"])), strings.TrimSpace(asString(response["mqtt_server"]))),
		WebSocketServer: firstNonEmpty(strings.TrimSpace(asString(response["webSocketServer"])), strings.TrimSpace(asString(response["websocketServer"])), strings.TrimSpace(asString(response["web_socket_server"]))),
	}, nil
}

func realtimeEndpointForRoute(route thinQRouteInfo, transport string) string {
	endpoint := strings.TrimSpace(route.MQTTServer)
	if strings.EqualFold(strings.TrimSpace(transport), "ws") {
		endpoint = strings.TrimSpace(route.WebSocketServer)
	}
	return normalizeThinQBroker(endpoint)
}

func (b *thinQRealtimeBridge) ensureClientCertificate(ctx context.Context, cfg setupConfig) (setupConfig, thinQCertInfo, error) {
	if thinQCertificateLooksValid(cfg.RealtimeClientCertPEM, cfg.RealtimeClientKeyPEM, cfg.RealtimeCertExpiresAt) && len(cfg.RealtimeSubscriptions) > 0 {
		expiresAt := parseTimeRFC3339(cfg.RealtimeCertExpiresAt)
		return cfg, thinQCertInfo{
			CertificatePEM: cfg.RealtimeClientCertPEM,
			PrivateKeyPEM:  cfg.RealtimeClientKeyPEM,
			Subscriptions:  dedupeAndSort(cfg.RealtimeSubscriptions),
			ExpiresAt:      expiresAt,
		}, nil
	}

	privateKeyPEM, csrPEM, err := generateThinQCSR()
	if err != nil {
		return cfg, thinQCertInfo{}, err
	}

	certificatePEM, subscriptions, expiresAt, err := b.requestClientCertificate(ctx, cfg, csrPEM)
	if err != nil {
		return cfg, thinQCertInfo{}, err
	}
	cfg.RealtimeClientKeyPEM = strings.TrimSpace(privateKeyPEM)
	cfg.RealtimeClientCertPEM = strings.TrimSpace(certificatePEM)
	cfg.RealtimeSubscriptions = dedupeAndSort(subscriptions)
	if !expiresAt.IsZero() {
		cfg.RealtimeCertExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	if err := b.setup.save(cfg); err != nil {
		return cfg, thinQCertInfo{}, err
	}
	return cfg, thinQCertInfo{
		CertificatePEM: cfg.RealtimeClientCertPEM,
		PrivateKeyPEM:  cfg.RealtimeClientKeyPEM,
		Subscriptions:  cfg.RealtimeSubscriptions,
		ExpiresAt:      parseTimeRFC3339(cfg.RealtimeCertExpiresAt),
	}, nil
}

func thinQCertificateLooksValid(certPEM, keyPEM, expiresRaw string) bool {
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
		return false
	}
	if !thinQPrivateKeyLooksSupported(keyPEM) {
		return false
	}
	expires := parseTimeRFC3339(expiresRaw)
	if expires.IsZero() {
		return true
	}
	return time.Until(expires) > 24*time.Hour
}

func thinQPrivateKeyLooksSupported(keyPEM string) bool {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return false
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil && k != nil {
		return true
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		_, ok := k.(*rsa.PrivateKey)
		return ok
	}
	return false
}

func (b *thinQRealtimeBridge) requestClientCertificate(ctx context.Context, cfg setupConfig, csrPEM string) (string, []string, time.Time, error) {
	urlStr := strings.TrimRight(normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion), "/") + "/client/certificate"
	body := map[string]any{
		"body": map[string]any{
			"service-code": "SVC202",
			"csr":          csrPEM,
		},
	}
	payload, err := b.doThinQJSON(ctx, cfg, http.MethodPost, urlStr, body)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	response, _ := payload["response"].(map[string]any)
	result, _ := response["result"].(map[string]any)
	certPEM := firstNonEmpty(asString(result["certificatePem"]), asString(response["certificatePem"]))
	if strings.TrimSpace(certPEM) == "" {
		return "", nil, time.Time{}, fmt.Errorf("client certificate response missing certificatePem")
	}

	subscriptions := make([]string, 0, 8)
	if arr, ok := result["subscriptions"].([]any); ok {
		for _, item := range arr {
			topic := strings.TrimSpace(asString(item))
			if topic != "" {
				subscriptions = append(subscriptions, topic)
			}
		}
	}
	if arr, ok := response["subscriptions"].([]any); ok {
		for _, item := range arr {
			topic := strings.TrimSpace(asString(item))
			if topic != "" {
				subscriptions = append(subscriptions, topic)
			}
		}
	}
	if len(subscriptions) == 0 {
		return "", nil, time.Time{}, fmt.Errorf("client certificate response missing subscriptions")
	}

	expiresAt := extractCertExpiry(certPEM)
	return certPEM, dedupeAndSort(subscriptions), expiresAt, nil
}

func (b *thinQRealtimeBridge) registerClient(ctx context.Context, cfg setupConfig) error {
	urlStr := strings.TrimRight(normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion), "/") + "/client"
	body := map[string]any{
		"body": map[string]any{
			"type":         "MQTT",
			"service-code": "SVC202",
			"device-type":  "607",
		},
	}
	_, err := b.doThinQJSON(ctx, cfg, http.MethodPost, urlStr, body)
	if err == nil {
		return nil
	}
	var apiErr *thinQAPIError
	if errors.As(err, &apiErr) {
		msg := strings.ToLower(strings.TrimSpace(apiErr.Message))
		if strings.TrimSpace(apiErr.Code) == "9000" || strings.Contains(msg, "already registered") {
			return nil
		}
	}
	return err
}

func (b *thinQRealtimeBridge) ensureEventSubscriptions(ctx context.Context, cfg setupConfig) error {
	devices, err := b.provider.ListDevices(ctx, cfg)
	if err != nil {
		return err
	}
	subscribed, err := b.fetchEventSubscriptions(ctx, cfg)
	if err != nil {
		return err
	}
	for _, device := range devices {
		id := strings.TrimSpace(device.ID)
		if id == "" {
			continue
		}
		if _, ok := subscribed[id]; ok {
			continue
		}
		if err := b.subscribeEvent(ctx, cfg, id); err != nil {
			logWarnf("thinq realtime event subscribe failed device_id=%s err=%v", id, err)
		}
	}
	return nil
}

func (b *thinQRealtimeBridge) fetchEventSubscriptions(ctx context.Context, cfg setupConfig) (map[string]struct{}, error) {
	urlStr := strings.TrimRight(normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion), "/") + "/event"
	payload, err := b.doThinQJSON(ctx, cfg, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	response, _ := payload["response"].([]any)
	out := map[string]struct{}{}
	for _, item := range response {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(asString(m["deviceId"]))
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

func (b *thinQRealtimeBridge) subscribeEvent(ctx context.Context, cfg setupConfig, deviceID string) error {
	urlStr := strings.TrimRight(normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion), "/") + "/event/" + url.PathEscape(deviceID) + "/subscribe"
	body := map[string]any{"expire": map[string]any{"unit": "HOUR", "timer": 24}}
	_, err := b.doThinQJSON(ctx, cfg, http.MethodPost, urlStr, body)
	return err
}

func (b *thinQRealtimeBridge) doThinQJSON(ctx context.Context, cfg setupConfig, method, urlStr string, body map[string]any) (map[string]any, error) {
	var reqBodyReader *strings.Reader
	if body == nil {
		reqBodyReader = strings.NewReader("")
	} else {
		bts, _ := json.Marshal(body)
		reqBodyReader = strings.NewReader(string(bts))
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBodyReader)
	if err != nil {
		return nil, err
	}
	b.provider.applyHeaders(req, cfg)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.provider.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseThinQAPIError(method+" "+urlStr, resp)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func generateThinQCSR() (privateKeyPEM string, csrPEM string, err error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}))

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: "homenavi-lg-thinq"},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}, privateKey)
	if err != nil {
		return "", "", err
	}
	csrPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	return privateKeyPEM, csrPEM, nil
}

func extractCertExpiry(certPEM string) time.Time {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter.UTC()
}

func parseTimeRFC3339(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func tlsServerNameFromEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err == nil {
		if host := strings.TrimSpace(u.Hostname()); host != "" {
			return host
		}
	}
	return ""
}

func thinQRealtimeTLSConfig(certPEM, keyPEM string, serverName string) (*tls.Config, error) {
	keyPair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{keyPair}, MinVersion: tls.VersionTLS12, ServerName: strings.TrimSpace(serverName)}, nil
}

func dedupeAndSort(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeThinQBroker(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if strings.HasPrefix(endpoint, "mqtts://") {
		return "ssl://" + strings.TrimPrefix(endpoint, "mqtts://")
	}
	if strings.HasPrefix(endpoint, "mqtt://") {
		return "tcp://" + strings.TrimPrefix(endpoint, "mqtt://")
	}
	if strings.HasPrefix(endpoint, "wss://") || strings.HasPrefix(endpoint, "ws://") || strings.HasPrefix(endpoint, "ssl://") || strings.HasPrefix(endpoint, "tcp://") {
		return endpoint
	}
	return endpoint
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
