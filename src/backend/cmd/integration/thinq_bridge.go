package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type thinqDevice struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Manufacturer string         `json:"manufacturer"`
	Model        string         `json:"model"`
	Online       bool           `json:"online"`
	State        map[string]any `json:"state"`
}

type thinqCommand struct {
	Name    string         `json:"name"`
	CtrlKey string         `json:"ctrl_key,omitempty"`
	Command string         `json:"command,omitempty"`
	Params  map[string]any `json:"params"`
}

type thinqProvider interface {
	Name() string
	ListDevices(ctx context.Context, cfg setupConfig) ([]thinqDevice, error)
	SendCommand(ctx context.Context, cfg setupConfig, deviceID string, cmd thinqCommand) error
}

type cloudThinQProvider struct {
	client *http.Client
}

func newCloudThinQProvider() *cloudThinQProvider {
	return &cloudThinQProvider{client: &http.Client{Timeout: 12 * time.Second}}
}

func (p *cloudThinQProvider) Name() string { return "cloud" }

func (p *cloudThinQProvider) ListDevices(ctx context.Context, cfg setupConfig) ([]thinqDevice, error) {
	base := normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion)
	if strings.TrimSpace(cfg.PATToken) == "" {
		return nil, fmt.Errorf("pat_token is required")
	}
	country := strings.ToUpper(strings.TrimSpace(cfg.Country))
	if country == "" {
		country = countryForRegion(cfg.AccountRegion)
		cfg.Country = country
	}
	logInfof("thinq list devices start base=%s region=%s country=%s", base, normalizeThinQRegion(cfg.AccountRegion), country)
	urlStr := strings.TrimRight(base, "/") + "/devices"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		logWarnf("thinq list devices request build failed err=%v", err)
		return nil, err
	}
	p.applyHeaders(req, cfg)
	resp, err := p.client.Do(req)
	if err != nil {
		logWarnf("thinq list devices request failed country=%s err=%v", country, err)
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := resp.StatusCode
		apiErr := parseThinQAPIError("device list", resp)
		logWarnf("thinq list devices non-2xx country=%s status=%d err=%v", country, status, apiErr)
		return nil, apiErr
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		_ = resp.Body.Close()
		logWarnf("thinq list devices decode failed country=%s err=%v", country, err)
		return nil, err
	}
	_ = resp.Body.Close()
	logInfof("thinq list devices success country=%s", country)
	body := unwrapThinQPayload(payload)
	items := extractThinQItems(body)
	devices := make([]thinqDevice, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		deviceInfo, _ := m["deviceInfo"].(map[string]any)
		id := firstNonEmpty(asString(m["deviceId"]), asString(m["id"]))
		if id == "" {
			continue
		}
		typeName := firstNonEmpty(asString(m["deviceType"]), asString(deviceInfo["deviceType"]), asString(m["type"]))
		dev := thinqDevice{
			ID:           id,
			Name:         firstNonEmpty(asString(deviceInfo["alias"]), asString(m["alias"]), asString(m["deviceName"]), asString(m["name"]), id),
			Type:         normalizeDeviceType(typeName),
			Manufacturer: firstNonEmpty(asString(m["manufacturer"]), "LG"),
			Model:        firstNonEmpty(asString(deviceInfo["modelName"]), asString(m["modelName"]), asString(m["model"])),
			Online:       asBool(firstNonEmpty(asString(m["online"]), asString(m["isOnline"]), "true")),
			State:        map[string]any{},
		}
		if rawState, ok := m["state"].(map[string]any); ok {
			dev.State = cloneAnyMap(rawState)
		}
		devices = append(devices, dev)
	}

	for i := range devices {
		if len(devices[i].State) > 0 {
			if profile, err := p.fetchProfile(ctx, cfg, base, devices[i].ID); err == nil && len(profile) > 0 {
				devices[i].State["profile"] = profile
			} else if err != nil {
				logDebugf("thinq fetch profile skipped device_id=%s err=%v", devices[i].ID, err)
			}
			continue
		}
		state, err := p.fetchState(ctx, cfg, base, devices[i].ID)
		if err != nil {
			logWarnf("thinq fetch state failed device_id=%s err=%v", devices[i].ID, err)
			continue
		}
		if profile, err := p.fetchProfile(ctx, cfg, base, devices[i].ID); err == nil && len(profile) > 0 {
			state["profile"] = profile
		} else if err != nil {
			logDebugf("thinq fetch profile failed device_id=%s err=%v", devices[i].ID, err)
		}
		devices[i].State = state
	}
	logInfof("thinq list devices completed mapped_devices=%d", len(devices))
	return devices, nil
}

