package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func testWasherDevice(remoteEnabled bool) thinqDevice {
	return thinqDevice{
		ID:   "washer-1",
		Type: "washer",
		State: map[string]any{
			"response": []any{map[string]any{
				"location":            map[string]any{"locationName": "MAIN"},
				"remoteControlEnable": map[string]any{"remoteControlEnabled": remoteEnabled},
			}},
			"profile": map[string]any{
				"property": []any{map[string]any{
					"location": map[string]any{"locationName": "MAIN"},
					"operation": map[string]any{
						"washerOperationMode": map[string]any{"type": "enum", "mode": []any{"w"}, "value": map[string]any{"w": []any{"START", "STOP", "POWER_ON", "POWER_OFF"}}},
					},
				}},
			},
		},
	}
}

func TestWasherCommandsRequireMatchingWritableProfileAndRemoteState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*thinqDevice)
	}{
		{"missing profile", func(device *thinqDevice) { delete(device.State, "profile") }},
		{"unknown remote state", func(device *thinqDevice) {
			device.State["response"] = []any{map[string]any{"location": map[string]any{"locationName": "MAIN"}}}
		}},
		{"wrong location", func(device *thinqDevice) {
			device.State["response"] = []any{map[string]any{"location": map[string]any{"locationName": "MINI"}, "remoteControlEnable": map[string]any{"remoteControlEnabled": true}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			device := testWasherDevice(true)
			test.mutate(&device)
			if _, err := translateHDPCommand(device, "start", nil); err == nil {
				t.Fatal("expected unsafe command to be rejected")
			}
		})
	}
}

func TestWasherHDPModeWithRemoteDisabled(t *testing.T) {
	device := testWasherDevice(false)
	for _, mode := range []string{"STOP", "POWER_OFF", "POWER_ON"} {
		command, err := translateHDPCommand(device, "set_state", map[string]any{"operation_mode": mode})
		if err != nil || command.Params["operation"].(map[string]any)["washerOperationMode"] != mode {
			t.Errorf("mode %s: command %#v, error %v", mode, command, err)
		}
	}
	if _, err := translateHDPCommand(device, "set_state", map[string]any{"operation_mode": "START"}); err == nil {
		t.Fatal("accepted START while remote control is disabled")
	}
}

func TestCombinedLaundryTypesDoNotUseSingleWasherOrDryerCommands(t *testing.T) {
	for _, deviceType := range []string{"DEVICE_WASHTOWER_WASHER", "DEVICE_WASHTOWER_DRYER", "DEVICE_MAIN_WASHCOMBO", "DEVICE_MINI_WASHCOMBO"} {
		t.Run(deviceType, func(t *testing.T) {
			device := testWasherDevice(true)
			device.Type = deviceType
			if kind := normalizeDeviceType(deviceType); kind == "washer" || kind == "dryer" {
				t.Fatalf("combined device normalized to %s", kind)
			}
			if _, err := translateHDPCommand(device, "start", nil); err == nil {
				t.Fatal("combined device accepted a single-appliance command")
			}
		})
	}
}

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

func TestGenericApplianceStateAndCapabilities(t *testing.T) {
	cases := []struct {
		deviceType string
		state      map[string]any
		property   string
		want       any
	}{
		{"DEVICE_AIR_CONDITIONER", map[string]any{"temperature": map[string]any{"currentTemperature": 24.5}}, "temperature.currentTemperature", 24.5},
		{"DEVICE_WATER_PURIFIER", map[string]any{"runState": map[string]any{"cockState": "NORMAL"}}, "runState.cockState", "NORMAL"},
		{"DEVICE_OVEN", map[string]any{"response": []any{map[string]any{"location": map[string]any{"locationName": "LOWER"}, "temperature": map[string]any{"currentTemperature": 180.0}}}}, "response.0.temperature.currentTemperature", 180.0},
		{"DEVICE_REFRIGERATOR", map[string]any{"temperature": []any{map[string]any{"locationName": "FRIDGE", "currentTemperature": 4.0}}}, "temperature.0.currentTemperature", 4.0},
		{"DEVICE_DISH_WASHER", map[string]any{"runState": map[string]any{"currentState": "PAUSE"}}, "runState.currentState", "PAUSE"},
	}
	for _, fixture := range cases {
		t.Run(fixture.deviceType, func(t *testing.T) {
			device := thinqDevice{ID: fixture.deviceType, Type: fixture.deviceType, Online: true, State: fixture.state}
			mapped := mapThinQToHDPState(device)
			if mapped[fixture.property] != fixture.want {
				t.Fatalf("expected %s = %#v, got %#v", fixture.property, fixture.want, mapped)
			}
			for _, capability := range mapThinQCapabilities(device) {
				access, _ := capability["access"].(map[string]any)
				if access["write"] == true {
					t.Fatalf("unexpected writable capability without a profile: %#v", capability)
				}
			}
		})
	}
}

func TestDocumentedDeviceStateExamplesAreExposed(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	for name, raw := range examples {
		if !strings.HasSuffix(name, "-object-example") {
			continue
		}
		kind := strings.TrimSuffix(name, "-object-example")
		if kind == "washer" {
			continue
		}
		t.Run(kind, func(t *testing.T) {
			entry, _ := raw.(map[string]any)
			state, ok := entry["value"].(map[string]any)
			if !ok {
				array, arrayOK := entry["value"].([]any)
				if !arrayOK {
					t.Fatal("fixture is not an object or array")
				}
				state = map[string]any{"response": array}
			}
			mapped := mapThinQToHDPState(thinqDevice{Type: kind, State: state})
			var check func(string, any)
			check = func(path string, value any) {
				switch typed := value.(type) {
				case map[string]any:
					for key, child := range typed {
						next := key
						if path != "" {
							next = path + "." + key
						}
						check(next, child)
					}
				case []any:
					for index, child := range typed {
						check(path+"."+strconv.Itoa(index), child)
					}
				case string, bool, float64:
					if mapped[path] != typed {
						t.Errorf("%s: expected %#v, got %#v", path, typed, mapped[path])
					}
				}
			}
			check("", state)
		})
	}
}

func TestAirPurifierProfileControl(t *testing.T) {
	device := thinqDevice{ID: "purifier", Type: "DEVICE_AIR_PURIFIER", Online: true, State: map[string]any{
		"operation": map[string]any{"airPurifierOperationMode": "POWER_OFF"},
		"profile": map[string]any{"property": map[string]any{"operation": map[string]any{
			"airPurifierOperationMode": map[string]any{"type": "enum", "mode": []any{"r", "w"}, "value": map[string]any{"r": []any{"POWER_ON", "POWER_OFF"}, "w": []any{"POWER_ON", "POWER_OFF"}}},
		}}},
	}}
	inputs := mapThinQInputs(mapThinQCapabilities(device), device)
	found := false
	for _, input := range inputs {
		if input["property"] == "operation.airPurifierOperationMode" && input["id"] == "set_thinq_property" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected profile-based purifier operation input, got %#v", inputs)
	}
	command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{"operation.airPurifierOperationMode": "POWER_ON"})
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := command.Params["operation"].(map[string]any)
	if !ok || operation["airPurifierOperationMode"] != "POWER_ON" {
		t.Fatalf("unexpected control payload: %#v", command.Params)
	}
	hdpCommand, err := translateHDPCommand(device, "set_state", map[string]any{"operation.airPurifierOperationMode": "POWER_ON"})
	if err != nil || !reflect.DeepEqual(hdpCommand.Params, command.Params) {
		t.Fatalf("HDP command payload = %#v, error = %v", hdpCommand.Params, err)
	}
	if _, err := translateHDPCommand(device, "set_state", map[string]any{"operation.airPurifierOperationMode": "INVALID"}); err == nil {
		t.Fatal("expected invalid HDP enum to be rejected")
	}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{"operation.airPurifierOperationMode": "INVALID"}); err == nil {
		t.Fatal("expected invalid enum to be rejected")
	}
	delete(device.State, "profile")
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{"operation.airPurifierOperationMode": "POWER_ON"}); err == nil {
		t.Fatal("expected command without a profile to be rejected")
	}
}

