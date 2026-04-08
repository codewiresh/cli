package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"strings"

	"nhooyr.io/websocket"

	"github.com/codewiresh/codewire/internal/client"
	"github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/connection"
	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/peerclient"
	"github.com/codewiresh/codewire/internal/protocol"
)

type relayNodeRecord struct {
	Name      string `json:"name"`
	PeerURL   string `json:"peer_url,omitempty"`
	Connected bool   `json:"connected"`
}

func normalizeSessionLocatorForCurrentNode(locator sessionLocator) (sessionLocator, error) {
	if !locator.isRemote() {
		return locator, nil
	}

	cfg, err := config.LoadConfig(dataDir())
	if err != nil {
		return locator, fmt.Errorf("loading config: %w", err)
	}
	if strings.TrimSpace(locator.Node) == strings.TrimSpace(cfg.Node.Name) {
		locator.Node = ""
	}
	return locator, nil
}

func lookupRelayNode(nodeName string) (relayNodeRecord, *config.Config, error) {
	cfg, err := config.LoadConfig(dataDir())
	if err != nil {
		return relayNodeRecord{}, nil, fmt.Errorf("loading config: %w", err)
	}
	relayURL, authToken, networkID, err := client.LoadRelayAuth(dataDir(), client.RelayAuthOptions{})
	if err != nil {
		return relayNodeRecord{}, nil, err
	}

	requestURL := strings.TrimRight(strings.TrimSpace(relayURL), "/") + "/api/v1/nodes"
	if strings.TrimSpace(networkID) != "" {
		requestURL += "?network_id=" + neturl.QueryEscape(strings.TrimSpace(networkID))
	}

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return relayNodeRecord{}, nil, fmt.Errorf("building relay node discovery request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(authToken))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return relayNodeRecord{}, nil, fmt.Errorf("querying relay for node %q: %w", nodeName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return relayNodeRecord{}, nil, fmt.Errorf("relay node discovery returned HTTP %d", resp.StatusCode)
	}

	var nodes []relayNodeRecord
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return relayNodeRecord{}, nil, fmt.Errorf("decoding relay node discovery response: %w", err)
	}

	for _, node := range nodes {
		if strings.TrimSpace(node.Name) != nodeName {
			continue
		}
		return node, cfg, nil
	}

	return relayNodeRecord{}, nil, fmt.Errorf("remote node %q was not found in the current relay network", nodeName)
}

func dialPeerClientForNode(ctx context.Context, nodeName string) (*peerclient.Client, func(), error) {
	node, cfg, err := lookupRelayNode(nodeName)
	if err != nil {
		return nil, nil, err
	}
	if !node.Connected {
		return nil, nil, fmt.Errorf("remote node %q is registered in the current relay network but is not connected", nodeName)
	}

	runtimeCred, credErr := issueRuntimeCredentialForPeer(ctx)
	if credErr != nil {
		return nil, nil, credErr
	}

	tcpConn, tailnetConn, err := peer.DialNetworkPeerTCP(ctx, *cfg.RelayURL, runtimeCred, nodeName, peer.TailnetPeerPort)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to peer node %q over tailnet: %w", nodeName, err)
	}

	client := peerclient.New(tcpConn)
	if err := client.Authenticate(ctx, runtimeCred); err != nil {
		_ = client.Close()
		_ = tailnetConn.Close()
		return nil, nil, fmt.Errorf("authenticating peer connection for node %q: %w", nodeName, err)
	}
	return client, func() {
		_ = client.Close()
		_ = tailnetConn.Close()
	}, nil
}

