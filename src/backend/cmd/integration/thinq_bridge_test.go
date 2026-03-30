package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMapThinQToHDPStateTV(t *testing.T) {
	d := thinqDevice{
		ID:     "tv-1",
		Type:   "tv",
		Online: true,
		State: map[string]any{
			"power": "ON", "volume": 140, "muted": "true", "input": "hdmi2", "playback": "playing",
		},
	}
	state := mapThinQToHDPState(d)
	if state["power"] != "on" {
		t.Fatalf("expected power on, got %#v", state["power"])
	}
	if state["volume"] != 100 {
		t.Fatalf("expected volume clamp 100, got %#v", state["volume"])
	}
	if state["muted"] != true {
		t.Fatalf("expected muted true, got %#v", state["muted"])
	}
}

func TestMapThinQToHDPStateWasher(t *testing.T) {
	d := thinqDevice{
		ID:     "washer-1",
		Type:   "washer",
		Online: true,
		State: map[string]any{
			"run_state": "running", "cycle": "quick", "remaining_min": "55", "door_locked": 1,
			"response": []any{map[string]any{"runState": map[string]any{"currentState": "START"}}},
		},
	}
	state := mapThinQToHDPState(d)
	if state["run_state"] != "running" {
		t.Fatalf("expected running, got %#v", state["run_state"])
	}
	if state["remaining_min"] != 55 {
		t.Fatalf("expected remaining 55, got %#v", state["remaining_min"])
	}
	if state["door_locked"] != true {
		t.Fatalf("expected door_locked true, got %#v", state["door_locked"])
	}
	if state["operation_mode"] != "START" {
		t.Fatalf("expected operation_mode START, got %#v", state["operation_mode"])
	}
}

func TestMapThinQToHDPStateWasherOperationOnlyReport(t *testing.T) {
	d := thinqDevice{
		ID:     "washer-2",
		Type:   "washer",
		Online: true,
		State: map[string]any{
			"response": []any{map[string]any{
				"location":            map[string]any{"locationName": "MAIN"},
				"operation":           map[string]any{"washerOperationMode": "START"},
				"timer":               map[string]any{"remainHour": 0, "remainMinute": 43},
				"remoteControlEnable": map[string]any{"remoteControlEnabled": true},
			}},
		},
	}
	state := mapThinQToHDPState(d)
	if state["run_state"] != "running" {
		t.Fatalf("expected running, got %#v", state["run_state"])
	}
	if state["operation_mode"] != "START" {
		t.Fatalf("expected operation_mode START, got %#v", state["operation_mode"])
	}
	if state["remaining_min"] != 43 {
		t.Fatalf("expected remaining 43, got %#v", state["remaining_min"])
	}
	if state["remote_control_enabled"] != true {
		t.Fatalf("expected remote_control_enabled true, got %#v", state["remote_control_enabled"])
	}
}

func TestTranslateTVCommand(t *testing.T) {
	d := thinqDevice{ID: "tv-1", Type: "tv"}
	cmd, err := translateHDPCommand(d, "", map[string]any{"volume": 44})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cmd.Name != "set_volume" {
		t.Fatalf("expected set_volume, got %s", cmd.Name)
	}
	if cmd.Params["volume"] != 44 {
		t.Fatalf("expected volume 44, got %#v", cmd.Params["volume"])
	}
}

