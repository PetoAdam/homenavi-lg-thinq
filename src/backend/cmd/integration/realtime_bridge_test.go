package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubMQTTMessage struct {
	topic   string
	payload []byte
}

func (m stubMQTTMessage) Duplicate() bool { return false }
func (m stubMQTTMessage) Qos() byte       { return 0 }
func (m stubMQTTMessage) Retained() bool  { return false }
func (m stubMQTTMessage) Topic() string   { return m.topic }
func (m stubMQTTMessage) MessageID() uint16 {
	return 0
}
func (m stubMQTTMessage) Payload() []byte { return m.payload }
func (m stubMQTTMessage) Ack()            {}

func TestThinQRealtimeBridge_HandleRealtimeMessageQueuesSync(t *testing.T) {
	bridge := newThinQRealtimeBridge(nil, nil)
	syncNow := make(chan struct{}, 1)

	bridge.handleRealtimeMessage(
		setupConfig{RealtimeTransport: "mqtt"},
		nil,
		syncNow,
		nil,
		stubMQTTMessage{
			topic:   "test/topic",
			payload: []byte(`{"deviceId":"device-1","event":{"state":"changed"}}`),
		},
	)

	select {
	case <-syncNow:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected realtime message to queue an immediate sync")
	}
}

func TestThinQRealtimeBridge_HandleRealtimeMessageThrottlesSync(t *testing.T) {
	bridge := newThinQRealtimeBridge(nil, nil)
	syncNow := make(chan struct{}, 10)

	msg := stubMQTTMessage{
		topic:   "test/topic",
		payload: []byte(`{"deviceId":"device-1","event":{"state":"changed"}}`),
	}

	bridge.handleRealtimeMessage(setupConfig{RealtimeTransport: "mqtt"}, nil, syncNow, nil, msg)
	bridge.handleRealtimeMessage(setupConfig{RealtimeTransport: "mqtt"}, nil, syncNow, nil, msg)

	if got := len(syncNow); got != 1 {
		t.Fatalf("expected a single queued sync during throttle window, got %d", got)
	}
}

func TestRealtimeEndpointForRouteSupportsMQTTAndWS(t *testing.T) {
	route := thinQRouteInfo{
		MQTTServer:      "mqtts://mqtt.example.com:8883",
		WebSocketServer: "wss://ws.example.com/mqtt",
	}
	if got := realtimeEndpointForRoute(route, "mqtt"); got != "ssl://mqtt.example.com:8883" {
		t.Fatalf("unexpected mqtt endpoint: %q", got)
	}
	if got := realtimeEndpointForRoute(route, "ws"); got != "wss://ws.example.com/mqtt" {
		t.Fatalf("unexpected ws endpoint: %q", got)
	}
}

func TestThinQRealtimeBridgeFetchRouteUsesHeadersAndUnwrapsResult(t *testing.T) {
	t.Parallel()
	var authz string
	var apiKey string
	var country string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/route" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		authz = r.Header.Get("Authorization")
		apiKey = r.Header.Get("x-api-key")
		country = r.Header.Get("x-country")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"apiServer":"https://api.example.com","mqttServer":"mqtts://mqtt.example.com:8883","webSocketServer":"wss://ws.example.com/mqtt"}}`))
	}))
	defer srv.Close()

	provider := newCloudThinQProvider()
	provider.client = srv.Client()
	bridge := newThinQRealtimeBridge(nil, provider)
	cfg := applySetupDefaults(setupConfig{
		PATToken:      "test-pat",
		APIBaseURL:    srv.URL,
		APIKey:        "api-key",
		Country:       "HU",
		AccountRegion: "eu",
	})

	route, err := bridge.fetchRoute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("fetchRoute failed: %v", err)
	}
	if authz != "Bearer test-pat" {
		t.Fatalf("unexpected authorization header: %q", authz)
	}
	if apiKey != "api-key" {
		t.Fatalf("unexpected api key: %q", apiKey)
	}
	if country != "HU" {
		t.Fatalf("unexpected country header: %q", country)
	}
	if route.MQTTServer != "mqtts://mqtt.example.com:8883" {
		t.Fatalf("unexpected mqtt server: %q", route.MQTTServer)
	}
	if route.WebSocketServer != "wss://ws.example.com/mqtt" {
		t.Fatalf("unexpected ws server: %q", route.WebSocketServer)
	}
}