func (p *cloudThinQProvider) SendCommand(ctx context.Context, cfg setupConfig, deviceID string, cmd thinqCommand) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("missing device id")
	}
	if strings.TrimSpace(cmd.Name) == "" {
		return fmt.Errorf("missing command name")
	}
	base := normalizeAPIBaseURL(cfg.APIBaseURL, cfg.AccountRegion)
	if strings.TrimSpace(cfg.PATToken) == "" {
		return fmt.Errorf("pat_token is required")
	}
	urlStr := strings.TrimRight(base, "/") + "/devices/" + url.PathEscape(deviceID) + "/control"
	payload := cloneAnyMap(cmd.Params)
	if payload == nil {
		payload = map[string]any{}
	}
	if strings.TrimSpace(cmd.CtrlKey) != "" || strings.TrimSpace(cmd.Command) != "" {
		ctrlKey := firstNonEmpty(strings.TrimSpace(cmd.CtrlKey), strings.TrimSpace(cmd.Name))
		command := firstNonEmpty(strings.TrimSpace(cmd.Command), strings.TrimSpace(cmd.Name))
		payload = map[string]any{
			"ctrlKey":     ctrlKey,
			"command":     command,
			"data":        cloneAnyMap(cmd.Params),
			"dataSetList": []any{cloneAnyMap(cmd.Params)},
		}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		logWarnf("thinq send command request build failed device_id=%s command=%s err=%v", deviceID, cmd.Name, err)
		return err
	}
	logInfof("thinq send command device_id=%s command=%s", deviceID, cmd.Name)
	p.applyHeaders(req, cfg)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		logWarnf("thinq send command request failed device_id=%s command=%s err=%v", deviceID, cmd.Name, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseThinQAPIError("device control", resp)
		logWarnf("thinq send command non-2xx device_id=%s command=%s status=%d err=%v", deviceID, cmd.Name, resp.StatusCode, apiErr)
		return apiErr
	}
	logInfof("thinq send command success device_id=%s command=%s", deviceID, cmd.Name)
	return nil
}

