package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/auth"
	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/peerclient"
	"github.com/codewiresh/codewire/internal/protocol"
)

func setupPeerRuntimeNode(t *testing.T, networkID string) (*Node, string, string, *networkauth.IssuerState) {
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

	relaySrv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/network-auth/bundle":
			if got := r.Header.Get("Authorization"); got != "Bearer relay-node-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if got := r.URL.Query().Get("network_id"); got != networkID {
				t.Fatalf("network_id = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(state.Bundle(time.Now().UTC(), time.Hour))
		case "/api/v1/groups/bindings":
			if got := r.Header.Get("Authorization"); got != "Bearer relay-node-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]any{})
		case "/api/v1/groups/mesh/members", "/api/v1/groups/other/members":
			if got := r.Header.Get("Authorization"); got != "Bearer relay-node-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
			return
		}
	}))
	t.Cleanup(relaySrv.Close)
	relayURL := relaySrv.URL
	n.config.RelayURL = &relayURL
	n.config.RelayNodeNetwork = &networkID
	relayNodeToken := "relay-node-token"
	n.config.RelayNodeToken = &relayNodeToken

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
			Sessions:                n.Manager,
			NodeName:                n.config.Node.Name,
			AuthorizePeer:           n.authorizePeerRuntime,
			AuthorizeSender:         n.authorizePeerSender,
			AuthorizeDelivery:       n.authorizePeerDelivery,
			AuthorizeObserver:       n.authorizePeerObserver,
			RequireRemoteSenderAuth: true,
		})
	}()
	time.Sleep(50 * time.Millisecond)

	return n, "ws://" + addr + "/peer", token, state
}

func TestPeerWebSocketEndpointRequiresRuntimeCredential(t *testing.T) {
	n, baseURL, token, state := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	if _, err := peerclient.Msg(context.Background(), client, nil, "", peer.SessionLocator{Name: "coder"}, "hello over /peer", "inbox"); err == nil {
		t.Fatal("expected anonymous remote message to be rejected")
	}

	delegation, _, err := networkauth.SignSenderDelegation(state, "dev-1", nil, "planner", nil, []string{"msg"}, n.config.Node.Name, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignSenderDelegation: %v", err)
	}
	msgID, err := peerclient.Msg(context.Background(), client, &peer.SessionLocator{Node: "dev-1", Name: "planner"}, delegation, peer.SessionLocator{Name: "coder"}, "hello over /peer", "inbox")
	if err != nil {
		t.Fatalf("delegated Msg: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message id")
	}

	recipientID, err := n.Manager.ResolveByName("coder")
	if err != nil {
		t.Fatalf("ResolveByName: %v", err)
	}
	events, err := n.Manager.ReadMessages(recipientID, 10)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("messages len = %d, want 1", len(events))
	}
	if _, err := peerclient.Inbox(context.Background(), client, peer.SessionLocator{Name: "coder"}, 10); err == nil {
		t.Fatal("expected remote inbox reads to be rejected")
	}
	observerGrant, _, err := networkauth.SignObserverDelegation(state, n.config.Node.Name, nil, "coder", []string{"msg.read", "msg.listen"}, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}
	messages, err := peerclient.InboxWithGrant(context.Background(), client, peer.SessionLocator{Name: "coder"}, observerGrant, 10)
	if err != nil {
		t.Fatalf("InboxWithGrant: %v", err)
	}
	if len(messages) != 1 || messages[0].Body != "hello over /peer" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestPeerWebSocketEndpointRejectsLocalNodeToken(t *testing.T) {
	n, baseURL, _, _ := setupPeerRuntimeNode(t, "project-alpha")

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
	_, baseURL, _, _ := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, "relay-session")
	if err == nil {
		defer ws.CloseNow()
		defer client.Close()
		t.Fatal("expected unauthorized dial")
	}
}

