package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	cwconfig "github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/session"
	tailnetlib "github.com/codewiresh/tailnet"
	"github.com/google/uuid"
	"nhooyr.io/websocket"
	"tailscale.com/tailcfg"
)

func startRuntimePeerMessagingTestServer(t *testing.T, nodeName, sessionName string, state *networkauth.IssuerState) (*session.SessionManager, uint32, *httptest.Server) {
	t.Helper()

	manager, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	sessionID, err := manager.Launch([]string{"sleep", "300"}, t.TempDir(), nil, nil, sessionName)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = manager.Kill(sessionID) })
	if err := manager.SetName(sessionID, sessionName); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	peerServer := &peer.Server{
		Sessions: manager,
		NodeName: nodeName,
		AuthorizePeer: func(_ context.Context, runtimeCredential string) (*peer.AuthenticatedPeer, error) {
			if state == nil {
				if strings.TrimSpace(runtimeCredential) == "" {
					return nil, io.EOF
				}
				return &peer.AuthenticatedPeer{SubjectKind: networkauth.SubjectKindClient, SubjectID: "client"}, nil
			}
			claims, err := networkauth.VerifyRuntimeCredential(runtimeCredential, state.Bundle(time.Now().UTC(), time.Hour), time.Now().UTC())
			if err != nil {
				return nil, err
			}
			return &peer.AuthenticatedPeer{
				NetworkID:   claims.NetworkID,
				SubjectKind: claims.SubjectKind,
				SubjectID:   claims.SubjectID,
			}, nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/peer", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if state != nil {
			if _, err := networkauth.VerifyRuntimeCredential(token, state.Bundle(time.Now().UTC(), time.Hour), time.Now().UTC()); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer wsConn.CloseNow()
		peerServer.ServeWebSocket(r.Context(), wsConn)
	})

	srv := newIPv4TestServer(t, mux)
	return manager, sessionID, srv
}

