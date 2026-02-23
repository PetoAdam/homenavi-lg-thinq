package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type integrationManifest struct {
	UI struct {
		Sidebar struct {
			Enabled bool   `json:"enabled"`
			Path    string `json:"path"`
		} `json:"sidebar"`
		Setup struct {
			Enabled bool   `json:"enabled"`
			Path    string `json:"path"`
		} `json:"setup"`
	} `json:"ui"`
}

func TestThinQManifestUsesDedicatedSetupAndDashboardUI(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "..", "..", "manifest", "homenavi-integration.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var m integrationManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	if !m.UI.Sidebar.Enabled {
		t.Fatalf("sidebar ui must be enabled")
	}
	if !m.UI.Setup.Enabled {
		t.Fatalf("setup ui must be enabled")
	}
	if m.UI.Sidebar.Path != "/ui/dashboard.html" {
		t.Fatalf("unexpected sidebar path: %q", m.UI.Sidebar.Path)
	}
	if m.UI.Setup.Path != "/ui/setup.html" {
		t.Fatalf("unexpected setup path: %q", m.UI.Setup.Path)
	}
	if m.UI.Sidebar.Path == m.UI.Setup.Path {
		t.Fatalf("sidebar and setup paths must be distinct")
	}
}

func TestThinQUIFilesExistAndContainExpectedTitles(t *testing.T) {
	uiDir := filepath.Join("..", "..", "..", "..", "web", "ui")

	setupBytes, err := os.ReadFile(filepath.Join(uiDir, "setup.html"))
	if err != nil {
		t.Fatalf("read setup ui: %v", err)
	}
	dashboardBytes, err := os.ReadFile(filepath.Join(uiDir, "dashboard.html"))
	if err != nil {
		t.Fatalf("read dashboard ui: %v", err)
	}

	setupHTML := string(setupBytes)
	dashboardHTML := string(dashboardBytes)

	if !strings.Contains(setupHTML, "<title>LG ThinQ Setup</title>") {
		t.Fatalf("setup ui title missing")
	}
	if !strings.Contains(dashboardHTML, "<title>LG ThinQ Dashboard</title>") {
		t.Fatalf("dashboard ui title missing")
	}
	if !strings.Contains(setupHTML, "./app.js") || !strings.Contains(dashboardHTML, "./app.js") {
		t.Fatalf("shared app.js script reference is required in both ui pages")
	}
}