func issueRuntimeCredentialForPeer(ctx context.Context) (string, error) {
	cfg, err := config.LoadConfig(dataDir())
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	if cfg.RelayURL == nil || strings.TrimSpace(*cfg.RelayURL) == "" {
		return "", fmt.Errorf("relay not configured (set CODEWIRE_RELAY_URL or log in to hosted Codewire)")
	}
	relayURL := strings.TrimSpace(*cfg.RelayURL)

	var clientErr error
	if authToken := config.ResolveRelayUserAuthToken(relayURL); authToken != "" {
		networkID := ""
		if cfg.RelaySelectedNetwork != nil {
			networkID = strings.TrimSpace(*cfg.RelaySelectedNetwork)
		}
		// Try client credential (session token / API key) first.
		issued, err := networkauth.IssueClientRuntimeCredential(ctx, http.DefaultClient, relayURL, authToken, networkID)
		if err == nil {
			return issued.Credential, nil
		}
		clientErr = err
	}

	// Fall back to node credential from local node enrollment.
	_, nodeToken, _, err := client.LoadRelayNodeAuth(dataDir())
	if err != nil {
		if clientErr != nil {
			return "", fmt.Errorf("runtime credential: client auth failed: %v; node auth unavailable: %w", clientErr, err)
		}
		return "", err
	}
	issued, err := networkauth.IssueNodeRuntimeCredential(ctx, http.DefaultClient, relayURL, nodeToken)
	if err != nil {
		if clientErr != nil {
			return "", fmt.Errorf("runtime credential: client and node auth both failed: client=%v node=%w", clientErr, err)
		}
		return "", fmt.Errorf("runtime credential: node auth failed: %w", err)
	}
	return issued.Credential, nil
}

func currentNodeName() (string, error) {
	cfg, err := config.LoadConfig(dataDir())
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	return cfg.Node.Name, nil
}

func issueRemoteSenderDelegation(target *client.Target, fromLocator sessionLocator, verb, audienceNode string) (*peer.SessionLocator, string, error) {
	fromValue := fromLocator.Name
	if fromLocator.ID != nil {
		fromValue = fmt.Sprintf("%d", *fromLocator.ID)
	}
	resolvedID, err := client.ResolveSessionArg(target, fromValue)
	if err != nil {
		return nil, "", err
	}

	senderCap, issuedID, issuedName, err := client.IssueSenderDelegation(target, &resolvedID, "", verb, audienceNode)
	if err != nil {
		return nil, "", err
	}

	nodeName, err := currentNodeName()
	if err != nil {
		return nil, "", err
	}

	locator := &peer.SessionLocator{Node: nodeName}
	switch {
	case issuedID != nil:
		id := *issuedID
		locator.ID = &id
	case issuedName != "":
		locator.Name = issuedName
	default:
		id := resolvedID
		locator.ID = &id
	}
	return locator, senderCap, nil
}

func toPeerSessionLocator(locator sessionLocator) peer.SessionLocator {
	result := peer.SessionLocator{
		Node: locator.Node,
		Name: locator.Name,
	}
	if locator.ID != nil {
		id := *locator.ID
		result.ID = &id
		result.Name = ""
	}
	return result
}

func resolveRemoteDelivery(delivery string) string {
	if delivery == "auto" {
		return "inbox"
	}
	return delivery
}

func resolveObserverGrant(locator sessionLocator, verb, explicitGrant string) (string, error) {
	if strings.TrimSpace(explicitGrant) != "" {
		return strings.TrimSpace(explicitGrant), nil
	}
	_, _, networkID, err := client.LoadRelayAuth(dataDir(), client.RelayAuthOptions{})
	if err != nil {
		return "", err
	}
	return client.ResolveAcceptedAccessGrant(dataDir(), networkID, locator.Node, locator.ID, locator.Name, verb)
}

func printMessageResponses(messages []protocol.MessageResponse) {
	if len(messages) == 0 {
		fmt.Println("No messages")
		return
	}

	for _, m := range messages {
		fromLabel := fmt.Sprintf("%d", m.From)
		if m.FromName != "" {
			fromLabel = m.FromName
		}
		toLabel := fmt.Sprintf("%d", m.To)
		if m.ToName != "" {
			toLabel = m.ToName
		}

		switch m.EventType {
		case "message.request":
			fmt.Printf("[%s] REQUEST %s -> %s (req=%s): %s\n", m.Timestamp, fromLabel, toLabel, m.RequestID, m.Body)
		case "message.reply":
			fmt.Printf("[%s] REPLY %s (req=%s): %s\n", m.Timestamp, fromLabel, m.RequestID, m.Body)
		default:
			fmt.Printf("[%s] %s -> %s: %s\n", m.Timestamp, fromLabel, toLabel, m.Body)
		}
	}
}