func TestDocumentedApplianceOperationProfiles(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	examples := components["examples"].(map[string]any)
	cases := []struct{ deviceType, example, group, field string }{
		{"DEVICE_AIR_CONDITIONER", "air_conditioner", "operation", "airConOperationMode"},
		{"DEVICE_DRYER", "dryer", "operation", "dryerOperationMode"},
		{"DEVICE_STYLER", "styler", "operation", "stylerOperationMode"},
		{"DEVICE_SYSTEM_BOILER", "system_boiler", "operation", "boilerOperationMode"},
		{"DEVICE_DEHUMIDIFIER", "dehumidifier", "operation", "dehumidifierOperationMode"},
		{"DEVICE_HUMIDIFIER", "humidifier", "operation", "humidifierOperationMode"},
		{"DEVICE_CEILING_FAN", "ceiling_fan", "operation", "ceilingfanOperationMode"},
		{"DEVICE_AIR_PURIFIER_FAN", "air_purifier_fan", "operation", "airFanOperationMode"},
		{"DEVICE_ROBOT_CLEANER", "robot_cleaner", "operation", "cleanOperationMode"},
		{"DEVICE_WATER_HEATER", "water_heater", "waterHeaterJobMode", "currentJobMode"},
	}
	for _, fixture := range cases {
		t.Run(fixture.deviceType, func(t *testing.T) {
			profile := examples[fixture.example+"-profile-example"].(map[string]any)["value"].(map[string]any)
			properties := profile["property"].(map[string]any)
			definition := properties[fixture.group].(map[string]any)[fixture.field].(map[string]any)
			values := definition["value"].(map[string]any)["w"].([]any)
			device := thinqDevice{ID: fixture.deviceType, Type: fixture.deviceType, Online: true, State: map[string]any{"profile": profile, "remoteControlEnable": map[string]any{"remoteControlEnabled": true}}}
			path := fixture.group + "." + fixture.field
			command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{path: values[0]})
			if err != nil {
				t.Fatalf("documented control %s rejected: %v", path, err)
			}
			if command.Params[fixture.group].(map[string]any)[fixture.field] != values[0] {
				t.Fatalf("unexpected payload %#v", command.Params)
			}
		})
	}
}

