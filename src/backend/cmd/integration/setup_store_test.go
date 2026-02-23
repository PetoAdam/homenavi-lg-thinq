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