func TestPeerWebSocketEndpointRejectsQueryParamToken(t *testing.T) {
	_, baseURL, token, _ := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL+"?token="+token, "")
	if err == nil {
		defer ws.CloseNow()
		defer client.Close()
		t.Fatal("expected query-param token dial to fail")
	}
}

func TestPeerWebSocketEndpointRejectsRuntimeCredentialReplay(t *testing.T) {
	_, baseURL, token, _ := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket first: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	replayedClient, replayedWS, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err == nil {
		defer replayedWS.CloseNow()
		defer replayedClient.Close()
		t.Fatal("expected replayed runtime credential dial to fail")
	}
}

func TestAuthorizePeerSenderRejectsSenderDelegationReplay(t *testing.T) {
	n, _, _, state := setupPeerRuntimeNode(t, "project-alpha")

	sessionID, err := n.Manager.ResolveByName("coder")
	if err != nil {
		t.Fatalf("ResolveByName: %v", err)
	}
	delegation, _, err := networkauth.SignSenderDelegation(state, "dev-1", &sessionID, "coder", nil, []string{"msg"}, n.config.Node.Name, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignSenderDelegation: %v", err)
	}
	from := &peer.SessionLocator{Node: "dev-1", ID: &sessionID}

	authorized, err := n.authorizePeerSender(context.Background(), "msg", from, delegation)
	if err != nil {
		t.Fatalf("authorizePeerSender first: %v", err)
	}
	if authorized == nil || authorized.DisplayName != "dev-1:coder" {
		t.Fatalf("authorized = %#v", authorized)
	}

	if _, err := n.authorizePeerSender(context.Background(), "msg", from, delegation); err == nil {
		t.Fatal("expected replayed sender delegation to fail")
	}
}

func TestPeerWebSocketListenRequiresObserverGrant(t *testing.T) {
	n, baseURL, token, state := setupPeerRuntimeNode(t, "project-alpha")

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	session := &peer.SessionLocator{Name: "coder"}
	if err := peerclient.ListenWithGrant(context.Background(), client, session, "", func(*protocol.SessionEvent) error { return nil }); err == nil {
		t.Fatal("expected listen without observer grant to fail")
	}
	_ = client.Close()
	ws.CloseNow()

	token, _, err = networkauth.SignRuntimeCredential(state, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignRuntimeCredential: %v", err)
	}
	client, ws, err = peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket second: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	observerGrant, _, err := networkauth.SignObserverDelegation(state, n.config.Node.Name, nil, "coder", []string{"msg.listen"}, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventsCh := make(chan *protocol.SessionEvent, 1)
	readyCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	var once sync.Once
	go func() {
		errCh <- peerclient.ListenWithGrantAndReady(ctx, client, session, observerGrant, func() error {
			readyCh <- struct{}{}
			return nil
		}, func(event *protocol.SessionEvent) error {
			once.Do(func() {
				eventsCh <- event
				cancel()
			})
			return nil
		})
	}()

	select {
	case <-readyCh:
	case err := <-errCh:
		t.Fatalf("listen exited early: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for listen ack")
	}

	recipientID, err := n.Manager.ResolveByName("coder")
	if err != nil {
		t.Fatalf("ResolveByName: %v", err)
	}
	if _, err := n.Manager.SendMessage(0, recipientID, "hello listen"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case event := <-eventsCh:
		if event == nil || event.EventType != "direct.message" {
			t.Fatalf("event = %#v", event)
		}
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Fatalf("listen error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for listen event")
	}
}

func TestPeerWebSocketRejectsCrossGroupMessage(t *testing.T) {
	n, baseURL, token, state := setupPeerRuntimeNode(t, "project-alpha")

	groupedID, err := n.Manager.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "", "group:mesh")
	if err != nil {
		t.Fatalf("Launch grouped session: %v", err)
	}
	t.Cleanup(func() { _ = n.Manager.Kill(groupedID) })
	if err := n.Manager.SetName(groupedID, "mesh-agent"); err != nil {
		t.Fatalf("SetName grouped session: %v", err)
	}

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	delegation, _, err := networkauth.SignSenderDelegation(state, "dev-1", nil, "outsider", []string{"other"}, []string{"msg"}, n.config.Node.Name, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignSenderDelegation: %v", err)
	}
	if _, err := peerclient.Msg(context.Background(), client, &peer.SessionLocator{Node: "dev-1", Name: "outsider"}, delegation, peer.SessionLocator{Name: "mesh-agent"}, "blocked", "inbox"); err == nil {
		t.Fatal("expected cross-group message to be rejected")
	}
}