func TestRefrigeratorTemperatureControlUsesLocationAndProfileRange(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	profile := document["components"].(map[string]any)["examples"].(map[string]any)["refrigerator-profile-example"].(map[string]any)["value"].(map[string]any)
	device := thinqDevice{ID: "fridge", Type: "DEVICE_REFRIGERATOR", Online: true, State: map[string]any{
		"profile":     profile,
		"temperature": []any{map[string]any{"locationName": "FRIDGE", "unit": "C", "targetTemperature": 4.0}},
	}}
	property := "temperature.FRIDGE.C.targetTemperature"
	if got := mapThinQToHDPState(device)[property]; got != 4.0 {
		t.Fatalf("expected location-specific state, got %#v", got)
	}
	command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: 5.0})
	if err != nil {
		t.Fatal(err)
	}
	temperature, ok := command.Params["temperature"].(map[string]any)
	if !ok || temperature["locationName"] != "FRIDGE" || temperature["unit"] != "C" || temperature["targetTemperature"] != 5.0 {
		t.Fatalf("unexpected payload %#v", command.Params)
	}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: 100.0}); err == nil {
		t.Fatal("expected out-of-range value to be rejected")
	}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "5"}); err == nil {
		t.Fatal("expected nonnumeric JSON value to be rejected")
	}
	properties := profile["property"].(map[string]any)
	first := properties["temperature"].([]any)[0].(map[string]any)
	target := first["targetTemperature"].(map[string]any)
	limits := target["value"].(map[string]any)["w"].(map[string]any)
	limits["except"] = []any{5.0}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: 5.0}); err == nil {
		t.Fatal("expected excluded temperature to be rejected")
	}
	limits["except"] = []any{"5"}
	for _, input := range applianceProfileInputs(device) {
		if input["property"] == property && !reflect.DeepEqual(input["range"].(map[string]any)["except"], []any{5.0}) {
			t.Fatalf("expected numeric exclusions, got %#v", input["range"])
		}
	}
	limits["except"] = "malformed"
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: 4.0}); err == nil {
		t.Fatal("expected malformed profile restrictions to be rejected")
	}
}

func TestWineCellarTemperatureUsesDocumentedCompartments(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	state := examples["wine_cellar-object-example"].(map[string]any)["value"].(map[string]any)
	state["profile"] = examples["wine_cellar-profile-example"].(map[string]any)["value"]
	device := thinqDevice{Type: "DEVICE_WINE_CELLAR", State: state}
	property := "temperature.WINE_LOWER.C.targetTemperature"
	if mapThinQToHDPState(device)[property] != 18.0 || len(applianceProfileInputs(device)) != 2 {
		t.Fatal("wine cellar compartment states or inputs missing")
	}
	for temperature := 5; temperature <= 18; temperature++ {
		command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: temperature})
		if err != nil {
			t.Fatalf("temperature %d: %v", temperature, err)
		}
		payload, _ := command.Params["temperature"].(map[string]any)
		if payload["locationName"] != "WINE_LOWER" || payload["unit"] != "C" || payload["targetTemperature"] != float64(temperature) {
			t.Fatalf("unexpected payload %#v", command.Params)
		}
	}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: 19}); err == nil {
		t.Fatal("accepted out-of-range temperature")
	}
}

func TestPairedHoodAndMicrowaveControls(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	for _, kind := range []string{"hood", "microwave_oven"} {
		t.Run(kind, func(t *testing.T) {
			profile := examples[kind+"-profile-example"].(map[string]any)["value"].(map[string]any)
			state := examples[kind+"-object-example"].(map[string]any)["value"].(map[string]any)
			state["profile"] = profile
			device := thinqDevice{Type: kind, State: state}
			inputs := applianceProfileInputs(device)
			if len(inputs) != 2 {
				t.Fatalf("expected two controls, got %#v", inputs)
			}
			args := map[string]any{"lamp.lampBrightness": float64(1), "ventilation.fanSpeed": float64(0)}
			if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{"lamp.lampBrightness": 1.0}); err == nil {
				t.Fatal("accepted a paired control as a single property")
			}
			command, err := translateHDPCommand(device, "set_thinq_pair", args)
			if err != nil {
				t.Fatal(err)
			}
			lamp, _ := command.Params["lamp"].(map[string]any)
			ventilation, _ := command.Params["ventilation"].(map[string]any)
			if lamp["lampBrightness"] != float64(1) || ventilation["fanSpeed"] != float64(0) {
				t.Fatalf("unexpected payload %#v", command.Params)
			}
			hdpCommand, err := translateHDPCommand(device, "set_state", map[string]any{"lamp.lampBrightness": float64(1)})
			if err != nil {
				t.Fatalf("HDP paired command failed: %v", err)
			}
			currentFan := state["ventilation"].(map[string]any)["fanSpeed"]
			if hdpCommand.Params["ventilation"].(map[string]any)["fanSpeed"] != currentFan {
				t.Fatalf("HDP command did not preserve the paired value: %#v", hdpCommand.Params)
			}
			metadataInputs := mapThinQToHDPMetadata(device)["inputs"].([]map[string]any)
			if len(metadataInputs) != 2 || metadataInputs[0]["id"] == metadataInputs[1]["id"] {
				t.Fatalf("paired HDP input IDs are not unique: %#v", metadataInputs)
			}
			fanMax := 5
			if kind == "microwave_oven" {
				fanMax = 4
			}
			for brightness := 0; brightness <= 2; brightness++ {
				for speed := 0; speed <= fanMax; speed++ {
					values := map[string]any{"lamp.lampBrightness": brightness, "ventilation.fanSpeed": speed}
					if _, err := translateHDPCommand(device, "set_thinq_pair", values); err != nil {
						t.Errorf("valid pair %#v rejected: %v", values, err)
					}
				}
			}
			for _, invalid := range []map[string]any{
				{"lamp.lampBrightness": 1.0},
				{"lamp.lampBrightness": 100.0, "ventilation.fanSpeed": 0.0},
				{"lamp.lampBrightness": 1.5, "ventilation.fanSpeed": 0.0},
				{"lamp.lampBrightness": 1.0, "ventilation.fanSpeed": "0"},
				{"lamp.lampBrightness": 1.0, "ventilation.fanSpeed": 0.0, "extra": 1.0},
			} {
				if _, err := translateHDPCommand(device, "set_thinq_pair", invalid); err == nil {
					t.Errorf("accepted invalid values %#v", invalid)
				}
			}
			properties := profile["property"].(map[string]any)
			lampDefinition := properties["lamp"].(map[string]any)["lampBrightness"].(map[string]any)
			limits := lampDefinition["value"].(map[string]any)["w"].(map[string]any)
			limits["except"] = []any{1.0}
			if _, err := translateHDPCommand(device, "set_thinq_pair", args); err == nil {
				t.Error("accepted excluded brightness")
			}
			lampDefinition["mode"] = []any{"r"}
			if len(applianceProfileInputs(device)) != 0 {
				t.Error("exposed a pair with a read-only property")
			}
		})
	}
}

