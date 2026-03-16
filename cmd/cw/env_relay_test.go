package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cwconfig "github.com/codewiresh/codewire/internal/config"
)

func TestResolveEnvRelayEnrollmentExplicitNetwork(t *testing.T) {
	dir := t.TempDir()
	relayURL := ""
	relaySession := "relay-session"
	defaultNetwork := "private-default"
	if err := cwconfig.SaveConfig(dir, &cwconfig.Config{
		RelayURL:     &relayURL,
		RelaySession: &relaySession,
		RelayNetwork: &defaultNetwork,
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	wantNetwork := "project-beta"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/invites" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+relaySession {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if body["fleet_id"] != wantNetwork {
			t.Fatalf("fleet_id = %v, want %q", body["fleet_id"], wantNetwork)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":          "CW-INV-ENV",
			"uses_remaining": 1,
			"expires_at":     time.Now().UTC().Add(24 * time.Hour),
		})
	}))
	defer srv.Close()

	relayURL = srv.URL
	if err := cwconfig.SaveConfig(dir, &cwconfig.Config{
		RelayURL:     &relayURL,
		RelaySession: &relaySession,
		RelayNetwork: &defaultNetwork,
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	enrollment, err := resolveEnvRelayEnrollment(dir, true, wantNetwork, false)
	if err != nil {
		t.Fatalf("resolveEnvRelayEnrollment: %v", err)
	}
	if enrollment == nil {
		t.Fatal("expected enrollment")
	}
	if enrollment.NetworkID != wantNetwork {
		t.Fatalf("NetworkID = %q, want %q", enrollment.NetworkID, wantNetwork)
	}
	if enrollment.InviteToken != "CW-INV-ENV" {
		t.Fatalf("InviteToken = %q", enrollment.InviteToken)
	}
}

func TestResolveEnvRelayEnrollmentPersistsConsent(t *testing.T) {
	dir := t.TempDir()
	relaySession := "relay-session"
	defaultNetwork := "private-default"
	autoJoin := (*bool)(nil)
	relayURL := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":          "CW-INV-PRIVATE",
			"uses_remaining": 1,
			"expires_at":     time.Now().UTC().Add(24 * time.Hour),
		})
	}))
	defer srv.Close()
	relayURL = srv.URL

	if err := cwconfig.SaveConfig(dir, &cwconfig.Config{
		RelayURL:             &relayURL,
		RelaySession:         &relaySession,
		RelayNetwork:         &defaultNetwork,
		RelayAutoJoinPrivate: autoJoin,
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	enrollment, err := resolveEnvRelayEnrollment(dir, true, "", false)
	if err != nil {
		t.Fatalf("resolveEnvRelayEnrollment: %v", err)
	}
	if enrollment == nil {
		t.Fatal("expected enrollment")
	}
	cfg, err := cwconfig.LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.RelayAutoJoinPrivate == nil || !*cfg.RelayAutoJoinPrivate {
		t.Fatal("expected relay_auto_join_private consent to persist")
	}
}