func (p *cloudThinQProvider) fetchState(ctx context.Context, cfg setupConfig, base, deviceID string) (map[string]any, error) {
	urlStr := strings.TrimRight(base, "/") + "/devices/" + url.PathEscape(deviceID) + "/state"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		logWarnf("thinq fetch state request build failed device_id=%s err=%v", deviceID, err)
		return nil, err
	}
	p.applyHeaders(req, cfg)
	resp, err := p.client.Do(req)
	if err != nil {
		logWarnf("thinq fetch state request failed device_id=%s err=%v", deviceID, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseThinQAPIError("device state", resp)
		logWarnf("thinq fetch state non-2xx device_id=%s status=%d err=%v", deviceID, resp.StatusCode, apiErr)
		return nil, apiErr
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	payload = unwrapThinQPayload(payload)
	if state, ok := payload["state"].(map[string]any); ok {
		return cloneAnyMap(state), nil
	}
	if data, ok := payload["data"].(map[string]any); ok {
		return cloneAnyMap(data), nil
	}
	return cloneAnyMap(payload), nil
}

func (p *cloudThinQProvider) fetchProfile(ctx context.Context, cfg setupConfig, base, deviceID string) (map[string]any, error) {
	urlStr := strings.TrimRight(base, "/") + "/devices/" + url.PathEscape(deviceID) + "/profile"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		logWarnf("thinq fetch profile request build failed device_id=%s err=%v", deviceID, err)
		return nil, err
	}
	p.applyHeaders(req, cfg)
	resp, err := p.client.Do(req)
	if err != nil {
		logWarnf("thinq fetch profile request failed device_id=%s err=%v", deviceID, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseThinQAPIError("device profile", resp)
		logWarnf("thinq fetch profile non-2xx device_id=%s status=%d err=%v", deviceID, resp.StatusCode, apiErr)
		return nil, apiErr
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	payload = unwrapThinQPayload(payload)
	if profile, ok := payload["profile"].(map[string]any); ok {
		return cloneAnyMap(profile), nil
	}
	if response, ok := payload["response"].(map[string]any); ok {
		return cloneAnyMap(response), nil
	}
	return cloneAnyMap(payload), nil
}

func (p *cloudThinQProvider) applyHeaders(req *http.Request, cfg setupConfig) {
	token := strings.TrimSpace(cfg.PATToken)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-api-key", strings.TrimSpace(cfg.APIKey))
	req.Header.Set("x-country", strings.ToUpper(strings.TrimSpace(cfg.Country)))
	req.Header.Set("x-service-phase", firstNonEmpty(strings.TrimSpace(cfg.ServicePhase), "OP"))
	req.Header.Set("x-client-id", firstNonEmpty(strings.TrimSpace(cfg.ClientID), "homenavi-lg-thinq-client"))
	req.Header.Set("x-message-id", thinQMessageID())
	req.Header.Set("Accept", "application/json")
	logDebugf("thinq headers prepared method=%s path=%s country=%s client_id=%s", req.Method, req.URL.Path, strings.ToUpper(strings.TrimSpace(cfg.Country)), firstNonEmpty(strings.TrimSpace(cfg.ClientID), "homenavi-lg-thinq-client"))
}

func thinQMessageID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func unwrapThinQPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	if result, ok := payload["result"].(map[string]any); ok {
		return result
	}
	if response, ok := payload["response"].(map[string]any); ok {
		return response
	}
	if data, ok := payload["data"].(map[string]any); ok {
		return data
	}
	return payload
}

func extractThinQItems(payload map[string]any) []any {
	for _, key := range []string{"response", "devices", "item", "items", "list", "data"} {
		if arr, ok := payload[key].([]any); ok {
			return arr
		}
	}
	return []any{}
}

type bridgeStore struct {
	mu      sync.RWMutex
	devices map[string]thinqDevice
}

func newBridgeStore() *bridgeStore { return &bridgeStore{devices: map[string]thinqDevice{}} }

func (s *bridgeStore) replace(devices []thinqDevice) []string {
	next := make(map[string]thinqDevice, len(devices))
	for _, d := range devices {
		if strings.TrimSpace(d.ID) == "" {
			continue
		}
		d.State = cloneAnyMap(d.State)
		next[d.ID] = d
	}
	removed := make([]string, 0)
	s.mu.Lock()
	for id := range s.devices {
		if _, ok := next[id]; !ok {
			removed = append(removed, id)
		}
	}
	s.devices = next
	s.mu.Unlock()
	return removed
}

func (s *bridgeStore) get(deviceID string) (thinqDevice, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[deviceID]
	if !ok {
		return thinqDevice{}, false
	}
	d.State = cloneAnyMap(d.State)
	return d, true
}

func (s *bridgeStore) list() []thinqDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]thinqDevice, 0, len(s.devices))
	for _, d := range s.devices {
		cloned := d
		cloned.State = cloneAnyMap(d.State)
		out = append(out, cloned)
	}
	return out
}

func (s *bridgeStore) allSnapshot() map[string]any {
	devs := s.list()
	out := make(map[string]any, len(devs))
	for _, d := range devs {
		caps := mapThinQCapabilities(d)
		out[d.ID] = map[string]any{
			"name":         d.Name,
			"type":         normalizeDeviceType(d.Type),
			"manufacturer": d.Manufacturer,
			"model":        d.Model,
			"online":       d.Online,
			"mapped_state": mapThinQToHDPState(d),
			"raw_state":    cloneAnyMap(d.State),
			"homenavi_id":  sanitizeDeviceID(d.ID),
			"protocol":     "lgthinq",
			"capabilities": caps,
			"inputs":       mapThinQInputs(caps, d),
		}
	}
	return out
}

