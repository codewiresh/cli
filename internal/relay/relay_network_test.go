package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/store"
	"nhooyr.io/websocket"
)

func TestNodesListRequiresAuthAndScopesByNetwork(t *testing.T) {
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
	}, nil, nil))
	defer srv.Close()
	client := srv.Client()

	registerNode := func(networkID, nodeName string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"network_id": networkID,
			"node_name":  nodeName,
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/nodes", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer admin-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("register node: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("register node status = %d", resp.StatusCode)
		}
	}

	registerNode("network-a", "shared-node")
	registerNode("network-b", "shared-node")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/nodes?network_id=network-a", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unauthenticated list nodes: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/nodes?network_id=network-a", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = client.Do(req)
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
		t.Fatalf("nodes = %#v, want one network-a node", nodes)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/nodes?all=true", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("authenticated list all nodes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated all status = %d", resp.StatusCode)
	}

	nodes = nil
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode all nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("all nodes len = %d, want 2", len(nodes))
	}
	foundNetworks := map[string]bool{}
	for _, node := range nodes {
		foundNetworks[node.NetworkID] = true
	}
	if !foundNetworks["network-a"] || !foundNetworks["network-b"] {
		t.Fatalf("all nodes networks = %#v, want network-a and network-b", foundNetworks)
	}
}

func TestKVIsNetworkScopedAndRequiresAuth(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "admin-token",
	}, nil, nil))
	defer srv.Close()
	client := srv.Client()

	putKV := func(networkID, value string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/kv/tasks/build?network_id="+networkID, bytes.NewBufferString(value))
		req.Header.Set("Authorization", "Bearer admin-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("put kv: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("put kv status = %d", resp.StatusCode)
		}
	}

	putKV("network-a", "alpha")
	putKV("network-b", "beta")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/kv/tasks/build?network_id=network-a", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unauthenticated kv get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated kv status = %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/kv/tasks/build?network_id=network-a", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("network-a kv get: %v", err)
	}
	defer resp.Body.Close()
	var valueA bytes.Buffer
	if _, err := valueA.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read valueA: %v", err)
	}
	if valueA.String() != "alpha" {
		t.Fatalf("network-a value = %q, want alpha", valueA.String())
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/kv/tasks/build?network_id=network-b", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("network-b kv get: %v", err)
	}
	defer resp.Body.Close()
	var valueB bytes.Buffer
	if _, err := valueB.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read valueB: %v", err)
	}
	if valueB.String() != "beta" {
		t.Fatalf("network-b value = %q, want beta", valueB.String())
	}
}

func TestJoinRegistersNodeIntoInviteNetwork(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now().UTC()
	if err := st.InviteCreate(context.Background(), store.Invite{
		NetworkID:     "network-invite",
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
	}, nil, nil))
	defer srv.Close()
	client := srv.Client()

	body, _ := json.Marshal(map[string]string{
		"node_name":    "joined-node",
		"invite_token": "CW-INV-TEST",
	})
	resp, err := client.Post(srv.URL+"/api/v1/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join status = %d", resp.StatusCode)
	}

	node, err := st.NodeGet(context.Background(), "network-invite", "joined-node")
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
	}, nil, nil))
	defer srv.Close()
	client := srv.Client()

	createBody, _ := json.Marshal(map[string]string{"network_id": "project-alpha"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/networks", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create network status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/networks", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = client.Do(req)
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

func TestNodeConnectPersistsPeerURL(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	token := "node-token"
	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "network-a",
		Name:         "builder",
		Token:        token,
		AuthorizedAt: now,
		LastSeenAt:   now,
	}); err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:  "http://relay.test",
		AuthMode: "none",
	}, nil, nil))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/node/connect"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":       {"Bearer " + token},
			"X-CodeWire-Peer-URL": {"https://builder.example.com/ws"},
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer ws.CloseNow()

	node, err := st.NodeGet(context.Background(), "network-a", "builder")
	if err != nil {
		t.Fatalf("NodeGet: %v", err)
	}
	if node == nil {
		t.Fatal("expected node")
	}
	if node.PeerURL != "https://builder.example.com/ws" {
		t.Fatalf("PeerURL = %q, want advertised URL", node.PeerURL)
	}
}