func startRuntimeRelayServer(t *testing.T, state *networkauth.IssuerState, relaySession, relayNetwork string, nodes []map[string]any) *httptest.Server {
	t.Helper()

	return newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			if got := r.Header.Get("Authorization"); got != "Bearer "+relaySession {
				t.Fatalf("Authorization = %q", got)
			}
			if relayNetwork != "" {
				if networkID := r.URL.Query().Get("network_id"); networkID != relayNetwork {
					t.Fatalf("network_id = %q", networkID)
				}
			}
			if nodes == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(nodes)
		case "/api/v1/network-auth/runtime/client":
			if got := r.Header.Get("Authorization"); got != "Bearer "+relaySession {
				t.Fatalf("Authorization = %q", got)
			}
			token, claims, err := networkauth.SignRuntimeCredential(state, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
			if err != nil {
				t.Fatalf("SignRuntimeCredential: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(networkauth.RuntimeCredentialResponse{
				Credential:  token,
				NetworkID:   claims.NetworkID,
				SubjectKind: claims.SubjectKind,
				SubjectID:   claims.SubjectID,
				ExpiresAt:   claims.ExpiresAt,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func startRuntimeTailnetRelayNode(t *testing.T, relayNetwork, relaySession, nodeName, sessionName string) (*networkauth.IssuerState, *session.SessionManager, uint32, *httptest.Server) {
	t.Helper()

	state, err := networkauth.NewIssuerState(relayNetwork)
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	manager, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	sessionID, err := manager.Launch([]string{"sleep", "300"}, t.TempDir(), nil, nil, sessionName)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = manager.Kill(sessionID) })
	if err := manager.SetName(sessionID, sessionName); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	coord := tailnetlib.NewCoordinator(slog.Default())
	t.Cleanup(func() { _ = coord.Close() })
	derpSrv := tailnetlib.NewDERPServer()
	derpHandler, derpCleanup := tailnetlib.DERPHandler(derpSrv)
	t.Cleanup(func() {
		derpCleanup()
		_ = derpSrv.Close()
	})

	mux := http.NewServeMux()
	mux.Handle("/derp", derpHandler)
	mux.Handle("/derp/", derpHandler)
	mux.HandleFunc("/derp/latency-check", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+relaySession {
			t.Fatalf("Authorization = %q", got)
		}
		if relayNetwork != "" {
			got := r.URL.Query().Get("network_id")
			if got != relayNetwork {
				t.Fatalf("network_id = %q", got)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name":      nodeName,
			"connected": true,
		}})
	})
	mux.HandleFunc("/api/v1/network-auth/runtime/client", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+relaySession {
			t.Fatalf("Authorization = %q", got)
		}
		token, claims, err := networkauth.SignRuntimeCredential(state, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
		if err != nil {
			t.Fatalf("SignRuntimeCredential: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(networkauth.RuntimeCredentialResponse{
			Credential:  token,
			NetworkID:   claims.NetworkID,
			SubjectKind: claims.SubjectKind,
			SubjectID:   claims.SubjectID,
			ExpiresAt:   claims.ExpiresAt,
		})
	})
	mux.HandleFunc("/api/v1/tailnet/coordinate", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, err := networkauth.VerifyRuntimeCredential(token, state.Bundle(time.Now().UTC(), time.Hour), time.Now().UTC())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer wsConn.CloseNow()

		peerID := peer.StablePrincipalUUID(claims.NetworkID, claims.SubjectKind, claims.SubjectID)
		if claims.SubjectKind == networkauth.SubjectKindClient {
			peerID = uuid.New()
		}
		respCh := coord.Register(peerID, claims.SubjectKind+":"+claims.SubjectID)
		defer coord.Deregister(peerID)

		if err := wsConn.Write(r.Context(), websocket.MessageText, mustJSON(t, peer.TailnetCoordinateResponse{
			Type:    "derp_map",
			DERPMap: mustDERPMap(t, relayNetwork, r.Host),
		})); err != nil {
			return
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			for nodes := range respCh {
				if len(nodes) == 0 {
					continue
				}
				if err := wsConn.Write(r.Context(), websocket.MessageText, mustJSON(t, peer.TailnetCoordinateResponse{
					Type:  "peer_update",
					Nodes: nodes,
				})); err != nil {
					return
				}
			}
		}()

		for {
			_, data, err := wsConn.Read(r.Context())
			if err != nil {
				<-done
				return
			}
			var req peer.TailnetCoordinateRequest
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			switch req.Type {
			case "node":
				if req.Node != nil {
					coord.UpdateNode(peerID, req.Node)
				}
			case "subscribe":
				if claims.SubjectKind == networkauth.SubjectKindClient && strings.TrimSpace(req.TargetNode) != "" {
					coord.AddTunnel(peerID, peer.StablePrincipalUUID(claims.NetworkID, networkauth.SubjectKindNode, strings.TrimSpace(req.TargetNode)))
				}
			}
		}
	})

	relaySrv := newIPv4TestServer(t, mux)

	peerServer := &peer.Server{
		Sessions: manager,
		NodeName: nodeName,
		AuthorizePeer: func(_ context.Context, runtimeCredential string) (*peer.AuthenticatedPeer, error) {
			claims, err := networkauth.VerifyRuntimeCredential(runtimeCredential, state.Bundle(time.Now().UTC(), time.Hour), time.Now().UTC())
			if err != nil {
				return nil, err
			}
			return &peer.AuthenticatedPeer{
				NetworkID:   claims.NetworkID,
				SubjectKind: claims.SubjectKind,
				SubjectID:   claims.SubjectID,
			}, nil
		},
	}
	nodeToken, _, err := networkauth.SignRuntimeCredential(state, networkauth.SubjectKindNode, nodeName, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignRuntimeCredential(node): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tailnetConn, err := peer.StartNodeTailnetListener(ctx, relaySrv.URL, nodeToken, peerServer)
	if err != nil {
		t.Fatalf("StartNodeTailnetListener: %v", err)
	}
	t.Cleanup(func() { _ = tailnetConn.Close() })

	return state, manager, sessionID, relaySrv
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func mustDERPMap(t *testing.T, relayNetwork, host string) *tailcfg.DERPMap {
	t.Helper()
	_ = relayNetwork
	dm, err := peer.NewDERPMapFromRelayURL("http://" + host)
	if err != nil {
		t.Fatalf("NewDERPMapFromRelayURL: %v", err)
	}
	return dm
}

func saveRelayConfig(t *testing.T, relayURL, relaySession, relayNetwork string) {
	t.Helper()
	t.Setenv("CODEWIRE_API_KEY", relaySession)

	cfg := &cwconfig.Config{
		RelayURL: &relayURL,
	}
	if relayNetwork != "" {
		cfg.RelayNetwork = &relayNetwork
	}
	if err := cwconfig.SaveConfig(dataDir(), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

func withTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("Setenv HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })

	oldServer := serverFlag
	oldToken := tokenFlag
	serverFlag = ""
	tokenFlag = ""
	t.Cleanup(func() {
		serverFlag = oldServer
		tokenFlag = oldToken
	})
}

func TestMsgCmdRejectsFromForRemotePeerServer(t *testing.T) {
	withTestHome(t)

	cmd := msgCmd()
	if err := cmd.Flags().Set("from", "other-node:planner"); err != nil {
		t.Fatalf("Set from: %v", err)
	}

	err := cmd.RunE(cmd, []string{"dev-2:coder", "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sender session must be owned by the current local node") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMsgCmdRoutesRemoteLocatorViaRelayDiscovery(t *testing.T) {
	t.Skip("covered by TestRelayNetworkMessagingThreeSessionsKind; the lightweight httptest relay harness does not produce a reliably dialable tailnet peer for message routing")
	withTestHome(t)

	relaySession := "relay-session"
	relayNetwork := "project-alpha"
	_, manager, sessionID, relaySrv := startRuntimeTailnetRelayNode(t, relayNetwork, relaySession, "dev-2", "coder")

	saveRelayConfig(t, relaySrv.URL, relaySession, relayNetwork)

	cmd := msgCmd()
	if err := cmd.RunE(cmd, []string{"dev-2:coder", "hello over relay discovery"}); err != nil {
		t.Fatalf("msg command failed: %v", err)
	}

	messages, err := manager.ReadMessages(sessionID, 10)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if !strings.Contains(string(messages[0].Data), "hello over relay discovery") {
		t.Fatalf("unexpected message payload: %s", string(messages[0].Data))
	}
}

func TestMsgCmdRoutesRemoteLocatorViaRelayRuntimeCredential(t *testing.T) {
	t.Skip("covered by TestRelayNetworkMessagingThreeSessionsKind; the lightweight httptest relay harness does not produce a reliably dialable tailnet peer for message routing")
	withTestHome(t)

	_, manager, sessionID, relaySrv := startRuntimeTailnetRelayNode(t, "project-alpha", "relay-session", "dev-2", "coder")

	relayURL := relaySrv.URL
	relaySession := "relay-session"
	relayNetwork := "project-alpha"
	t.Setenv("CODEWIRE_API_KEY", relaySession)
	if err := cwconfig.SaveConfig(dataDir(), &cwconfig.Config{
		RelayURL:     &relayURL,
		RelayNetwork: &relayNetwork,
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	cmd := msgCmd()
	if err := cmd.RunE(cmd, []string{"dev-2:coder", "hello over runtime credential"}); err != nil {
		t.Fatalf("msg command failed: %v", err)
	}

	messages, err := manager.ReadMessages(sessionID, 10)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if !strings.Contains(string(messages[0].Data), "hello over runtime credential") {
		t.Fatalf("unexpected message payload: %s", string(messages[0].Data))
	}
}

func TestRequestCmdRoutesRemoteLocatorViaRelayDiscovery(t *testing.T) {
	t.Skip("covered by TestRelayNetworkMessagingThreeSessionsKind; the lightweight httptest relay harness is not a faithful model for tailnet request/reply")
	withTestHome(t)

	relaySession := "relay-session"
	_, manager, sessionID, relaySrv := startRuntimeTailnetRelayNode(t, "default", relaySession, "dev-2", "coder")

	saveRelayConfig(t, relaySrv.URL, relaySession, "")

	sub := manager.Subscriptions.Subscribe(&sessionID, nil, []session.EventType{session.EventRequest})
	t.Cleanup(func() { manager.Subscriptions.Unsubscribe(sub.ID) })
	go func() {
		select {
		case se := <-sub.Ch:
			if se.Event.Type != session.EventRequest {
				return
			}
			var req session.RequestData
			if err := json.Unmarshal(se.Event.Data, &req); err != nil {
				return
			}
			_ = manager.SendReply(sessionID, req.RequestID, "approved remotely")
		case <-time.After(10 * time.Second):
		}
	}()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	cmd := requestCmd()
	if err := cmd.RunE(cmd, []string{"dev-2:coder", "deploy now?"}); err != nil {
		t.Fatalf("request command failed: %v", err)
	}

	_ = w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(output), "approved remotely") {
		t.Fatalf("unexpected output: %q", string(output))
	}
}

func TestRequestCmdRejectsFromForRemotePeerRequest(t *testing.T) {
	withTestHome(t)

	cmd := requestCmd()
	if err := cmd.Flags().Set("from", "other-node:planner"); err != nil {
		t.Fatalf("Set from: %v", err)
	}

	err := cmd.RunE(cmd, []string{"dev-2:coder", "deploy now?"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sender session must be owned by the current local node") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplyCmdRejectsRemoteLocatorForRemoteSender(t *testing.T) {
	withTestHome(t)

	cmd := replyCmd()
	if err := cmd.Flags().Set("from", "other-node:coder"); err != nil {
		t.Fatalf("Set from: %v", err)
	}
	err := cmd.RunE(cmd, []string{"req_test", "done"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "are not allowed for reply") {
		t.Fatalf("unexpected error: %v", err)
	}
}
