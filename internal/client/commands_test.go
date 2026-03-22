package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRelayAuthUsesOverridesWithoutConfig(t *testing.T) {
	t.Setenv("CODEWIRE_RELAY_URL", "")
	t.Setenv("CODEWIRE_RELAY_SESSION", "")
	t.Setenv("CODEWIRE_RELAY_NETWORK", "")

	dir := t.TempDir()
	relayURL, authToken, networkID, err := loadRelayAuth(dir, RelayAuthOptions{
		RelayURL:  "http://127.0.0.1:8080",
		AuthToken: "dev-secret",
		NetworkID: "alpha",
	})
	if err != nil {
		t.Fatalf("loadRelayAuth returned error: %v", err)
	}
	if relayURL != "http://127.0.0.1:8080" {
		t.Fatalf("relayURL = %q, want override", relayURL)
	}
	if authToken != "dev-secret" {
		t.Fatalf("authToken = %q, want override", authToken)
	}
	if networkID != "alpha" {
		t.Fatalf("networkID = %q, want override", networkID)
	}
}

func TestLoadRelayAuthUsesEnvFallback(t *testing.T) {
	t.Setenv("CODEWIRE_RELAY_URL", "http://127.0.0.1:8080")
	t.Setenv("CODEWIRE_RELAY_SESSION", "env-token")
	t.Setenv("CODEWIRE_RELAY_NETWORK", "env-network")

	dir := t.TempDir()
	relayURL, authToken, networkID, err := loadRelayAuth(dir, RelayAuthOptions{})
	if err != nil {
		t.Fatalf("loadRelayAuth returned error: %v", err)
	}
	if relayURL != "http://127.0.0.1:8080" {
		t.Fatalf("relayURL = %q, want env value", relayURL)
	}
	if authToken != "env-token" {
		t.Fatalf("authToken = %q, want env value", authToken)
	}
	if networkID != "env-network" {
		t.Fatalf("networkID = %q, want env value", networkID)
	}
}

func TestLoadRelayAuthOverridesConfig(t *testing.T) {
	t.Setenv("CODEWIRE_RELAY_URL", "")
	t.Setenv("CODEWIRE_RELAY_SESSION", "")
	t.Setenv("CODEWIRE_RELAY_NETWORK", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := []byte("relay_url = \"https://relay.example.com\"\nrelay_session = \"session-token\"\nrelay_network = \"default\"\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile(config.toml): %v", err)
	}

	relayURL, authToken, networkID, err := loadRelayAuth(dir, RelayAuthOptions{
		RelayURL:  "http://127.0.0.1:8080",
		AuthToken: "dev-secret",
		NetworkID: "alpha",
	})
	if err != nil {
		t.Fatalf("loadRelayAuth returned error: %v", err)
	}
	if relayURL != "http://127.0.0.1:8080" {
		t.Fatalf("relayURL = %q, want override", relayURL)
	}
	if authToken != "dev-secret" {
		t.Fatalf("authToken = %q, want override", authToken)
	}
	if networkID != "alpha" {
		t.Fatalf("networkID = %q, want override", networkID)
	}
}

func TestUseNetworkPersistsConfig(t *testing.T) {
	dir := t.TempDir()

	if err := UseNetwork(dir, "project-alpha"); err != nil {
		t.Fatalf("UseNetwork: %v", err)
	}

	_, _, networkID, err := loadRelayAuth(dir, RelayAuthOptions{
		RelayURL:  "http://127.0.0.1:8080",
		AuthToken: "dev-secret",
	})
	if err != nil {
		t.Fatalf("loadRelayAuth: %v", err)
	}
	if networkID != "project-alpha" {
		t.Fatalf("networkID = %q, want project-alpha", networkID)
	}
}

func TestCreateNetworkCreatesAndSelectsNetwork(t *testing.T) {
	origClient := relayHTTPClient
	defer func() { relayHTTPClient = origClient }()

	dir := t.TempDir()

	sawCreate := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/networks" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dev-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if body["network_id"] != "project-beta" {
			t.Fatalf("network_id = %q", body["network_id"])
		}
		sawCreate = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "created",
			"network_id": "project-beta",
		})
	}))
	defer srv.Close()
	relayHTTPClient = srv.Client()

	if err := CreateNetwork(dir, "project-beta", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
	}, true); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	if !sawCreate {
		t.Fatal("expected create request")
	}

	_, _, networkID, err := loadRelayAuth(dir, RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
	})
	if err != nil {
		t.Fatalf("loadRelayAuth: %v", err)
	}
	if networkID != "project-beta" {
		t.Fatalf("networkID = %q, want project-beta", networkID)
	}
}