func TestPeerWebSocketRejectsCrossGroupRequest(t *testing.T) {
	n, baseURL, token, state := setupPeerRuntimeNode(t, "project-alpha")

	groupedID, err := n.Manager.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "", "group:mesh")
	if err != nil {
		t.Fatalf("Launch grouped session: %v", err)
	}
	t.Cleanup(func() { _ = n.Manager.Kill(groupedID) })
	if err := n.Manager.SetName(groupedID, "mesh-agent"); err != nil {
		t.Fatalf("SetName grouped session: %v", err)
	}

	client, ws, err := peerclient.DialWebSocket(context.Background(), baseURL, token)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	defer ws.CloseNow()
	defer client.Close()

	delegation, _, err := networkauth.SignSenderDelegation(state, "dev-1", nil, "outsider", []string{"other"}, []string{"request"}, n.config.Node.Name, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignSenderDelegation: %v", err)
	}
	if _, err := peerclient.Request(context.Background(), client, &peer.SessionLocator{Node: "dev-1", Name: "outsider"}, delegation, peer.SessionLocator{Name: "mesh-agent"}, "blocked request", 1, "inbox"); err == nil {
		t.Fatal("expected cross-group request to be rejected")
	}
}

func TestAuthorizeLocalDeliveryRejectsCrossGroupMessage(t *testing.T) {
	n, _, _, _ := setupPeerRuntimeNode(t, "project-alpha")

	fromID, err := n.Manager.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "", "group:other")
	if err != nil {
		t.Fatalf("Launch source session: %v", err)
	}
	t.Cleanup(func() { _ = n.Manager.Kill(fromID) })
	if err := n.Manager.SetName(fromID, "other-agent"); err != nil {
		t.Fatalf("SetName source session: %v", err)
	}

	toID, err := n.Manager.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "", "group:mesh")
	if err != nil {
		t.Fatalf("Launch target session: %v", err)
	}
	t.Cleanup(func() { _ = n.Manager.Kill(toID) })
	if err := n.Manager.SetName(toID, "mesh-agent"); err != nil {
		t.Fatalf("SetName target session: %v", err)
	}

	if err := n.authorizeLocalDelivery(context.Background(), fromID, toID, "msg"); err == nil {
		t.Fatal("expected local cross-group delivery to be rejected")
	}
}

func TestNodeSyncsGroupMembershipOnNameChangeAndExit(t *testing.T) {
	dir := t.TempDir()
	n, err := NewNode(dir)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}

	var requests []string
	relaySrv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if got := r.Header.Get("Authorization"); got != "Bearer relay-node-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/groups/mesh/members" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case http.MethodDelete:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer relaySrv.Close()

	relayURL := relaySrv.URL
	relayToken := "relay-node-token"
	n.config.RelayURL = &relayURL
	n.config.RelayNodeToken = &relayToken
	n.config.Node.Name = "node-a"

	sessionID, err := n.Manager.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "", "group:mesh")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := n.Manager.SetName(sessionID, "agent-1"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if err := n.Manager.Kill(sessionID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0] != "POST /api/v1/groups/mesh/members?" {
		t.Fatalf("add request = %q", requests[0])
	}
	if requests[1] != "DELETE /api/v1/groups/mesh/members?node_name=node-a&session_name=agent-1" {
		t.Fatalf("remove request = %q", requests[1])
	}
}