func capability(id, name, kind, property, valueType string, read, write, event bool) map[string]any {
	return map[string]any{
		"id":         id,
		"name":       name,
		"kind":       kind,
		"property":   property,
		"value_type": valueType,
		"access": map[string]any{
			"read":  read,
			"write": write,
			"event": event,
		},
	}
}

func mapThinQCapabilities(d thinqDevice) []map[string]any {
	mapped := mapThinQToHDPState(d)
	profile, _ := d.State["profile"].(map[string]any)
	switch normalizeDeviceType(d.Type) {
	case "tv":
		base := []map[string]any{
			capability("switch", "Power", "actuator", "power", "string", true, true, true),
			capability("media.playback", "Playback", "actuator", "playback", "string", true, true, true),
			capability("media.volume", "Volume", "actuator", "volume", "number", true, true, true),
			capability("media.input", "Input", "actuator", "input", "string", true, true, true),
		}
		return enrichCapabilitiesWithStateProfile("tv", base, mapped, profile)
	case "washer":
		base := []map[string]any{
			capability("switch", "Power", "actuator", "power", "string", true, true, true),
			capability("appliance.washer.status", "Washer Status", "sensor", "run_state", "string", true, false, true),
			capability("appliance.washer.control", "Washer Control", "actuator", "cycle", "string", true, true, true),
		}
		return enrichCapabilitiesWithStateProfile("washer", base, mapped, profile)
	default:
		base := []map[string]any{capability("switch", "Power", "actuator", "power", "string", true, true, true)}
		return enrichCapabilitiesWithStateProfile(normalizeDeviceType(d.Type), base, mapped, profile)
	}
}

func enrichCapabilitiesWithStateProfile(deviceType string, base []map[string]any, mappedState map[string]any, profile map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(base)+16)
	seen := map[string]bool{}
	appendCap := func(c map[string]any) {
		id := strings.TrimSpace(asString(c["id"]))
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, c)
	}
	for _, c := range base {
		appendCap(c)
	}
	for key, val := range mappedState {
		if key == "online" {
			continue
		}
		id := "thinq." + deviceType + "." + key
		valueType := "string"
		switch val.(type) {
		case bool:
			valueType = "boolean"
		case int, int64, float64, json.Number:
			valueType = "number"
		}
		appendCap(capability(id, strings.ReplaceAll(strings.Title(strings.ReplaceAll(key, "_", " ")), "  ", " "), "sensor", key, valueType, true, false, true))
	}
	for key := range profile {
		id := "thinq." + deviceType + ".profile." + key
		appendCap(capability(id, "Profile "+key, "meta", key, "object", true, false, false))
	}
	return out
}

func washerOperationProfile(d thinqDevice) (string, map[string]bool, string) {
	ctrlKey := ""
	available := map[string]bool{}
	locationName := ""
	state := cloneAnyMap(d.State)
	if response, ok := state["response"].([]any); ok && len(response) > 0 {
		if first, ok := response[0].(map[string]any); ok {
			if loc, ok := first["location"].(map[string]any); ok {
				locationName = strings.TrimSpace(asString(loc["locationName"]))
			}
		}
	}
	if locationName == "" {
		if loc, ok := state["location"].(map[string]any); ok {
			locationName = strings.TrimSpace(asString(loc["locationName"]))
		}
	}
	profile, _ := state["profile"].(map[string]any)
	props, _ := profile["property"].([]any)
	for _, entry := range props {
		propMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		operation, ok := propMap["operation"].(map[string]any)
		if !ok {
			continue
		}
		for key, raw := range operation {
			if ctrlKey == "" {
				ctrlKey = strings.TrimSpace(key)
			}
			def, _ := raw.(map[string]any)
			values, _ := def["value"].(map[string]any)
			for _, modeValues := range values {
				items, ok := modeValues.([]any)
				if !ok {
					continue
				}
				for _, item := range items {
					name := strings.ToUpper(strings.TrimSpace(asString(item)))
					if name != "" {
						available[name] = true
					}
				}
			}
		}
	}
	if locationName == "" {
		locationName = "MAIN"
	}
	return ctrlKey, available, locationName
}

