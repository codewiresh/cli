package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/auth"
	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/peerclient"
)

func setupPeerRuntimeNode(t *testing.T, networkID string) (*Node, string, string) {
	t.Helper()

	dir := t.TempDir()
	n, err := NewNode(dir)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}

	recipientID, err := n.Manager.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = n.Manager.Kill(recipientID) })
	if err := n.Manager.SetName(recipientID, "coder"); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	state, err := networkauth.NewIssuerState(networkID)
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}
	token, _, err := networkauth.SignRuntimeCredential(state, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignRuntimeCredential: %v", err)
	}

	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/network-auth/bundle" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("network_id"); got != networkID {
			t.Fatalf("network_id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state.Bundle(time.Now().UTC(), time.Hour))
	}))
	t.Cleanup(relaySrv.Close)
	relayURL := relaySrv.URL
	n.config.RelayURL = &relayURL
	n.config.RelayNetwork = &networkID

	oldListen := n.config.Node.Listen
	t.Cleanup(func() { n.config.Node.Listen = oldListen })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp listen unavailable in this test environment: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	n.config.Node.Listen = &addr

	go func() {
		_ = n.runWSServer(ctx, addr, &peer.Server{
			Sessions:        n.Manager,
			NodeName:        n.config.Node.Name,
			AuthorizeSender: n.authorizePeerSender,
		})
	}()
	time.Sleep(50 * time.Millisecond)

	return n, "ws://" + addr + "/peer", token
}

func TestPeerWebSocketEndpointRequiresRuntimeCredential(t *testing.T) {
	_, baseURL, token := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	msgID, err := peerclient.Msg(context.Background(), client, nil, "", peer.SessionLocator{Name: "coder"}, "hello over /peer", "inbox")
	if err != nil {
		t.Fatalf("Msg: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message id")
	}

	messages, err := peerclient.Inbox(context.Background(), client, peer.SessionLocator{Name: "coder"}, 10)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].Body != "hello over /peer" {
		t.Fatalf("message body = %q", messages[0].Body)
	}
}

func TestPeerWebSocketEndpointRejectsLocalNodeToken(t *testing.T) {
	n, baseURL, _ := setupPeerRuntimeNode(t, "project-alpha")

	token, err := auth.LoadOrGenerateToken(n.dataDir)
	if err != nil {
		t.Fatalf("LoadOrGenerateToken: %v", err)
	}

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err == nil {
		defer ws.CloseNow()
		defer client.Close()
		t.Fatal("expected unauthorized dial")
	}
}

func TestPeerWebSocketEndpointRejectsRelaySessionFallback(t *testing.T) {
	_, baseURL, _ := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, "relay-session")
	if err == nil {
		defer ws.CloseNow()
		defer client.Close()
		t.Fatal("expected unauthorized dial")
	}
}
