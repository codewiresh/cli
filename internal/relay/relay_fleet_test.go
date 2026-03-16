package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/store"
)

func TestNodesListRequiresAuthAndScopesByFleet(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := NewNodeHub()
	sessions := NewPendingSessions()
	srv := httptest.NewServer(buildMux(hub, sessions, st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "admin-token",
	}))
	defer srv.Close()

	registerNode := func(fleetID, nodeName string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"fleet_id":  fleetID,
			"node_name": nodeName,
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/nodes", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer admin-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("register node: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("register node status = %d", resp.StatusCode)
		}
	}

	registerNode("fleet-a", "shared-node")
	registerNode("fleet-b", "shared-node")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/nodes?fleet_id=fleet-a", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unauthenticated list nodes: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/nodes?fleet_id=fleet-a", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authenticated list nodes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d", resp.StatusCode)
	}

	var nodes []nodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "shared-node" {
		t.Fatalf("nodes = %#v, want one fleet-a node", nodes)
	}
}

func TestKVIsFleetScopedAndRequiresAuth(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "admin-token",
	}))
	defer srv.Close()

	putKV := func(fleetID, value string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/kv/tasks/build?fleet_id="+fleetID, bytes.NewBufferString(value))
		req.Header.Set("Authorization", "Bearer admin-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put kv: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("put kv status = %d", resp.StatusCode)
		}
	}

	putKV("fleet-a", "alpha")
	putKV("fleet-b", "beta")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/kv/tasks/build?fleet_id=fleet-a", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unauthenticated kv get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated kv status = %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/kv/tasks/build?fleet_id=fleet-a", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fleet-a kv get: %v", err)
	}
	defer resp.Body.Close()
	var valueA bytes.Buffer
	if _, err := valueA.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read valueA: %v", err)
	}
	if valueA.String() != "alpha" {
		t.Fatalf("fleet-a value = %q, want alpha", valueA.String())
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/kv/tasks/build?fleet_id=fleet-b", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fleet-b kv get: %v", err)
	}
	defer resp.Body.Close()
	var valueB bytes.Buffer
	if _, err := valueB.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read valueB: %v", err)
	}
	if valueB.String() != "beta" {
		t.Fatalf("fleet-b value = %q, want beta", valueB.String())
	}
}

func TestJoinRegistersNodeIntoInviteFleet(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now().UTC()
	if err := st.InviteCreate(context.Background(), store.Invite{
		FleetID:       "fleet-invite",
		Token:         "CW-INV-TEST",
		UsesRemaining: 1,
		ExpiresAt:     now.Add(1 * time.Hour),
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("InviteCreate: %v", err)
	}

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:  "http://relay.test",
		AuthMode: "none",
	}))
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{
		"node_name":    "joined-node",
		"invite_token": "CW-INV-TEST",
	})
	resp, err := http.Post(srv.URL+"/api/v1/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join status = %d", resp.StatusCode)
	}

	node, err := st.NodeGet(context.Background(), "fleet-invite", "joined-node")
	if err != nil {
		t.Fatalf("NodeGet: %v", err)
	}
	if node == nil {
		t.Fatal("expected joined node")
	}
}

func TestNetworksCanBeCreatedAndListed(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "admin-token",
	}))
	defer srv.Close()

	createBody, _ := json.Marshal(map[string]string{"network_id": "project-alpha"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/networks", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create network status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/networks", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list networks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list networks status = %d", resp.StatusCode)
	}

	var networks []networkResponse
	if err := json.NewDecoder(resp.Body).Decode(&networks); err != nil {
		t.Fatalf("decode networks: %v", err)
	}

	found := map[string]networkResponse{}
	for _, network := range networks {
		found[network.ID] = network
	}
	if _, ok := found["default"]; !ok {
		t.Fatal("expected default network")
	}
	if _, ok := found["project-alpha"]; !ok {
		t.Fatal("expected project-alpha network")
	}
}