func washerOperationModes(available map[string]bool) []string {
	modes := make([]string, 0, len(available))
	for key, enabled := range available {
		if enabled {
			modes = append(modes, key)
		}
	}
	sort.Strings(modes)
	return modes
}

func mapThinQInputs(caps []map[string]any, d thinqDevice) []map[string]any {
	hasID := func(id string) bool {
		for _, c := range caps {
			if strings.EqualFold(asString(c["id"]), id) {
				return true
			}
		}
		return false
	}
	inputs := make([]map[string]any, 0)
	if hasID("media.volume") {
		inputs = append(inputs, map[string]any{
			"id":            "set_volume",
			"label":         "Set Volume",
			"type":          "range",
			"capability_id": "media.volume",
			"property":      "volume",
			"range":         map[string]any{"min": 0, "max": 100, "step": 1},
		})
	}
	if hasID("media.input") {
		inputs = append(inputs, map[string]any{
			"id":            "set_input",
			"label":         "Set Input",
			"type":          "select",
			"capability_id": "media.input",
			"property":      "input",
			"options": []map[string]any{
				{"value": "hdmi1", "label": "HDMI 1"},
				{"value": "hdmi2", "label": "HDMI 2"},
				{"value": "tv", "label": "TV"},
			},
		})
	}
	if hasID("appliance.washer.control") {
		_, available, _ := washerOperationProfile(d)
		modes := washerOperationModes(available)
		if available["START"] {
			inputs = append(inputs, map[string]any{"id": "start", "label": "Start", "type": "button", "capability_id": "appliance.washer.control", "property": "start"})
		}
		if available["STOP"] {
			inputs = append(inputs, map[string]any{"id": "stop", "label": "Stop", "type": "button", "capability_id": "appliance.washer.control", "property": "stop"})
		}
		if len(modes) > 0 {
			options := make([]map[string]any, 0, len(modes))
			for _, mode := range modes {
				options = append(options, map[string]any{"value": mode, "label": strings.ReplaceAll(strings.Title(strings.ToLower(mode)), "_", " ")})
			}
			inputs = append(inputs, map[string]any{"id": "set_operation_mode", "label": "Set Operation", "type": "select", "capability_id": "appliance.washer.control", "property": "operation_mode", "options": options})
		}
	}
	if hasID("switch") {
		inputs = append(inputs, map[string]any{"id": "set_power", "label": "Set Power", "type": "select", "capability_id": "switch", "property": "power", "options": []map[string]any{{"value": "on", "label": "On"}, {"value": "off", "label": "Off"}}})
	}
	return inputs
}

func mapThinQToHDPMetadata(d thinqDevice) map[string]any {
	icon := "plug"
	typeKey := normalizeDeviceType(d.Type)

	// Use icon keys that the core HomeNavi UI already supports.
	switch {
	case strings.Contains(typeKey, "washer") || strings.Contains(typeKey, "wash"):
		icon = "water"
	case strings.Contains(typeKey, "dryer"):
		icon = "fan"
	case strings.Contains(typeKey, "air") && strings.Contains(typeKey, "condition"):
		icon = "thermostat"
	case typeKey == "ac":
		icon = "thermostat"
	case strings.Contains(typeKey, "fan") || strings.Contains(typeKey, "purifier") || strings.Contains(typeKey, "dehumid"):
		icon = "fan"
	case strings.Contains(typeKey, "speaker") || strings.Contains(typeKey, "sound") || strings.Contains(typeKey, "audio"):
		icon = "audio"
	case strings.Contains(typeKey, "tv"):
		icon = "camera"
	case strings.Contains(typeKey, "dishwasher"):
		icon = "water"
	case strings.Contains(typeKey, "fridge") || strings.Contains(typeKey, "refrigerator"):
		icon = "sensor"
	}
	return map[string]any{
		"type":         "metadata",
		"device_id":    sanitizeDeviceID(d.ID),
		"name":         firstNonEmpty(strings.TrimSpace(d.Name), sanitizeDeviceID(d.ID)),
		"protocol":     "lgthinq",
		"manufacturer": firstNonEmpty(strings.TrimSpace(d.Manufacturer), "LG"),
		"model":        strings.TrimSpace(d.Model),
		"icon":         icon,
		"online":       d.Online,
		"capabilities": mapThinQCapabilities(d),
		"inputs":       mapThinQInputs(mapThinQCapabilities(d), d),
	}
}

