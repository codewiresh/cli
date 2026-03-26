package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterWithAuthToken(t *testing.T) {
	sawAuth := ""
	sawNetwork := ""
	sawNode := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/nodes" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		var req struct {
			NodeName  string `json:"node_name"`
			NetworkID string `json:"network_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		sawNode = req.NodeName
		sawNetwork = req.NetworkID
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"node_token": "node-token"})
	}))
	defer srv.Close()

	nodeToken, err := RegisterWithAuthToken(context.Background(), srv.URL, "project-alpha", "dev-1", "sess-token")
	if err != nil {
		t.Fatalf("RegisterWithAuthToken: %v", err)
	}
	if nodeToken != "node-token" {
		t.Fatalf("nodeToken = %q, want node-token", nodeToken)
	}
	if sawAuth != "Bearer sess-token" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if sawNode != "dev-1" {
		t.Fatalf("node_name = %q", sawNode)
	}
	if sawNetwork != "project-alpha" {
		t.Fatalf("network_id = %q", sawNetwork)
	}
}

func TestJoinWithInvite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/join" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			NodeName    string `json:"node_name"`
			InviteToken string `json:"invite_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if req.NodeName != "dev-2" {
			t.Fatalf("node_name = %q", req.NodeName)
		}
		if req.InviteToken != "CW-INV-TEST" {
			t.Fatalf("invite_token = %q", req.InviteToken)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"node_token": "node-token",
			"node_name":  "dev-2",
			"network_id": "project-alpha",
		})
	}))
	defer srv.Close()

	result, err := JoinWithInvite(context.Background(), srv.URL, "dev-2", "CW-INV-TEST")
	if err != nil {
		t.Fatalf("JoinWithInvite: %v", err)
	}
	if result.NodeToken != "node-token" {
		t.Fatalf("NodeToken = %q", result.NodeToken)
	}
	if result.NodeName != "dev-2" {
		t.Fatalf("NodeName = %q", result.NodeName)
	}
	if result.NetworkID != "project-alpha" {
		t.Fatalf("NetworkID = %q", result.NetworkID)
	}
}

func TestSSHURIIncludesNetworkPrefix(t *testing.T) {
	got := SSHURI("https://relay.example.com", "network-alpha", "builder", "node-token", 2222)
	want := "ssh://network-alpha/builder:node-token@relay.example.com:2222"
	if got != want {
		t.Fatalf("SSHURI = %q, want %q", got, want)
	}
}
