package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewClientUsesAPIKeyEnvWithConfigServer(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CW_CONFIG_DIR", configDir)
	t.Setenv("CODEWIRE_API_KEY", "cw_env_override")
	t.Setenv("CODEWIRE_SERVER_URL", "")

	if err := SaveConfig(&PlatformConfig{
		ServerURL:    "https://example.invalid",
		SessionToken: "session-token",
		DefaultOrg:   "org-default",
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	client, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.ServerURL != "https://example.invalid" {
		t.Fatalf("ServerURL = %q, want config server", client.ServerURL)
	}
	if client.SessionToken != "cw_env_override" {
		t.Fatalf("SessionToken = %q, want CODEWIRE_API_KEY", client.SessionToken)
	}
}

func TestNewClientUsesEnvWithoutConfig(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "missing")
	t.Setenv("CW_CONFIG_DIR", configDir)
	t.Setenv("CODEWIRE_API_KEY", "cw_env_only")
	t.Setenv("CODEWIRE_SERVER_URL", "https://api.codewire.test")

	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("expected config dir to be absent, got err=%v", err)
	}

	client, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.ServerURL != "https://api.codewire.test" {
		t.Fatalf("ServerURL = %q, want env server", client.ServerURL)
	}
	if client.SessionToken != "cw_env_only" {
		t.Fatalf("SessionToken = %q, want CODEWIRE_API_KEY", client.SessionToken)
	}
}