func TestCombinedLaundryProfileOperations(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	for _, kind := range []string{"washtower", "washtower_washer", "washtower_dryer", "main_washcombo", "mini_washcombo"} {
		t.Run(kind, func(t *testing.T) {
			profile := examples[kind+"-profile-example"].(map[string]any)["value"].(map[string]any)
			fixture := examples[kind+"-object-example"].(map[string]any)["value"]
			state, ok := fixture.(map[string]any)
			if !ok {
				state = map[string]any{"response": fixture}
			}
			state["profile"] = profile
			device := thinqDevice{Type: kind, State: state}
			inputs := applianceProfileInputs(device)
			expected := 1
			if kind == "washtower" {
				expected = 2
			}
			if len(inputs) != expected {
				t.Fatalf("expected %d operations, got %#v", expected, inputs)
			}
			for _, input := range inputs {
				property := asString(input["property"])
				for _, value := range []string{"STOP", "POWER_OFF"} {
					if _, err := translateHDPCommand(device, "set_thinq_laundry", map[string]any{property: value}); err != nil {
						t.Errorf("%s=%s: %v", property, value, err)
					}
					if _, err := translateHDPCommand(device, "set_state", map[string]any{property: value}); err != nil {
						t.Errorf("HDP %s=%s: %v", property, value, err)
					}
				}
				if _, err := translateHDPCommand(device, "set_thinq_laundry", map[string]any{property: "START"}); err == nil {
					t.Errorf("%s accepted START while remote disabled", property)
				}
				if _, err := translateHDPCommand(device, "set_thinq_laundry", map[string]any{property: "INVALID"}); err == nil {
					t.Errorf("%s accepted an invalid operation", property)
				}
			}
			if kind == "washtower" {
				for _, component := range []string{"washer", "dryer"} {
					state[component].(map[string]any)["remoteControlEnable"].(map[string]any)["remoteControlEnabled"] = true
				}
			} else if kind == "washtower_dryer" {
				state["remoteControlEnable"].(map[string]any)["remoteControlEnabled"] = true
			} else {
				state["response"].([]any)[0].(map[string]any)["remoteControlEnable"].(map[string]any)["remoteControlEnabled"] = true
			}
			for _, input := range inputs {
				property := asString(input["property"])
				command, err := translateHDPCommand(device, "set_thinq_laundry", map[string]any{property: "START"})
				if err != nil {
					t.Fatalf("%s did not accept remote-enabled START: %v", property, err)
				}
				payload := command.Params
				if component := asString(input["component"]); component != "" {
					payload, _ = payload[component].(map[string]any)
				}
				operation, _ := payload["operation"].(map[string]any)
				if operation[asString(input["operation_field"])] != "START" {
					t.Errorf("unexpected operation payload %#v", command.Params)
				}
				if location := asString(input["location"]); location != "" {
					locationPayload, _ := payload["location"].(map[string]any)
					if locationPayload["locationName"] != location {
						t.Errorf("unexpected location payload %#v", command.Params)
					}
				}
			}
			if kind == "main_washcombo" || kind == "mini_washcombo" {
				device.Type = "DEVICE_WASHCOMBO_" + strings.ToUpper(strings.TrimSuffix(kind, "_washcombo"))
				aliasInputs := applianceProfileInputs(device)
				if len(aliasInputs) != len(inputs) {
					t.Fatalf("official type %s lost controls: %#v", device.Type, aliasInputs)
				}
				property := asString(aliasInputs[0]["property"])
				if _, err := translateHDPCommand(device, "set_thinq_laundry", map[string]any{property: "START"}); err != nil {
					t.Fatalf("official type %s command failed: %v", device.Type, err)
				}
			}
		})
	}
	if _, err := translateHDPCommand(testWasherDevice(true), "set_thinq_laundry", map[string]any{"location.MAIN.operation.washerOperationMode": "START"}); err == nil {
		t.Error("combined command accepted for a standalone washer")
	}
}

func TestCombinedLaundryMapsLocationSpecificOperation(t *testing.T) {
	device := thinqDevice{Type: "mini_washcombo", State: map[string]any{"response": []any{
		map[string]any{"location": map[string]any{"locationName": "MINI"}, "operation": map[string]any{"washerOperationMode": "STOP"}},
	}}}
	mapped := mapThinQToHDPState(device)
	if mapped["location.MINI.operation.washerOperationMode"] != "STOP" {
		t.Fatalf("location-specific operation not mapped: %#v", mapped)
	}
}