func mapThinQToHDPState(d thinqDevice) map[string]any {
	state := cloneAnyMap(d.State)
	out := map[string]any{"online": d.Online}
	switch normalizeDeviceType(d.Type) {
	case "tv":
		out["power"] = normalizePower(state["power"])
		out["volume"] = clamp(asInt(state["volume"], 12), 0, 100)
		out["muted"] = asBool(state["muted"])
		out["input"] = firstNonEmpty(asString(state["input"]), "hdmi1")
		out["playback"] = firstNonEmpty(asString(state["playback"]), "stopped")
	case "washer":
		src := state
		if response, ok := state["response"].([]any); ok && len(response) > 0 {
			if first, ok := response[0].(map[string]any); ok {
				src = first
			}
		}
		runState := firstNonEmpty(strings.ToLower(asString(src["run_state"])), strings.ToLower(asString(state["run_state"])), "idle")
		operationMode := ""
		if rs, ok := src["runState"].(map[string]any); ok {
			operationMode = strings.ToUpper(strings.TrimSpace(asString(rs["currentState"])))
			runState = firstNonEmpty(normalizeWasherRunState(operationMode), runState)
		}
		out["run_state"] = runState
		if operationMode == "" {
			switch {
			case strings.Contains(runState, "off"):
				operationMode = "POWER_OFF"
			case runState == "running":
				operationMode = "START"
			case runState == "idle":
				operationMode = "STOP"
			}
		}
		if operationMode != "" {
			out["operation_mode"] = operationMode
		}
		if cycleObj, ok := src["cycle"].(map[string]any); ok {
			out["cycle"] = firstNonEmpty(asString(cycleObj["cycleCount"]), "cotton")
		} else {
			out["cycle"] = firstNonEmpty(asString(src["cycle"]), asString(state["cycle"]), "cotton")
		}
		if timer, ok := src["timer"].(map[string]any); ok {
			out["remaining_min"] = clamp(asInt(timer["remainMinute"], 0)+(60*clamp(asInt(timer["remainHour"], 0), 0, 999)), 0, 999)
		} else {
			remaining := asInt(src["remaining_min"], -1)
			if remaining < 0 {
				remaining = asInt(state["remaining_min"], 0)
			}
			out["remaining_min"] = clamp(remaining, 0, 999)
		}
		// Prefer an explicit door lock field when present; fall back to remoteControlEnabled when that's all we have.
		if raw, ok := src["door_locked"]; ok {
			out["door_locked"] = asBool(raw)
		} else if raw, ok := src["doorLocked"]; ok {
			out["door_locked"] = asBool(raw)
		} else if raw, ok := src["doorLock"]; ok {
			out["door_locked"] = asBool(raw)
		} else if rc, ok := src["remoteControlEnable"].(map[string]any); ok {
			out["door_locked"] = asBool(rc["remoteControlEnabled"])
		} else {
			if _, ok := src["door_locked"]; ok {
				out["door_locked"] = asBool(src["door_locked"])
			} else {
				out["door_locked"] = asBool(state["door_locked"])
			}
		}
		out["error_code"] = firstNonEmpty(asString(src["error_code"]), asString(state["error_code"]))
		if strings.Contains(runState, "off") {
			out["power"] = "off"
		} else {
			out["power"] = "on"
		}
		if loc, ok := src["location"].(map[string]any); ok {
			out["location"] = asString(loc["locationName"])
		}
		if rc, ok := src["remoteControlEnable"].(map[string]any); ok {
			out["remote_control_enabled"] = asBool(rc["remoteControlEnabled"])
		}
		if ts := asString(state["timestamp"]); ts != "" {
			out["last_seen"] = ts
		}
	default:
		out["power"] = normalizePower(state["power"])
	}
	return out
}

