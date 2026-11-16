package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExportHDPMetadataForVerification(t *testing.T) {
	devices := map[string]thinqDevice{
		"tv":     {ID: "tv", Type: "DEVICE_TV", Online: true, State: map[string]any{"volume": 20.0}},
		"washer": testWasherDevice(true),
		"purifier": {ID: "purifier", Type: "DEVICE_AIR_PURIFIER", Online: true, State: map[string]any{
			"operation": map[string]any{"airPurifierOperationMode": "POWER_OFF"},
			"profile": map[string]any{"property": map[string]any{
				"operation": map[string]any{"airPurifierOperationMode": map[string]any{"type": "enum", "mode": []any{"r", "w"}, "value": map[string]any{"r": []any{"POWER_ON", "POWER_OFF"}, "w": []any{"POWER_ON", "POWER_OFF"}}}},
				"airPurifierJobMode": map[string]any{"currentJobMode": map[string]any{"type": "enum", "mode": []any{"r", "w"}, "value": map[string]any{"w": []any{"AUTO", "MANUAL"}}}},
			}},
		}},
	}
	for name, device := range devices {
		metadata := mapThinQToHDPMetadata(device)
		capabilities := metadata["capabilities"].([]map[string]any)
		inputs := metadata["inputs"].([]map[string]any)
		if name == "washer" {
			var writableControl, matchingInput bool
			for _, capability := range capabilities {
				access, _ := capability["access"].(map[string]any)
				if capability["id"] == "appliance.washer.control" && capability["property"] == "operation_mode" && access["write"] == true {
					writableControl = true
				}
			}
			for _, input := range inputs {
				if input["capability_id"] == "appliance.washer.control" && input["property"] == "operation_mode" && input["type"] == "select" {
					matchingInput = true
				}
			}
			if !writableControl || !matchingInput {
				t.Fatal("washer sample must expose its writable operation_mode control")
			}
		}
		if name == "purifier" && (len(inputs) != 2 || inputs[0]["id"] == inputs[1]["id"]) {
			t.Fatalf("expected unique published purifier controls: %#v", inputs)
		}
		metadata["schema"] = "hdp.v1"
		payload, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if dir := os.Getenv("HOMENAVI_HDP_METADATA_OUTPUT_DIR"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, "lg-thinq-"+name+".json"), payload, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
