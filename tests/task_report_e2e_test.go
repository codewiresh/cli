//go:build integration

package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	cwclient "github.com/codewiresh/codewire/internal/client"
	cwconfig "github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/mcp"
	"github.com/codewiresh/codewire/internal/protocol"
	localrelay "github.com/codewiresh/codewire/internal/relay"
	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

var mcpServerStdioMu sync.Mutex

func TestTaskReportEndToEndViaMCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	jsServer := startJetStreamServer(t)
	defer func() {
		jsServer.Shutdown()
		jsServer.WaitForShutdown()
	}()

	httpAddr := reserveTCPAddr(t)
	sshAddr := reserveTCPAddr(t)
	relayURL := "http://" + httpAddr
	const (
		adminToken = "dev-secret"
		networkID  = "project_alpha"
		nodeName   = "builder"
		summary    = "ship task events end to end"
		state      = "working"
	)

	go func() {
		_ = localrelay.RunRelay(ctx, localrelay.RelayConfig{
			BaseURL:       relayURL,
			ListenAddr:    httpAddr,
			SSHListenAddr: sshAddr,
			DataDir:       t.TempDir(),
			AuthMode:      "token",
			AuthToken:     adminToken,
			NATSURL:       jsServer.ClientURL(),
		})
	}()
	waitForRelay(t, relayURL, adminToken)

	if err := createNetwork(t, relayURL, adminToken, networkID); err != nil {
		t.Fatalf("createNetwork: %v", err)
	}

	t.Setenv("CODEWIRE_RELAY_AUTH_TOKEN", adminToken)

	nodeDir := t.TempDir()
	if err := cwconfig.SaveConfig(nodeDir, &cwconfig.Config{
		Node: cwconfig.NodeConfig{Name: nodeName},
		RelayURL: func() *string {
			v := relayURL
			return &v
		}(),
		RelaySelectedNetwork: func() *string {
			v := networkID
			return &v
		}(),
	}); err != nil {
		t.Fatalf("SaveConfig(node): %v", err)
	}

	sock := startTestNode(t, nodeDir)
	waitForNodeConnected(t, relayURL, adminToken, networkID, nodeName)

	launchResp := requestResponse(t, sock, &protocol.Request{
		Type:       "Launch",
		Name:       "planner",
		Command:    []string{"bash", "-c", "sleep 60"},
		WorkingDir: "/tmp",
	})
	if launchResp.Type != "Launched" || launchResp.ID == nil {
		t.Fatalf("Launch response = %#v", launchResp)
	}
	sessionID := *launchResp.ID

	clientDir := t.TempDir()
	if err := cwconfig.SaveConfig(clientDir, &cwconfig.Config{
		RelayURL: func() *string {
			v := relayURL
			return &v
		}(),
		RelaySelectedNetwork: func() *string {
			v := networkID
			return &v
		}(),
	}); err != nil {
		t.Fatalf("SaveConfig(client): %v", err)
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	events := make(chan cwclient.TaskEvent, 8)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- cwclient.WatchTasks(watchCtx, clientDir, cwclient.RelayAuthOptions{
			AuthToken: adminToken,
		}, cwclient.WatchTasksOptions{
			NetworkID: networkID,
		}, events)
	}()
	time.Sleep(200 * time.Millisecond)

	gotText, err := callMCPReportTask(nodeDir, sessionID, summary, state)
	if err != nil {
		t.Fatalf("callMCPReportTask: %v", err)
	}
	if gotText != fmt.Sprintf("Reported task for session %d", sessionID) {
		t.Fatalf("mcp result = %q", gotText)
	}

	var watched cwclient.TaskEvent
	select {
	case watched = <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for watched task event")
	}
	if watched.Type != "task.report" {
		t.Fatalf("watch type = %q", watched.Type)
	}
	if watched.NetworkID != networkID || watched.NodeName != nodeName {
		t.Fatalf("watch event = %#v", watched)
	}
	if watched.SessionID != sessionID || watched.State != state || watched.Summary != summary {
		t.Fatalf("watch payload = %#v", watched)
	}
	watchCancel()
	if err := <-watchDone; err != nil {
		t.Fatalf("WatchTasks: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, err := cwclient.ListTasks(clientDir, cwclient.RelayAuthOptions{
			AuthToken: adminToken,
		}, cwclient.WatchTasksOptions{
			NetworkID: networkID,
		})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(snapshots) == 1 {
			got := snapshots[0]
			if got.NodeName != nodeName || got.SessionID != sessionID {
				t.Fatalf("snapshot = %#v", got)
			}
			if got.State != state || got.Summary != summary {
				t.Fatalf("snapshot payload = %#v", got)
			}
			if got.EventID == "" || got.StreamSeq == 0 {
				t.Fatalf("snapshot metadata = %#v", got)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for task snapshot")
}

func startJetStreamServer(t *testing.T) *natssrv.Server {
	t.Helper()

	opts := &natssrv.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := natssrv.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(3 * time.Second) {
		t.Fatal("jetstream server did not become ready")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(srv.ClientURL(), nats.Timeout(time.Second))
		if err == nil {
			nc.Close()
			return srv
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("jetstream server did not accept client connections")
	return nil
}

func reserveTCPAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(tcp4): %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForRelay(t *testing.T, relayURL, adminToken string) {
	t.Helper()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, relayURL+"/api/v1/auth/validate", nil)
		if err != nil {
			t.Fatalf("NewRequest(validate): %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("relay did not become ready at %s", relayURL)
}

func createNetwork(t *testing.T, relayURL, adminToken, networkID string) error {
	t.Helper()

	body := bytes.NewBufferString(fmt.Sprintf(`{"network_id":%q}`, networkID))
	req, err := http.NewRequest(http.MethodPost, relayURL+"/api/v1/networks", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("create network failed (%d): %s", resp.StatusCode, string(data))
	}
	return nil
}

func waitForNodeConnected(t *testing.T, relayURL, adminToken, networkID, nodeName string) {
	t.Helper()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, relayURL+"/api/v1/nodes?network_id="+networkID, nil)
		if err != nil {
			t.Fatalf("NewRequest(nodes): %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := client.Do(req)
		if err == nil {
			var nodes []struct {
				Name      string `json:"name"`
				Connected bool   `json:"connected"`
			}
			if resp.StatusCode == http.StatusOK {
				if err := json.NewDecoder(resp.Body).Decode(&nodes); err == nil {
					_ = resp.Body.Close()
					for _, n := range nodes {
						if n.Name == nodeName && n.Connected {
							return
						}
					}
				}
			}
			_ = resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("node %q did not connect to relay", nodeName)
}

func callMCPReportTask(dataDir string, sessionID uint32, summary, state string) (string, error) {
	mcpServerStdioMu.Lock()
	defer mcpServerStdioMu.Unlock()

	inR, inW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer inR.Close()

	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inW.Close()
		return "", err
	}
	defer outR.Close()

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	os.Stdin = inR
	os.Stdout = outW
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- mcp.RunMCPServer(dataDir)
		_ = outW.Close()
	}()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codewire_report_task",
			"arguments": map[string]any{
				"session_id": sessionID,
				"summary":    summary,
				"state":      state,
			},
		},
	}
	if err := json.NewEncoder(inW).Encode(req); err != nil {
		_ = inW.Close()
		return "", err
	}
	_ = inW.Close()

	scanner := bufio.NewScanner(outR)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("mcp server produced no response")
	}

	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("%s", resp.Error.Message)
	}
	if len(resp.Result.Content) == 0 {
		return "", fmt.Errorf("mcp response missing content")
	}
	if err := <-errCh; err != nil {
		return "", err
	}
	return resp.Result.Content[0].Text, nil
}