func normalizeWasherRunState(operationMode string) string {
	switch strings.ToUpper(strings.TrimSpace(operationMode)) {
	case "START", "RUNNING", "RUN":
		return "running"
	case "STOP", "IDLE", "END", "COMPLETE", "COMPLETED":
		return "idle"
	case "POWER_OFF", "OFF":
		return "power_off"
	case "POWER_ON", "ON":
		return "idle"
	default:
		return strings.ToLower(strings.TrimSpace(operationMode))
	}
}

func parseThinQAPIError(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(b))
	logDebugf("thinq api error op=%s status=%d body=%q", op, resp.StatusCode, body)
	message := ""
	code := ""
	if body != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(body), &payload); err == nil {
			if errorMap, ok := payload["error"].(map[string]any); ok {
				message = strings.TrimSpace(asString(errorMap["message"]))
				code = strings.TrimSpace(asString(errorMap["code"]))
			} else if rawErr := strings.TrimSpace(asString(payload["error"])); rawErr != "" {
				message = rawErr
			}
			if message == "" {
				message = strings.TrimSpace(asString(payload["message"]))
			}
			if code == "" {
				code = strings.TrimSpace(asString(payload["code"]))
			}
		}
	}
	if message == "" {
		if body != "" {
			message = body
		} else {
			message = http.StatusText(resp.StatusCode)
		}
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && message == "" {
		message = "PAT token missing permission for this feature or region/country mismatch"
	}
	lowerMsg := strings.ToLower(message)
	if strings.Contains(lowerMsg, "scope") || strings.Contains(lowerMsg, "permission") {
		message = "PAT token does not include required permission for this feature"
	}
	return &thinQAPIError{
		Op:      op,
		Status:  resp.StatusCode,
		Code:    code,
		Message: message,
		RawBody: body,
	}
}

type thinQAPIError struct {
	Op      string
	Status  int
	Code    string
	Message string
	RawBody string
}

