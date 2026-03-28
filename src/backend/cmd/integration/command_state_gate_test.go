package main

import (
	"testing"
	"time"
)

func TestCommandStateGate_ConfirmedStateIsGatedUntilSatisfied(t *testing.T) {
	g := &commandStateGate{
		pending: map[string]pendingStateExpectation{},
		timeout: 200 * time.Millisecond,
		freeze:  50 * time.Millisecond,
	}

	deviceID := "device-1"
	expected := map[string]any{"power": "on"}
	g.track(deviceID, "c1", expected)

	// Synced publish should be blocked while the expected state hasn't arrived.
	if g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be blocked during freeze")
	}

	// After freeze, still block until expected is satisfied.
	time.Sleep(60 * time.Millisecond)
	if g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be blocked after freeze until expected satisfied")
	}

	// When state matches expected, it should allow and clear.
	if !g.allow(deviceID, map[string]any{"power": "on"}) {
		t.Fatalf("expected synced publish to be allowed when expected satisfied")
	}
	if !g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected gate to be cleared after satisfaction")
	}
}

func TestCommandStateGate_ExpectedStatePassesDuringFreeze(t *testing.T) {
	g := &commandStateGate{
		pending: map[string]pendingStateExpectation{},
		timeout: 200 * time.Millisecond,
		freeze:  150 * time.Millisecond,
	}

	deviceID := "device-freeze-match"
	g.track(deviceID, "c-match", map[string]any{"power": "on"})

	if !g.allow(deviceID, map[string]any{"power": "on"}) {
		t.Fatalf("expected confirmed matching state to pass during freeze")
	}
	if !g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected gate to be cleared after confirmed match")
	}
}

func TestCommandStateGate_TimeoutAllowsThrough(t *testing.T) {
	g := &commandStateGate{
		pending: map[string]pendingStateExpectation{},
		timeout: 80 * time.Millisecond,
		freeze:  0,
	}
	deviceID := "device-2"
	g.track(deviceID, "c2", map[string]any{"power": "on"})

	if g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be blocked before timeout")
	}

	time.Sleep(90 * time.Millisecond)
	if !g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be allowed after timeout")
	}
}

func TestCommandStateGate_NoExpectedGatesForFreezeOnly(t *testing.T) {
	g := &commandStateGate{
		pending: map[string]pendingStateExpectation{},
		timeout: 200 * time.Millisecond,
		freeze:  50 * time.Millisecond,
	}
	deviceID := "device-3"
	g.track(deviceID, "c3", nil)

	if g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be blocked during freeze")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if g.allow(deviceID, map[string]any{"power": "off"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected synced publish to be allowed after freeze when no expected state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandStateGate_NoExpectedRequiresChangeWhenBaselineProvided(t *testing.T) {
	g := &commandStateGate{
		pending: map[string]pendingStateExpectation{},
		timeout: 200 * time.Millisecond,
		freeze:  50 * time.Millisecond,
	}
	deviceID := "device-4"
	g.trackWithBaseline(deviceID, "c4", nil, map[string]any{"power": "off"})

	// During freeze, always block.
	if g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be blocked during freeze")
	}

	// After freeze, still block if state hasn't changed from baseline.
	time.Sleep(60 * time.Millisecond)
	if g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected synced publish to be blocked until state changes from baseline")
	}

	// When state changes, allow and clear.
	if !g.allow(deviceID, map[string]any{"power": "on"}) {
		t.Fatalf("expected synced publish to be allowed when state changed from baseline")
	}
	if !g.allow(deviceID, map[string]any{"power": "off"}) {
		t.Fatalf("expected gate to be cleared after allowing")
	}
}