func TestDishwasherCommandsRequireWritableProfileAndRemoteStart(t *testing.T) {
	device := thinqDevice{Type: "DEVICE_DISH_WASHER", State: map[string]any{
		"profile": map[string]any{"property": map[string]any{"operation": map[string]any{"dishWasherOperationMode": map[string]any{
			"type": "enum", "mode": []any{"w"}, "value": map[string]any{"w": []any{"START", "STOP", "POWER_OFF"}},
		}}}},
		"remoteControlEnable": map[string]any{"remoteControlEnabled": false},
	}}
	property := "operation.dishWasherOperationMode"
	if len(applianceProfileInputs(device)) != 1 {
		t.Fatal("expected profile-backed dishwasher control")
	}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "START"}); err == nil {
		t.Fatal("accepted remote-disabled start")
	}
	device.State["remoteControlEnable"].(map[string]any)["remoteControlEnabled"] = true
	command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "START"})
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := command.Params["operation"].(map[string]any)
	if operation["dishWasherOperationMode"] != "START" {
		t.Fatalf("unexpected command %#v", command.Params)
	}
	for _, kind := range []string{"DEVICE_DISH_WASHER", "DEVICE_DISHWASHER"} {
		device.Type = kind
		for _, value := range []string{"START", "STOP", "POWER_OFF"} {
			if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: value}); err != nil {
				t.Fatalf("%s %s: %v", kind, value, err)
			}
		}
		if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "PAUSE"}); err == nil {
			t.Fatalf("%s accepted unadvertised operation", kind)
		}
	}
	delete(device.State, "remoteControlEnable")
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "START"}); err == nil {
		t.Fatal("accepted start with missing remote status")
	}
	definition := device.State["profile"].(map[string]any)["property"].(map[string]any)["operation"].(map[string]any)["dishWasherOperationMode"].(map[string]any)
	definition["mode"] = []any{"r"}
	if len(applianceProfileInputs(device)) != 0 {
		t.Fatal("advertised read-only dishwasher operation")
	}
}

func TestVentilatorProfileOperations(t *testing.T) {
	device := thinqDevice{Type: "DEVICE_VENTILATOR", State: map[string]any{
		"ventJobMode": map[string]any{"currentJobMode": "VENT_NATURE"},
		"operation":   map[string]any{"ventOperationMode": "POWER_ON"},
		"profile": map[string]any{"property": map[string]any{
			"ventJobMode": map[string]any{"currentJobMode": map[string]any{"type": "enum", "mode": []any{"r", "w"}, "value": map[string]any{"w": []any{"VENT_AUTO", "VENT_NATURE"}}}},
			"operation":   map[string]any{"ventOperationMode": map[string]any{"type": "enum", "mode": []any{"r", "w"}, "value": map[string]any{"w": []any{"POWER_ON", "POWER_OFF"}}}},
		}},
	}}
	if inputs := applianceProfileInputs(device); len(inputs) != 2 {
		t.Fatalf("expected documented ventilation controls, got %#v", inputs)
	}
	for property, values := range map[string][]string{
		"ventJobMode.currentJobMode":  {"VENT_AUTO", "VENT_NATURE"},
		"operation.ventOperationMode": {"POWER_ON", "POWER_OFF"},
	} {
		for _, value := range values {
			command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: value})
			if err != nil {
				t.Fatalf("%s=%s: %v", property, value, err)
			}
			parts := strings.Split(property, ".")
			group, _ := command.Params[parts[0]].(map[string]any)
			if group[parts[1]] != value {
				t.Fatalf("unexpected payload %#v", command.Params)
			}
		}
		if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "INVALID"}); err == nil {
			t.Fatalf("accepted invalid %s", property)
		}
	}
}