func (e *thinQAPIError) Error() string {
	if e == nil {
		return "ThinQ API error"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if strings.TrimSpace(e.Code) != "" {
		return fmt.Sprintf("%s failed (%d/%s): %s", e.Op, e.Status, e.Code, msg)
	}
	return fmt.Sprintf("%s failed (%d): %s", e.Op, e.Status, msg)
}

func translateHDPCommand(d thinqDevice, command string, args map[string]any) (thinqCommand, error) {
	kind := normalizeDeviceType(d.Type)
	cmdName := strings.TrimSpace(strings.ToLower(command))
	if args == nil {
		args = map[string]any{}
	}
	switch kind {
	case "tv":
		switch {
		case cmdName == "power" || hasKey(args, "power"):
			return thinqCommand{Name: "set_power", Params: map[string]any{"power": normalizePower(args["power"])}}, nil
		case cmdName == "set_volume" || hasKey(args, "volume"):
			return thinqCommand{Name: "set_volume", Params: map[string]any{"volume": clamp(asInt(args["volume"], 12), 0, 100)}}, nil
		case cmdName == "set_mute" || hasKey(args, "muted"):
			return thinqCommand{Name: "set_mute", Params: map[string]any{"muted": asBool(args["muted"])}}, nil
		case cmdName == "set_input" || hasKey(args, "input"):
			return thinqCommand{Name: "set_input", Params: map[string]any{"input": firstNonEmpty(asString(args["input"]), "hdmi1")}}, nil
		default:
			return thinqCommand{}, fmt.Errorf("unsupported tv command")
		}
	case "washer":
		ctrlKey, available, locationName := washerOperationProfile(d)
		ctrlKey = firstNonEmpty(strings.TrimSpace(ctrlKey), "washerOperationMode")
		mkPayload := func(mode string) map[string]any {
			return map[string]any{
				"location":  map[string]any{"locationName": locationName},
				"operation": map[string]any{"washerOperationMode": mode},
			}
		}
		mkCommand := func(name, mode string) thinqCommand {
			return thinqCommand{
				Name:    name,
				CtrlKey: ctrlKey,
				Command: mode,
				Params:  mkPayload(mode),
			}
		}
		switch {
		case cmdName == "start" || asBool(args["start"]):
			if len(available) > 0 && !available["START"] {
				return thinqCommand{}, fmt.Errorf("unsupported washer command")
			}
			return mkCommand("start", "START"), nil
		case cmdName == "stop" || asBool(args["stop"]):
			if len(available) > 0 && !available["STOP"] {
				return thinqCommand{}, fmt.Errorf("unsupported washer command")
			}
			return mkCommand("stop", "STOP"), nil
		case cmdName == "set_power" || cmdName == "power" || hasKey(args, "power"):
			target := normalizePower(args["power"])
			if target == "on" {
				if len(available) > 0 && !available["POWER_ON"] {
					return thinqCommand{}, fmt.Errorf("unsupported washer command")
				}
				return mkCommand("set_power", "POWER_ON"), nil
			}
			if len(available) > 0 && !available["POWER_OFF"] {
				return thinqCommand{}, fmt.Errorf("unsupported washer command")
			}
			return mkCommand("set_power", "POWER_OFF"), nil
		case cmdName == "set_operation_mode" || hasKey(args, "operation_mode"):
			mode := strings.ToUpper(strings.TrimSpace(asString(args["operation_mode"])))
			if mode == "" {
				return thinqCommand{}, fmt.Errorf("missing operation_mode")
			}
			if len(available) > 0 && !available[mode] {
				return thinqCommand{}, fmt.Errorf("unsupported washer command")
			}
			return mkCommand("set_operation_mode", mode), nil
		default:
			return thinqCommand{}, fmt.Errorf("unsupported washer command")
		}
	default:
		return thinqCommand{}, fmt.Errorf("unsupported device type")
	}
}

func sanitizeDeviceID(id string) string {
	clean := strings.TrimSpace(id)
	if clean == "" {
		return "lgthinq/unknown"
	}
	clean = strings.ReplaceAll(clean, " ", "-")
	clean = strings.ReplaceAll(clean, "::", "/")
	if !strings.HasPrefix(clean, "lgthinq/") {
		clean = "lgthinq/" + strings.TrimPrefix(clean, "/")
	}
	return clean
}

func normalizeDeviceType(v string) string {
	raw := strings.ToLower(strings.TrimSpace(v))
	raw = strings.ReplaceAll(raw, "-", "_")
	raw = strings.ReplaceAll(raw, ".", "_")
	raw = strings.TrimPrefix(raw, "device_")
	if strings.Contains(raw, "washer") {
		return "washer"
	}
	if strings.Contains(raw, "dryer") {
		return "dryer"
	}
	if strings.Contains(raw, "tv") {
		return "tv"
	}
	switch raw {
	case "201", "tv", "smarttv", "media_tv", "media.tv":
		return "tv"
	case "202", "washer", "washingmachine", "wm", "washing_machine", "laundry_washer":
		return "washer"
	default:
		return raw
	}
}

func normalizePower(v any) string {
	raw := strings.ToLower(strings.TrimSpace(asString(v)))
	if raw == "" || raw == "0" || raw == "false" || raw == "standby" {
		return "off"
	}
	if raw == "1" || raw == "true" || raw == "on" {
		return "on"
	}
	return raw
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []byte:
		return strings.TrimSpace(string(t))
	case json.Number:
		return t.String()
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func asInt(v any, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		n, err := t.Int64()
		if err == nil {
			return int(n)
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return def
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		raw := strings.ToLower(strings.TrimSpace(t))
		return raw == "true" || raw == "1" || raw == "yes" || raw == "on"
	case int:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}

func clamp(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
