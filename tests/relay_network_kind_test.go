//go:build integration && kind

package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/client"
	"github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/node"
	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/peerclient"
	"github.com/codewiresh/codewire/internal/protocol"
	"github.com/codewiresh/codewire/internal/relay"
)

type relayDiscoveredNode struct {
	NetworkID string `json:"network_id,omitempty"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

type runningRelayNode struct {
	name    string
	dataDir string
	target  *client.Target
	cancel  context.CancelFunc
	done    chan error
}

func TestRelayNetworkMessagingThreeSessionsKind(t *testing.T) {
	relayURL := strings.TrimSpace(os.Getenv("CODEWIRE_RELAY_TEST_URL"))
	relayToken := strings.TrimSpace(os.Getenv("CODEWIRE_RELAY_TEST_TOKEN"))
	if relayURL == "" || relayToken == "" {
		t.Skip("set CODEWIRE_RELAY_TEST_URL and CODEWIRE_RELAY_TEST_TOKEN to run the kind relay test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	networkID := fmt.Sprintf("kind-msg-%d", time.Now().UnixNano())
	adminDir := filepath.Join(tempDir(t, "relay-admin"), ".codewire")
	if err := client.CreateNetwork(adminDir, networkID, client.RelayAuthOptions{
		RelayURL:  relayURL,
		AuthToken: relayToken,
	}, false); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	node1 := startRelayKindNode(t, ctx, relayURL, relayToken, networkID, "dev-1")
	node2 := startRelayKindNode(t, ctx, relayURL, relayToken, networkID, "dev-2")

	waitForConnectedRelayNodes(t, ctx, relayURL, relayToken, networkID, "dev-1", "dev-2")

	plannerID := launchSleepSession(t, node1.target, "planner")
	coderID := launchSleepSession(t, node2.target, "coder")
	reviewerID := launchSleepSession(t, node2.target, "reviewer")
	if plannerID == 0 || coderID == 0 || reviewerID == 0 {
		t.Fatal("expected non-zero session IDs")
	}

	// Session-authored remote message from planner -> coder.
	msgConn, closeMsgConn := dialTailnetPeerNode(t, ctx, relayURL, relayToken, networkID, "dev-2")
	defer closeMsgConn()

	msgSenderCap, msgFromID, msgFromName := issueSenderDelegation(t, node1.target, "planner", "msg", "dev-2")
	msgFrom := senderLocator("dev-1", msgFromID, msgFromName)
	msgID, err := peerclient.Msg(ctx, msgConn, msgFrom, msgSenderCap, peer.SessionLocator{Name: "coder"}, "hello from planner", "inbox")
	if err != nil {
		t.Fatalf("peerclient.Msg: %v", err)
	}
	if strings.TrimSpace(msgID) == "" {
		t.Fatal("expected non-empty message id")
	}

	coderInbox := waitForInboxBody(t, ctx, node2.target, coderID, "direct.message", "hello from planner")
	if coderInbox.FromName != "dev-1:planner" {
		t.Fatalf("coder inbox FromName = %q, want dev-1:planner", coderInbox.FromName)
	}

	// Remote request from planner -> coder, reviewer must not be able to reply.
	reqConn, closeReqConn := dialTailnetPeerNode(t, ctx, relayURL, relayToken, networkID, "dev-2")
	defer closeReqConn()

	requestSenderCap, requestFromID, requestFromName := issueSenderDelegation(t, node1.target, "planner", "request", "dev-2")
	requestFrom := senderLocator("dev-1", requestFromID, requestFromName)
	requestResultCh := make(chan *peerclient.RequestResult, 1)
	requestErrCh := make(chan error, 1)
	go func() {
		res, err := peerclient.Request(ctx, reqConn, requestFrom, requestSenderCap, peer.SessionLocator{Name: "coder"}, "ready for review?", 20, "inbox")
		if err != nil {
			requestErrCh <- err
			return
		}
		requestResultCh <- res
	}()

	requestMsg := waitForInboxBody(t, ctx, node2.target, coderID, "message.request", "ready for review?")
	if strings.TrimSpace(requestMsg.RequestID) == "" {
		t.Fatal("expected non-empty request id in coder inbox")
	}
	if requestMsg.FromName != "dev-1:planner" {
		t.Fatalf("request FromName = %q, want dev-1:planner", requestMsg.FromName)
	}

	err = client.Reply(node2.target, &reviewerID, requestMsg.RequestID, "", "reviewer should be rejected")
	if err == nil {
		t.Fatal("expected reviewer reply to be rejected")
	}
	if !strings.Contains(err.Error(), "may only be replied to by session") {
		t.Fatalf("unexpected reviewer reply error: %v", err)
	}

	if err := client.Reply(node2.target, &coderID, requestMsg.RequestID, "", "approved"); err != nil {
		t.Fatalf("coder reply: %v", err)
	}

	select {
	case err := <-requestErrCh:
		t.Fatalf("peerclient.Request: %v", err)
	case res := <-requestResultCh:
		if res.ReplyBody != "approved" {
			t.Fatalf("reply body = %q, want approved", res.ReplyBody)
		}
		if res.From == nil || res.From.Name != "coder" {
			t.Fatalf("reply sender = %#v, want coder", res.From)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for request result")
	}

	// Keep the third session active in the scenario and verify it remains isolated.
	reviewerInbox := mustReadInbox(t, node2.target, reviewerID, 10)
	for _, msg := range reviewerInbox {
		if msg.Body == "ready for review?" || msg.Body == "hello from planner" {
			t.Fatalf("reviewer inbox should not receive coder-targeted traffic, got %+v", reviewerInbox)
		}
	}
}

func startRelayKindNode(t *testing.T, parent context.Context, relayURL, relayToken, networkID, nodeName string) *runningRelayNode {
	t.Helper()

	dataDir := filepath.Join(tempDir(t, "relay-"+nodeName), ".codewire")

	cfg := &config.Config{
		Node: config.NodeConfig{
			Name: nodeName,
		},
	}
	if err := config.SaveConfig(dataDir, cfg); err != nil {
		t.Fatalf("SaveConfig(%s): %v", nodeName, err)
	}

	if err := relay.RunSetup(parent, relay.SetupOptions{
		RelayURL:  relayURL,
		DataDir:   dataDir,
		NetworkID: networkID,
		AuthToken: relayToken,
	}); err != nil {
		t.Fatalf("RunSetup(%s): %v", nodeName, err)
	}

	n, err := node.NewNode(dataDir)
	if err != nil {
		t.Fatalf("NewNode(%s): %v", nodeName, err)
	}

	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		done <- n.Run(ctx)
	}()

	target := &client.Target{Local: dataDir}
	waitForLocalNodeReady(t, ctx, dataDir, done)

	t.Cleanup(func() {
		_ = client.KillAll(target)
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				t.Logf("node %s stopped with: %v", nodeName, err)
			}
		case <-time.After(5 * time.Second):
			t.Logf("timeout waiting for node %s shutdown", nodeName)
		}
	})

	return &runningRelayNode{
		name:    nodeName,
		dataDir: dataDir,
		target:  target,
		cancel:  cancel,
		done:    done,
	}
}

func waitForLocalNodeReady(t *testing.T, ctx context.Context, dataDir string, done <-chan error) {
	t.Helper()
	sockPath := filepath.Join(dataDir, "codewire.sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case err := <-done:
			if err == nil || err == context.Canceled {
				t.Fatalf("node exited before becoming ready")
			}
			t.Fatalf("node exited before becoming ready: %v", err)
		case <-ctx.Done():
			t.Fatalf("node did not become ready before context cancellation: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("timeout waiting for local node to become ready")
}

func waitForConnectedRelayNodes(t *testing.T, ctx context.Context, relayURL, relayToken, networkID string, names ...string) map[string]relayDiscoveredNode {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		nodes := listRelayNodes(t, relayURL, relayToken, networkID)
		ready := true
		for _, name := range names {
			node, ok := nodes[name]
			if !ok || !node.Connected {
				ready = false
				break
			}
		}
		if ready {
			return nodes
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for relay nodes: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatalf("timeout waiting for relay nodes %v to connect", names)
	return nil
}

func listRelayNodes(t *testing.T, relayURL, relayToken, networkID string) map[string]relayDiscoveredNode {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, relayURL+"/api/v1/nodes?network_id="+networkID, nil)
	if err != nil {
		t.Fatalf("NewRequest(list nodes): %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+relayToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list relay nodes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list relay nodes status = %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var nodes []relayDiscoveredNode
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		t.Fatalf("Decode relay nodes: %v", err)
	}

	result := make(map[string]relayDiscoveredNode, len(nodes))
	for _, node := range nodes {
		result[node.Name] = node
	}
	return result
}

func issueRuntimeCredential(t *testing.T, ctx context.Context, relayURL, relayToken, networkID string) string {
	t.Helper()
	issued, err := networkauth.IssueClientRuntimeCredential(ctx, http.DefaultClient, relayURL, relayToken, networkID)
	if err != nil {
		t.Fatalf("IssueClientRuntimeCredential: %v", err)
	}
	return issued.Credential
}

func issueSenderDelegation(t *testing.T, target *client.Target, sessionName, verb, audienceNode string) (string, *uint32, string) {
	t.Helper()
	senderCap, fromID, fromName, err := client.IssueSenderDelegation(target, nil, sessionName, verb, audienceNode)
	if err != nil {
		t.Fatalf("IssueSenderDelegation(%s,%s): %v", sessionName, verb, err)
	}
	return senderCap, fromID, fromName
}

func senderLocator(node string, id *uint32, name string) *peer.SessionLocator {
	locator := &peer.SessionLocator{Node: node}
	if id != nil {
		locator.ID = id
		return locator
	}
	locator.Name = name
	return locator
}

func dialTailnetPeerNode(t *testing.T, ctx context.Context, relayURL, relayToken, networkID, nodeName string) (*peerclient.Client, func()) {
	t.Helper()
	runtimeCred := issueRuntimeCredential(t, ctx, relayURL, relayToken, networkID)
	tcpConn, tailnetConn, err := peer.DialNetworkPeerTCP(ctx, relayURL, runtimeCred, nodeName, peer.TailnetPeerPort)
	if err != nil {
		t.Fatalf("DialNetworkPeerTCP(%s): %v", nodeName, err)
	}
	client := peerclient.New(tcpConn)
	if err := client.Authenticate(ctx, runtimeCred); err != nil {
		t.Fatalf("Authenticate(%s): %v", nodeName, err)
	}
	return client, func() {
		_ = client.Close()
		_ = tailnetConn.Close()
	}
}

func launchSleepSession(t *testing.T, target *client.Target, name string) uint32 {
	t.Helper()
	if err := client.Run(target, []string{"sh", "-lc", "sleep 300"}, target.Local, name, nil, nil); err != nil {
		t.Fatalf("Run(%s): %v", name, err)
	}
	return waitForSessionID(t, target, name)
}

func waitForSessionID(t *testing.T, target *client.Target, name string) uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		id, err := client.ResolveSessionArg(target, name)
		if err == nil {
			return id
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout resolving session %q", name)
	return 0
}

func mustReadInbox(t *testing.T, target *client.Target, sessionID uint32, tail int) []protocol.MessageResponse {
	t.Helper()
	tailU := uint(tail)
	resp, err := requestResponseForTarget(target, &protocol.Request{
		Type: "MsgRead",
		ID:   &sessionID,
		Tail: &tailU,
	})
	if err != nil {
		t.Fatalf("MsgRead(%d): %v", sessionID, err)
	}
	if resp.Type != "MsgReadResult" {
		t.Fatalf("MsgRead(%d) response type = %q", sessionID, resp.Type)
	}
	if resp.Messages == nil {
		return nil
	}
	return *resp.Messages
}

func waitForInboxBody(t *testing.T, ctx context.Context, target *client.Target, sessionID uint32, eventType, body string) protocol.MessageResponse {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		messages := mustReadInbox(t, target, sessionID, 20)
		for _, msg := range messages {
			if msg.EventType == eventType && msg.Body == body {
				return msg
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for inbox body %q: %v", body, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatalf("timeout waiting for inbox body %q on session %d", body, sessionID)
	return protocol.MessageResponse{}
}

func requestResponseForTarget(target *client.Target, req *protocol.Request) (*protocol.Response, error) {
	reader, writer, err := target.Connect()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	defer writer.Close()

	if err := writer.SendRequest(req); err != nil {
		return nil, err
	}

	frame, err := reader.ReadFrame()
	if err != nil {
		return nil, err
	}
	if frame == nil {
		return nil, fmt.Errorf("connection closed before response")
	}
	if frame.Type != protocol.FrameControl {
		return nil, fmt.Errorf("expected control frame, got type 0x%02x", frame.Type)
	}

	var resp protocol.Response
	if err := json.Unmarshal(frame.Payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
