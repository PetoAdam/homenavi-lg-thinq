package main

import (
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