func TestRefrigeratorPowerSaveRequiresWritableBooleanProfile(t *testing.T) {
	definition := map[string]any{"type": "boolean", "mode": []any{"r", "w"}, "value": map[string]any{"w": []any{true, false}}}
	device := thinqDevice{Type: "DEVICE_REFRIGERATOR", State: map[string]any{
		"powerSave": map[string]any{"powerSaveEnabled": false},
		"profile":   map[string]any{"property": map[string]any{"powerSave": map[string]any{"powerSaveEnabled": definition}}},
	}}
	property := "powerSave.powerSaveEnabled"
	inputs := applianceProfileInputs(device)
	if len(inputs) != 1 || inputs[0]["type"] != "toggle" || inputs[0]["property"] != property {
		t.Fatalf("expected power-save toggle, got %#v", inputs)
	}
	capabilities := mapThinQCapabilities(device)
	foundBoolean := false
	for _, capability := range capabilities {
		if capability["id"] == "thinq.control."+property && capability["value_type"] == "boolean" {
			foundBoolean = true
		}
	}
	if !foundBoolean {
		t.Fatalf("expected boolean capability, got %#v", capabilities)
	}
	for _, value := range []bool{false, true} {
		command, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: value})
		if err != nil {
			t.Fatalf("power save %t: %v", value, err)
		}
		group, _ := command.Params["powerSave"].(map[string]any)
		if group["powerSaveEnabled"] != value {
			t.Fatalf("unexpected payload %#v", command.Params)
		}
	}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: "true"}); err == nil {
		t.Fatal("accepted a non-boolean value")
	}
	definition["value"].(map[string]any)["w"] = []any{false}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: true}); err == nil {
		t.Fatal("accepted a boolean value absent from the writable profile")
	}
	definition["mode"] = []any{"r"}
	if len(applianceProfileInputs(device)) != 0 {
		t.Fatal("advertised read-only power-save control")
	}
	definition["mode"] = []any{"w"}
	definition["value"].(map[string]any)["w"] = []any{false, true}
	if _, err := translateHDPCommand(device, "set_thinq_property", map[string]any{property: true, "unexpected": false}); err == nil {
		t.Fatal("accepted an extra property")
	}
	delete(device.State, "powerSave")
	if len(applianceProfileInputs(device)) != 0 {
		t.Fatal("advertised power-save control without current state")
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

func TestMapThinQCapabilitiesWasherKeepsPowerWritable(t *testing.T) {
	d := testWasherDevice(true)
	d.State["power"] = "off"

	powerCapabilities := 0
	for _, capability := range mapThinQCapabilities(d) {
		if capability["property"] != "power" {
			continue
		}
		powerCapabilities++
		access, ok := capability["access"].(map[string]any)
		if !ok || access["write"] != true {
			t.Fatalf("expected writable power capability, got %#v", capability)
		}
	}
	if powerCapabilities != 1 {
		t.Fatalf("expected one power capability, got %d", powerCapabilities)
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

func TestBridgeSnapshotPreservesPendingCommandState(t *testing.T) {
	store := newBridgeStore()
	store.replace([]thinqDevice{{
		ID:   "washer-1",
		Type: "washer",
		State: map[string]any{"response": []any{map[string]any{
			"operation": map[string]any{"washerOperationMode": "POWER_OFF"},
		}}},
	}})
	store.setPendingState("washer-1", map[string]any{"power": "on"})

	snapshot := store.allSnapshot()["washer-1"].(map[string]any)
	mapped := snapshot["mapped_state"].(map[string]any)
	if mapped["power"] != "on" {
		t.Fatalf("expected pending command state to overlay stale snapshot, got %#v", mapped)
	}

	store.clearPendingState("washer-1")
	snapshot = store.allSnapshot()["washer-1"].(map[string]any)
	mapped = snapshot["mapped_state"].(map[string]any)
	if mapped["power"] != "off" {
		t.Fatalf("expected confirmed raw state after clearing overlay, got %#v", mapped)
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
	d := testWasherDevice(true)
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
	d := testWasherDevice(true)
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
	d := testWasherDevice(false)
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

func TestTranslateWasherStartBlockedWhenRemoteControlOff(t *testing.T) {
	d := testWasherDevice(false)
	_, err := translateHDPCommand(d, "start", map[string]any{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "remote control is off") {
		t.Fatalf("expected remote control error, got %v", err)
	}
}

func TestMapThinQInputsWasherShowsProgramControlsWhenRemoteControlOff(t *testing.T) {
	d := testWasherDevice(false)
	inputs := mapThinQInputs(mapThinQCapabilities(d), d)
	hasPower := false
	hasStart := false
	hasMode := false
	for _, input := range inputs {
		switch input["id"] {
		case "set_power":
			hasPower = true
		case "start":
			hasStart = true
		case "set_operation_mode":
			hasMode = true
		}
	}
	if !hasPower {
		t.Fatalf("expected set_power input to remain available")
	}
	if !hasStart {
		t.Fatalf("expected start input to remain visible")
	}
	if !hasMode {
		t.Fatalf("expected set_operation_mode input to remain visible")
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
			if got := r.Header.Get("x-conditional-control"); got != "true" {
				t.Errorf("expected conditional control header, got %q", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode control payload: %v", err)
			}
			operation, ok := payload["operation"].(map[string]any)
			if !ok || operation["washerOperationMode"] != "POWER_ON" {
				t.Fatalf("unexpected operation payload: %#v", payload["operation"])
			}
			location, ok := payload["location"].(map[string]any)
			if !ok || location["locationName"] != "MAIN" {
				t.Fatalf("unexpected location payload: %#v", payload["location"])
			}
			if _, ok := payload["ctrlKey"]; ok {
				t.Fatalf("did not expect ctrlKey wrapper in payload: %#v", payload)
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
		Params: map[string]any{
			"location":  map[string]any{"locationName": "MAIN"},
			"operation": map[string]any{"washerOperationMode": "POWER_ON"},
		},
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

func TestCloudProviderSendsDocumentedCombinedControlPayloads(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	for _, kind := range []string{"hood", "microwave_oven", "washtower", "washtower_washer", "washtower_dryer", "main_washcombo", "mini_washcombo"} {
		t.Run(kind, func(t *testing.T) {
			profile := examples[kind+"-profile-example"].(map[string]any)["value"].(map[string]any)
			fixture := examples[kind+"-object-example"].(map[string]any)["value"]
			state, ok := fixture.(map[string]any)
			if !ok {
				state = map[string]any{"response": fixture}
			}
			state["profile"] = profile
			device := thinqDevice{Type: kind, State: state}
			var command thinqCommand
			var err error
			switch kind {
			case "hood", "microwave_oven":
				command, err = translateHDPCommand(device, "set_thinq_pair", map[string]any{"lamp.lampBrightness": 1.0, "ventilation.fanSpeed": 0.0})
			case "washtower":
				state["dryer"].(map[string]any)["remoteControlEnable"].(map[string]any)["remoteControlEnabled"] = true
				command, err = translateHDPCommand(device, "set_thinq_laundry", map[string]any{"dryer.operation.dryerOperationMode": "START"})
			case "washtower_dryer":
				command, err = translateHDPCommand(device, "set_thinq_laundry", map[string]any{"operation.dryerOperationMode": "POWER_OFF"})
			default:
				state["response"].([]any)[0].(map[string]any)["remoteControlEnable"].(map[string]any)["remoteControlEnabled"] = true
				location := "MAIN"
				if kind == "mini_washcombo" {
					location = "MINI"
				}
				command, err = translateHDPCommand(device, "set_thinq_laundry", map[string]any{"location." + location + ".operation.washerOperationMode": "START"})
			}
			if err != nil {
				t.Fatal(err)
			}
			expected, _ := json.Marshal(examples[kind+"-command-example"].(map[string]any)["value"])
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/devices/test-device/control" || r.Header.Get("x-conditional-control") != "true" {
					t.Errorf("unexpected control request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
				}
				var body any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				actual, _ := json.Marshal(body)
				if string(actual) != string(expected) {
					t.Errorf("payload %s, expected %s", actual, expected)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			cfg := applySetupDefaults(setupConfig{PATToken: "mock-token", APIBaseURL: srv.URL, Country: "HU"})
			if err := newCloudThinQProvider().SendCommand(context.Background(), cfg, "test-device", command); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMarketplaceMetadataMatchesManifest(t *testing.T) {
	metadata := make([]struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}, 2)
	for index, path := range []string{"../../../../manifest/homenavi-integration.json", "../../../../marketplace/metadata.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &metadata[index]); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if metadata[0].ID == "" || metadata[0].Version == "" || metadata[0] != metadata[1] {
		t.Fatalf("marketplace metadata does not match manifest: %#v", metadata)
	}
}

func TestSDKContractDiscoveryAndControl(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	messageIDs := map[string]bool{}
	requests := map[string]int{}
	expectedCommands := map[string]any{
		"washer": map[string]any{"location": map[string]any{"locationName": "MAIN"}, "operation": map[string]any{"washerOperationMode": "START"}},
		"hood":   map[string]any{"lamp": map[string]any{"lampBrightness": float64(1)}, "ventilation": map[string]any{"fanSpeed": float64(0)}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.Method+" "+request.URL.Path]++
		for header, expected := range map[string]string{
			"Authorization": "Bearer sdk-test-token", "x-country": "HU", "x-client-id": "sdk-test-client", "x-api-key": "sdk-test-key", "x-service-phase": "OP",
		} {
			if got := request.Header.Get(header); got != expected {
				t.Errorf("%s: %s = %q, want %q", request.URL.Path, header, got, expected)
			}
		}
		messageID := request.Header.Get("x-message-id")
		decoded, err := base64.RawURLEncoding.DecodeString(messageID)
		if err != nil || len(decoded) != 16 || decoded[6]>>4 != 4 || decoded[8]&0xc0 != 0x80 || messageIDs[messageID] {
			t.Errorf("invalid or duplicate SDK-style message ID %q", messageID)
		}
		messageIDs[messageID] = true
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet && request.URL.Path == "/devices" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"response": []any{
				map[string]any{"deviceId": "washer", "deviceInfo": map[string]any{"deviceType": "DEVICE_WASHER", "alias": "Laundry"}},
				map[string]any{"deviceId": "hood", "deviceInfo": map[string]any{"deviceType": "DEVICE_HOOD", "alias": "Hood"}},
			}})
			return
		}
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "devices" || (parts[1] != "washer" && parts[1] != "hood") {
			http.NotFound(writer, request)
			return
		}
		if request.Method == http.MethodGet && (parts[2] == "state" || parts[2] == "profile") {
			suffix := "-profile-example"
			if parts[2] == "state" {
				suffix = "-object-example"
			}
			example := examples[parts[1]+suffix].(map[string]any)["value"]
			_ = json.NewEncoder(writer).Encode(map[string]any{"response": example})
			return
		}
		if request.Method == http.MethodPost && parts[2] == "control" {
			if request.Header.Get("x-conditional-control") != "true" || request.Header.Get("Content-Type") != "application/json" {
				t.Error("missing SDK control headers")
			}
			var payload any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || !reflect.DeepEqual(payload, expectedCommands[parts[1]]) {
				t.Errorf("unexpected %s control payload: %#v (error %v)", parts[1], payload, err)
			}
			if requests[request.Method+" "+request.URL.Path] > 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":{"code":"2302","message":"command not supported in state"}}`))
				return
			}
			_, _ = writer.Write([]byte(`{"response":{}}`))
			return
		}
		http.Error(writer, "unexpected request", http.StatusBadRequest)
	}))
	defer server.Close()
	config := applySetupDefaults(setupConfig{PATToken: "sdk-test-token", APIBaseURL: server.URL, Country: "HU", ClientID: "sdk-test-client", APIKey: "sdk-test-key"})
	provider := newCloudThinQProvider()
	devices, err := provider.ListDevices(context.Background(), config)
	if err != nil || len(devices) != 2 {
		t.Fatalf("SDK-shaped discovery returned %d devices: %v", len(devices), err)
	}
	for _, device := range devices {
		var command thinqCommand
		if device.ID == "washer" {
			command, err = translateHDPCommand(device, "start", nil)
		} else {
			command, err = translateHDPCommand(device, "set_thinq_pair", map[string]any{"lamp.lampBrightness": 1, "ventilation.fanSpeed": 0})
		}
		if err != nil {
			t.Fatalf("%s profile/state did not produce a command: %v", device.ID, err)
		}
		if err := provider.SendCommand(context.Background(), config, device.ID, command); err != nil {
			t.Fatalf("%s control failed: %v", device.ID, err)
		}
		err = provider.SendCommand(context.Background(), config, device.ID, command)
		var apiErr *thinQAPIError
		if !errors.As(err, &apiErr) || apiErr.Code != "2302" || apiErr.Status != http.StatusBadRequest || apiErr.Message != "command not supported in state" {
			t.Errorf("%s control error was not preserved: %v", device.ID, err)
		}
	}
	if len(messageIDs) != 9 {
		t.Errorf("received %d requests, expected 9", len(messageIDs))
	}
	for path, count := range map[string]int{
		"GET /devices": 1, "GET /devices/washer/state": 1, "GET /devices/washer/profile": 1,
		"GET /devices/hood/state": 1, "GET /devices/hood/profile": 1,
		"POST /devices/washer/control": 2, "POST /devices/hood/control": 2,
	} {
		if requests[path] != count {
			t.Errorf("%s: received %d requests, expected %d", path, requests[path], count)
		}
	}
}

func TestSDKContractHTTPFailures(t *testing.T) {
	for _, operation := range []string{"devices", "state", "profile", "control"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusForbidden)
				_, _ = writer.Write([]byte(`{"error":{"code":"1308","message":"no control authority"}}`))
			}))
			defer server.Close()
			config := applySetupDefaults(setupConfig{PATToken: "mock-token", APIBaseURL: server.URL, Country: "HU"})
			provider := newCloudThinQProvider()
			var err error
			switch operation {
			case "devices":
				_, err = provider.ListDevices(context.Background(), config)
			case "state":
				_, err = provider.fetchState(context.Background(), config, server.URL, "device")
			case "profile":
				_, err = provider.fetchProfile(context.Background(), config, server.URL, "device")
			case "control":
				err = provider.SendCommand(context.Background(), config, "device", thinqCommand{Name: "test", Params: map[string]any{"operation": map[string]any{"mode": "STOP"}}})
			}
			var apiErr *thinQAPIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != "1308" || apiErr.Message != "no control authority" {
				t.Fatalf("expected SDK-shaped API error, got %v", err)
			}
		})
	}
}

func TestSDKContractUnavailableProfileKeepsDeviceReadOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/devices":
			_, _ = writer.Write([]byte(`{"response":[{"deviceId":"purifier","deviceInfo":{"deviceType":"DEVICE_AIR_PURIFIER"}}]}`))
		case "/devices/purifier/state":
			_, _ = writer.Write([]byte(`{"response":{"operation":{"airPurifierOperationMode":"POWER_ON"}}}`))
		case "/devices/purifier/profile":
			writer.WriteHeader(http.StatusForbidden)
			_, _ = writer.Write([]byte(`{"error":{"code":"1308","message":"no control authority"}}`))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	config := applySetupDefaults(setupConfig{PATToken: "mock-token", APIBaseURL: server.URL, Country: "HU"})
	devices, err := newCloudThinQProvider().ListDevices(context.Background(), config)
	if err != nil || len(devices) != 1 {
		t.Fatalf("expected one read-only device, got %#v: %v", devices, err)
	}
	if mapThinQToHDPState(devices[0])["operation.airPurifierOperationMode"] != "POWER_ON" {
		t.Fatal("lost the available state when the profile request failed")
	}
	if inputs := applianceProfileInputs(devices[0]); len(inputs) != 0 {
		t.Fatalf("advertised controls without a profile: %#v", inputs)
	}
	if _, err := translateHDPCommand(devices[0], "set_thinq_property", map[string]any{"operation.airPurifierOperationMode": "POWER_OFF"}); err == nil {
		t.Fatal("accepted a command without a writable profile")
	}
}

func TestCloudProviderDiscoversAllDocumentedDeviceExamples(t *testing.T) {
	data, err := os.ReadFile("../../../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	examples := document["components"].(map[string]any)["examples"].(map[string]any)
	entries := []any{}
	for name := range examples {
		if !strings.HasSuffix(name, "-object-example") {
			continue
		}
		kind := strings.TrimSuffix(name, "-object-example")
		if _, ok := examples[kind+"-profile-example"]; !ok {
			t.Fatalf("missing profile for %s", kind)
		}
		entries = append(entries, map[string]any{"deviceId": kind, "deviceInfo": map[string]any{"deviceType": "DEVICE_" + strings.ToUpper(kind), "alias": kind}})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/devices" {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": entries})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "devices" || (parts[2] != "state" && parts[2] != "profile") {
			http.NotFound(w, r)
			return
		}
		suffix := "-profile-example"
		if parts[2] == "state" {
			suffix = "-object-example"
		}
		example, _ := examples[parts[1]+suffix].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"response": example["value"]})
	}))
	defer srv.Close()
	cfg := applySetupDefaults(setupConfig{PATToken: "mock-token", APIBaseURL: srv.URL, Country: "HU"})
	devices, err := newCloudThinQProvider().ListDevices(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != len(entries) {
		t.Fatalf("discovered %d of %d device examples", len(devices), len(entries))
	}
	for _, device := range devices {
		if _, ok := device.State["profile"].(map[string]any); !ok {
			t.Errorf("%s has no profile", device.ID)
		}
		if len(mapThinQToHDPState(device)) <= 1 {
			t.Errorf("%s has no mapped readings", device.ID)
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
