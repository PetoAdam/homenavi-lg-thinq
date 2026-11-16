package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupStoreResolvePathDirectory(t *testing.T) {
	t.Parallel()
	s := &setupStore{path: "/tmp/thinq-secrets"}
	got := s.resolvePath()
	want := filepath.Join("/tmp/thinq-secrets", defaultSetupFileName)
	if got != want {
		t.Fatalf("resolvePath mismatch: got %q want %q", got, want)
	}
}

func TestDefaultSetupPathFromIntegrationsRoot(t *testing.T) {
	t.Parallel()
	set := func(k, v string) {
		if err := os.Setenv(k, v); err != nil {
			t.Fatalf("setenv %s: %v", k, err)
		}
	}
	unset := func(k string) {
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
	}

	unset("LG_THINQ_SETUP_PATH")
	unset("INTEGRATION_SECRETS_PATH")
	set("INTEGRATIONS_ROOT", "/opt/homenavi")
	t.Cleanup(func() {
		unset("LG_THINQ_SETUP_PATH")
		unset("INTEGRATION_SECRETS_PATH")
		unset("INTEGRATIONS_ROOT")
	})

	got := defaultSetupPath()
	want := filepath.Join("/opt/homenavi", "integrations", "secrets")
	if got != want {
		t.Fatalf("defaultSetupPath mismatch: got %q want %q", got, want)
	}
}

func TestSetupStoreSaveHandlesLegacyDirectoryTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	legacyDirTarget := filepath.Join(root, defaultSetupFileName)
	if err := os.MkdirAll(legacyDirTarget, 0o755); err != nil {
		t.Fatalf("mkdir legacy target: %v", err)
	}

	s := &setupStore{path: root}
	cfg := applySetupDefaults(setupConfig{PATToken: "test-token"})
	if err := s.save(cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	nestedFile := filepath.Join(legacyDirTarget, defaultSetupFileName)
	if _, err := os.Stat(nestedFile); err != nil {
		t.Fatalf("expected nested setup file to exist at %q: %v", nestedFile, err)
	}
}

func TestSetupStoreLoadHandlesLegacyDirectoryTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	legacyDirTarget := filepath.Join(root, defaultSetupFileName)
	if err := os.MkdirAll(legacyDirTarget, 0o755); err != nil {
		t.Fatalf("mkdir legacy target: %v", err)
	}

	raw := setupConfig{PATToken: "nested-token", SyncIntervalSec: 5}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal setup: %v", err)
	}
	nestedFile := filepath.Join(legacyDirTarget, defaultSetupFileName)
	if err := os.WriteFile(nestedFile, b, 0o600); err != nil {
		t.Fatalf("write nested setup file: %v", err)
	}

	s := &setupStore{path: root}
	got, err := s.load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got.PATToken != "nested-token" {
		t.Fatalf("unexpected PAT token: got %q", got.PATToken)
	}
}

func TestSetupStoreLoadDefaultsRealtimeEnabledWhenMissing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s := &setupStore{path: root}

	got, err := s.load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !got.RealtimeEnabled {
		t.Fatalf("expected realtime to default to enabled when config is missing")
	}
}

func TestSetupStoreLoadPreservesExplicitRealtimeDisabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	setupFile := filepath.Join(root, defaultSetupFileName)
	raw := setupConfig{
		PATToken:        "token",
		RealtimeEnabled: false,
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal setup: %v", err)
	}
	if err := os.WriteFile(setupFile, b, 0o600); err != nil {
		t.Fatalf("write setup file: %v", err)
	}

	s := &setupStore{path: root}
	got, err := s.load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got.RealtimeEnabled {
		t.Fatalf("expected explicit realtime_enabled=false to be preserved")
	}
}

func TestRealtimeEnvironmentIsOnlyADefault(t *testing.T) {
	for _, test := range []struct {
		name string
		env  string
		file string
		want bool
	}{
		{"enabled environment and disabled config", "true", `{"realtime_enabled":false}`, false},
		{"disabled environment and enabled config", "false", `{"realtime_enabled":true}`, true},
		{"disabled environment and missing field", "false", `{}`, false},
		{"enabled environment and missing field", "true", `{}`, true},
		{"invalid environment and missing field", "invalid", `{}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LG_THINQ_REALTIME_ENABLED", test.env)
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, defaultSetupFileName), []byte(test.file), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := (&setupStore{path: root}).load()
			if err != nil {
				t.Fatal(err)
			}
			if got.RealtimeEnabled != test.want {
				t.Fatalf("got realtime_enabled=%t, want %t", got.RealtimeEnabled, test.want)
			}
		})
	}
}

func TestRealtimeEnvironmentDefaultWithoutSetupFile(t *testing.T) {
	t.Setenv("LG_THINQ_REALTIME_ENABLED", "false")
	got, err := (&setupStore{path: t.TempDir()}).load()
	if err != nil {
		t.Fatal(err)
	}
	if got.RealtimeEnabled {
		t.Fatal("expected environment default to disable realtime for a new setup")
	}
}

func TestDecodeSetupUpdatePreservesOmittedRealtime(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		existing    bool
		want        bool
		patProvided bool
		wantErr     bool
	}{
		{"omitted enabled", `{}`, true, true, false, false},
		{"omitted disabled", `{}`, false, false, false, false},
		{"explicit disabled", `{"realtime_enabled":false}`, true, false, false, false},
		{"explicit enabled", `{"realtime_enabled":true,"pat_token":""}`, false, true, true, false},
		{"invalid type", `{"realtime_enabled":"false"}`, true, false, false, true},
		{"null realtime", `{"realtime_enabled":null}`, true, false, false, true},
		{"null payload", `null`, true, false, false, true},
		{"invalid json", `{`, true, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, patProvided, err := decodeSetupUpdate([]byte(test.body), setupConfig{RealtimeEnabled: test.existing})
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && (got.RealtimeEnabled != test.want || patProvided != test.patProvided) {
				t.Fatalf("got realtime=%t, patProvided=%t", got.RealtimeEnabled, patProvided)
			}
		})
	}
}