func TestTranslateTVNativeSetStatePowerCommand(t *testing.T) {
	d := thinqDevice{ID: "tv-1", Type: "tv"}
	cmd, err := translateHDPCommand(d, "set_state", map[string]any{"power": "on"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cmd.Name != "set_power" {
		t.Fatalf("expected set_power, got %s", cmd.Name)
	}
	if cmd.Params["power"] != "on" {
		t.Fatalf("expected power on, got %#v", cmd.Params["power"])
	}
}

func TestTranslateWasherCommand(t *testing.T) {
	d := thinqDevice{
		ID:   "washer-1",
		Type: "washer",
		State: map[string]any{
			"response": []any{map[string]any{"location": map[string]any{"locationName": "MAIN"}}},
			"profile": map[string]any{
				"property": []any{map[string]any{
					"operation": map[string]any{
						"washerOperationMode": map[string]any{"value": map[string]any{"w": []any{"START", "STOP", "POWER_ON", "POWER_OFF"}}},
					},
				}},
			},
		},
	}
	cmd, err := translateHDPCommand(d, "start", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cmd.Name != "start" {
		t.Fatalf("expected start, got %s", cmd.Name)
	}
	if cmd.CtrlKey != "washerOperationMode" {
		t.Fatalf("expected ctrl key washerOperationMode, got %#v", cmd.CtrlKey)
	}
	if cmd.Command != "START" {
		t.Fatalf("expected command START, got %#v", cmd.Command)
	}
	location, ok := cmd.Params["location"].(map[string]any)
	if !ok || location["locationName"] != "MAIN" {
		t.Fatalf("expected location MAIN, got %#v", cmd.Params["location"])
	}
	operation, ok := cmd.Params["operation"].(map[string]any)
	if !ok || operation["washerOperationMode"] != "START" {
		t.Fatalf("expected operation START, got %#v", cmd.Params["operation"])
	}
}

func TestTranslateWasherNativeSetStateOperationMode(t *testing.T) {
	d := thinqDevice{
		ID:   "washer-1",
		Type: "washer",
		State: map[string]any{
			"response": []any{map[string]any{"location": map[string]any{"locationName": "MAIN"}}},
			"profile": map[string]any{
				"property": []any{map[string]any{
					"operation": map[string]any{
						"washerOperationMode": map[string]any{"value": map[string]any{"w": []any{"START", "STOP", "POWER_ON", "POWER_OFF"}}},
					},
				}},
			},
		},
	}
	cmd, err := translateHDPCommand(d, "set_state", map[string]any{"operation_mode": "START"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cmd.Name != "set_operation_mode" {
		t.Fatalf("expected set_operation_mode, got %s", cmd.Name)
	}
	if cmd.CtrlKey != "washerOperationMode" {
		t.Fatalf("expected ctrl key washerOperationMode, got %#v", cmd.CtrlKey)
	}
	if cmd.Command != "START" {
		t.Fatalf("expected command START, got %#v", cmd.Command)
	}
	operation, ok := cmd.Params["operation"].(map[string]any)
	if !ok || operation["washerOperationMode"] != "START" {
		t.Fatalf("expected operation START, got %#v", cmd.Params["operation"])
	}
}

func TestTranslateWasherNativeSetStatePowerCommandUsesOperationCtrl(t *testing.T) {
	d := thinqDevice{
		ID:   "washer-1",
		Type: "washer",
		State: map[string]any{
			"response": []any{map[string]any{"location": map[string]any{"locationName": "MAIN"}}},
			"profile": map[string]any{
				"property": []any{map[string]any{
					"operation": map[string]any{
						"washerOperationMode": map[string]any{"value": map[string]any{"w": []any{"START", "STOP", "POWER_ON", "POWER_OFF"}}},
					},
				}},
			},
		},
	}
	cmd, err := translateHDPCommand(d, "set_state", map[string]any{"power": "on"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cmd.Name != "set_power" {
		t.Fatalf("expected set_power, got %s", cmd.Name)
	}
	if cmd.CtrlKey != "washerOperationMode" {
		t.Fatalf("expected ctrl key washerOperationMode, got %#v", cmd.CtrlKey)
	}
	if cmd.Command != "POWER_ON" {
		t.Fatalf("expected command POWER_ON, got %#v", cmd.Command)
	}
}

func TestExpectedStateForCommandWasherPowerOn(t *testing.T) {
	d := thinqDevice{
		ID:   "washer-1",
		Type: "washer",
	}

	cmd := thinqCommand{Name: "set_power", Params: map[string]any{"operation": map[string]any{"washerOperationMode": "POWER_ON"}}}
	expected := expectedStateForCommand(d, cmd)
	if expected["power"] != "on" {
		t.Fatalf("expected power on, got %#v", expected["power"])
	}
	if len(expected) != 1 {
		t.Fatalf("expected a minimal confirmed-state patch, got %#v", expected)
	}
}

func TestSanitizeDeviceID(t *testing.T) {
	got := sanitizeDeviceID("device-1")
	if got != "lgthinq/device-1" {
		t.Fatalf("unexpected id %q", got)
	}
}

func TestCloudProviderListDevicesOpenAPIHeaders(t *testing.T) {
	t.Parallel()
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/devices" && r.URL.Path != "/devices/dev-1/profile" {
			t.Fatalf("expected /devices or /devices/dev-1/profile path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-pat" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		if got := r.Header.Get("x-country"); got != "HU" {
			t.Fatalf("unexpected x-country: %q", got)
		}
		if got := r.Header.Get("x-client-id"); got != "homenavi-lg-thinq-client" {
			t.Fatalf("unexpected x-client-id: %q", got)
		}
		if got := r.Header.Get("x-api-key"); got == "" {
			t.Fatalf("missing x-api-key")
		}
		if got := r.Header.Get("x-message-id"); strings.TrimSpace(got) == "" {
			t.Fatalf("missing x-message-id")
		}
		if r.URL.Path == "/devices" {
			seen = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"devices":[{"deviceId":"dev-1","deviceType":"WASHER","deviceInfo":{"alias":"Laundry"},"state":{"power":"off"}}]}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"profile":{"dummy":true}}}`))
	}))
	defer srv.Close()

	provider := newCloudThinQProvider()
	cfg := applySetupDefaults(setupConfig{
		PATToken:      "test-pat",
		APIBaseURL:    srv.URL,
		Country:       "hu",
		AccountRegion: "eu",
	})

	devices, err := provider.ListDevices(context.Background(), cfg)
	if err != nil {
		t.Fatalf("list devices failed: %v", err)
	}
	if !seen {
		t.Fatalf("server did not receive request")
	}
	if len(devices) != 1 {
		t.Fatalf("expected one device, got %d", len(devices))
	}
}

func TestCloudProviderControlAndStatePathsFollowOpenAPI(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path] = true
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/devices/dev-1/control":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode control payload: %v", err)
			}
			if payload["ctrlKey"] != "washerOperationMode" {
				t.Fatalf("unexpected ctrlKey: %#v", payload["ctrlKey"])
			}
			if payload["command"] != "POWER_ON" {
				t.Fatalf("unexpected command: %#v", payload["command"])
			}
			if got := r.Header.Get("x-country"); got != "HU" {
				t.Fatalf("unexpected x-country: %q", got)
			}
			if got := r.Header.Get("x-message-id"); strings.TrimSpace(got) == "" {
				t.Fatalf("missing x-message-id")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"status":"ok"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/devices/dev-1/state":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"state":{"power":"on"}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/devices/dev-1/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"profile":{"foo":"bar"}}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	provider := newCloudThinQProvider()
	cfg := applySetupDefaults(setupConfig{
		PATToken:      "test-pat",
		APIBaseURL:    srv.URL,
		Country:       "HU",
		AccountRegion: "eu",
	})

	cmd := thinqCommand{
		Name:    "set_power",
		CtrlKey: "washerOperationMode",
		Command: "POWER_ON",
		Params:  map[string]any{"location": "MAIN"},
	}
	if err := provider.SendCommand(context.Background(), cfg, "dev-1", cmd); err != nil {
		t.Fatalf("send command failed: %v", err)
	}
	if _, err := provider.fetchState(context.Background(), cfg, srv.URL, "dev-1"); err != nil {
		t.Fatalf("fetch state failed: %v", err)
	}
	if _, err := provider.fetchProfile(context.Background(), cfg, srv.URL, "dev-1"); err != nil {
		t.Fatalf("fetch profile failed: %v", err)
	}

	for _, key := range []string{
		http.MethodPost + " /devices/dev-1/control",
		http.MethodGet + " /devices/dev-1/state",
		http.MethodGet + " /devices/dev-1/profile",
	} {
		if !seen[key] {
			t.Fatalf("missing request %s", key)
		}
	}
}

func TestCloudProviderListDevicesUsesConfiguredCountryOnly(t *testing.T) {
	t.Parallel()

	countriesTried := make([]string, 0, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devices" {
			http.NotFound(w, r)
			return
		}
		country := strings.ToUpper(strings.TrimSpace(r.Header.Get("x-country")))
		countriesTried = append(countriesTried, country)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Not supported country","code":"1307"}}`))
	}))
	defer srv.Close()

	p := &cloudThinQProvider{client: srv.Client()}
	cfg := applySetupDefaults(setupConfig{
		APIBaseURL:    srv.URL,
		PATToken:      "token",
		APIKey:        "api-key",
		Country:       "HU",
		ServicePhase:  "OP",
		ClientID:      "cid",
		AccountRegion: "eu",
	})

	_, err := p.ListDevices(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected ListDevices error")
	}
	if len(countriesTried) != 1 {
		t.Fatalf("expected exactly one country attempt, got=%v", countriesTried)
	}
	if countriesTried[0] != "HU" {
		t.Fatalf("configured country should be used, got=%v", countriesTried)
	}
	if !strings.Contains(err.Error(), "1307") || !strings.Contains(err.Error(), "Not supported country") {
		t.Fatalf("expected concrete ThinQ error in message, got=%v", err)
	}
}

func TestCloudProviderVerifyLoginUsesOnlyDeviceList(t *testing.T) {
	t.Parallel()
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path]++
		if r.URL.Path != "/devices" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"devices":[{"deviceId":"dev-1"},{"deviceId":"dev-2"}]}}`))
	}))
	defer srv.Close()

	provider := newCloudThinQProvider()
	provider.client = srv.Client()
	cfg := applySetupDefaults(setupConfig{
		PATToken:      "test-pat",
		APIBaseURL:    srv.URL,
		Country:       "HU",
		AccountRegion: "eu",
	})

	count, err := provider.VerifyLogin(context.Background(), cfg)
	if err != nil {
		t.Fatalf("verify login failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 devices, got %d", count)
	}
	if seen[http.MethodGet+" /devices"] != 1 {
		t.Fatalf("expected exactly one device-list request, got %v", seen)
	}
	if len(seen) != 1 {
		t.Fatalf("expected no state/profile calls during verify, got %v", seen)
	}
}

func TestParseThinQAPIErrorKeepsMessageAndCode(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Exceeded User API calls","code":"1314"}}`)),
	}
	err := parseThinQAPIError("device list", resp)
	if err == nil {
		t.Fatalf("expected parseThinQAPIError to return error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "1314") || !strings.Contains(msg, "Exceeded User API calls") {
		t.Fatalf("expected preserved API details, got=%s", msg)
	}
}

func TestProviderHTTPStatusUsesThinQStatusForClientErrors(t *testing.T) {
	t.Parallel()
	err := &thinQAPIError{Status: http.StatusUnauthorized, Code: "1307", Message: "Not supported country"}
	if got := providerHTTPStatus(err, http.StatusBadGateway); got != http.StatusUnauthorized {
		t.Fatalf("expected 401 passthrough, got %d", got)
	}
	err = &thinQAPIError{Status: http.StatusTooManyRequests, Code: "1314", Message: "Exceeded User API calls"}
	if got := providerHTTPStatus(err, http.StatusBadGateway); got != http.StatusTooManyRequests {
		t.Fatalf("expected 429 passthrough, got %d", got)
	}
	err = &thinQAPIError{Status: http.StatusInternalServerError, Message: "upstream error"}
	if got := providerHTTPStatus(err, http.StatusBadGateway); got != http.StatusBadGateway {
		t.Fatalf("expected fallback 502 for upstream 5xx, got %d", got)
	}
}
