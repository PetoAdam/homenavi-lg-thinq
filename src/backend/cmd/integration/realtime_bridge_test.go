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
		nil,
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

	bridge.handleRealtimeMessage(setupConfig{RealtimeTransport: "mqtt"}, nil, syncNow, nil, nil, nil, msg)
	bridge.handleRealtimeMessage(setupConfig{RealtimeTransport: "mqtt"}, nil, syncNow, nil, nil, nil, msg)

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
	if got := realtimeEndpointForRoute(thinQRouteInfo{MQTTServer: "mqtts://mqtt.example.com:8883"}, "ws"); got != "ssl://mqtt.example.com:8883" {
		t.Fatalf("expected fallback to mqtt endpoint, got %q", got)
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

func TestThinQRealtimeBridge_HandleRealtimeMessageAppliesRealtimeState(t *testing.T) {
	bridge := newThinQRealtimeBridge(nil, nil)
	store := newBridgeStore()
	store.replace([]thinqDevice{{
		ID:     "device-1",
		Type:   "washer",
		Online: true,
		State: map[string]any{
			"response": []any{map[string]any{
				"location": map[string]any{"locationName": "MAIN"},
				"runState": map[string]any{"currentState": "POWER_OFF"},
			}},
		},
	}})
	gate := newCommandStateGate(time.Second)
	gate.trackWithBaseline("device-1", "corr-1", map[string]any{"run_state": "running"}, map[string]any{"power": "off"})
	syncNow := make(chan struct{}, 1)

	bridge.handleRealtimeMessage(
		setupConfig{RealtimeTransport: "mqtt"},
		nil,
		syncNow,
		nil,
		store,
		gate,
		stubMQTTMessage{
			topic:   "test/topic",
			payload: []byte(`{"timestamp":"2026-03-30T10:00:00Z","event":{"deviceId":"device-1","report":{"location":{"locationName":"MAIN"},"operation":{"washerOperationMode":"START"},"timer":{"remainHour":0,"remainMinute":42},"remoteControlEnable":{"remoteControlEnabled":true}}}}`),
		},
	)

	updated, ok := store.get("device-1")
	if !ok {
		t.Fatalf("expected updated device in store")
	}
	state := mapThinQToHDPState(updated)
	if state["run_state"] != "running" {
		t.Fatalf("expected running, got %#v", state["run_state"])
	}
	if state["remaining_min"] != 42 {
		t.Fatalf("expected remaining 42, got %#v", state["remaining_min"])
	}
	if state["remote_control_enabled"] != true {
		t.Fatalf("expected remote_control_enabled true, got %#v", state["remote_control_enabled"])
	}
	if gate.hasPending("device-1", "corr-1") {
		t.Fatalf("expected realtime state to clear pending command gate")
	}
	if got := len(syncNow); got != 0 {
		t.Fatalf("expected no sync queue when realtime state was applied, got %d", got)
	}
}

func TestThinQRealtimeBridge_HandleRealtimeMessageQueuesSyncForPushOnly(t *testing.T) {
	bridge := newThinQRealtimeBridge(nil, nil)
	syncNow := make(chan struct{}, 1)

	bridge.handleRealtimeMessage(
		setupConfig{RealtimeTransport: "mqtt"},
		nil,
		syncNow,
		nil,
		newBridgeStore(),
		nil,
		stubMQTTMessage{
			topic:   "test/topic",
			payload: []byte(`{"push":{"pushType":"DEVICE_PUSH","deviceId":"device-1","pushCode":"WASHING_IS_COMPLETE"}}`),
		},
	)

	if got := len(syncNow); got != 1 {
		t.Fatalf("expected push-only realtime message to queue a sync, got %d", got)
	}
}