func printRequestReplyResult(rawOutput bool, result *peerclient.RequestResult) {
	if rawOutput {
		fmt.Println(result.ReplyBody)
		return
	}

	fromLabel := "unknown"
	if result != nil && result.From != nil {
		if result.From.Name != "" {
			fromLabel = result.From.Name
		} else if result.From.ID != nil {
			fromLabel = fmt.Sprintf("%d", *result.From.ID)
		}
	}
	fmt.Printf("[reply from %s] %s\n", fromLabel, result.ReplyBody)
}

func printSessionEvent(event *protocol.SessionEvent) {
	switch event.EventType {
	case "direct.message":
		var d struct {
			From     uint32 `json:"from"`
			FromName string `json:"from_name"`
			To       uint32 `json:"to"`
			ToName   string `json:"to_name"`
			Body     string `json:"body"`
		}
		if json.Unmarshal(event.Data, &d) != nil {
			return
		}
		fromLabel := fmt.Sprintf("%d", d.From)
		if d.FromName != "" {
			fromLabel = d.FromName
		}
		toLabel := fmt.Sprintf("%d", d.To)
		if d.ToName != "" {
			toLabel = d.ToName
		}
		fmt.Printf("[%s -> %s] %s\n", fromLabel, toLabel, d.Body)
	case "message.request":
		var d struct {
			RequestID string `json:"request_id"`
			From      uint32 `json:"from"`
			FromName  string `json:"from_name"`
			To        uint32 `json:"to"`
			ToName    string `json:"to_name"`
			Body      string `json:"body"`
		}
		if json.Unmarshal(event.Data, &d) != nil {
			return
		}
		fromLabel := fmt.Sprintf("%d", d.From)
		if d.FromName != "" {
			fromLabel = d.FromName
		}
		toLabel := fmt.Sprintf("%d", d.To)
		if d.ToName != "" {
			toLabel = d.ToName
		}
		fmt.Printf("[%s -> %s] REQUEST (%s): %s\n", fromLabel, toLabel, d.RequestID, d.Body)
	case "message.reply":
		var d struct {
			RequestID string `json:"request_id"`
			From      uint32 `json:"from"`
			FromName  string `json:"from_name"`
			Body      string `json:"body"`
		}
		if json.Unmarshal(event.Data, &d) != nil {
			return
		}
		fromLabel := fmt.Sprintf("%d", d.From)
		if d.FromName != "" {
			fromLabel = d.FromName
		}
		fmt.Printf("[%s] REPLY (%s): %s\n", fromLabel, d.RequestID, d.Body)
	}
}

// attachRemoteSession attaches to a session on a remote node via tailnet.
func attachRemoteSession(ctx context.Context, loc sessionLocator, noHistory bool) error {
	node, cfg, err := lookupRelayNode(loc.Node)
	if err != nil {
		return err
	}
	if !node.Connected {
		return fmt.Errorf("remote node %q is not connected", loc.Node)
	}

	runtimeCred, err := issueRuntimeCredentialForPeer(ctx)
	if err != nil {
		return err
	}

	// Dial tailnet TCP to the remote node.
	tcpConn, tailnetConn, err := peer.DialNetworkPeerTCP(ctx, *cfg.RelayURL, runtimeCred, loc.Node, peer.TailnetPeerPort)
	if err != nil {
		return fmt.Errorf("connecting to node %q over tailnet: %w", loc.Node, err)
	}
	defer tailnetConn.Close()

	// Upgrade to WebSocket over the tailnet TCP connection, hitting /ws.
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return tcpConn, nil
			},
		},
	}
	wsConn, _, err := websocket.Dial(ctx, "ws://tailnet/ws", &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + runtimeCred},
		},
	})
	if err != nil {
		return fmt.Errorf("websocket upgrade to node %q: %w", loc.Node, err)
	}
	defer wsConn.CloseNow()

	reader := connection.NewWSReader(ctx, wsConn)
	writer := connection.NewWSWriter(ctx, wsConn)

	// Resolve session ID.
	var sessionID uint32
	if loc.ID != nil {
		sessionID = *loc.ID
	} else if loc.Name != "" {
		resolved, err := client.ResolveSessionArgConn(reader, writer, loc.Name)
		if err != nil {
			return fmt.Errorf("resolving session %q on node %q: %w", loc.Name, loc.Node, err)
		}
		sessionID = resolved
	} else {
		return fmt.Errorf("no session ID or name specified")
	}

	return client.AttachConn(reader, writer, sessionID, noHistory)
}
