package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/codewiresh/codewire/internal/client"
	"github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/peerclient"
	"github.com/codewiresh/codewire/internal/protocol"
	"nhooyr.io/websocket"
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

func resolvePeerServer(nodeName string) (config.ServerEntry, error) {
	entry, relayErr := resolvePeerServerFromRelay(nodeName)
	if relayErr == nil {
		return entry, nil
	}

	servers, err := config.LoadServersConfig(dataDir())
	if err != nil {
		return config.ServerEntry{}, err
	}

	entry, ok := servers.Servers[nodeName]
	if !ok {
		if relayErr != nil && !strings.Contains(relayErr.Error(), "not configured") {
			return config.ServerEntry{}, relayErr
		}
		return config.ServerEntry{}, fmt.Errorf(
			"remote node %q is not discoverable yet; add a saved peer server with 'cw server add %s <url>'",
			nodeName,
			nodeName,
		)
	}
	if relayErr != nil {
		return entry, nil
	}
	return entry, nil
}

func resolvePeerServerFromRelay(nodeName string) (config.ServerEntry, error) {
	node, _, err := lookupRelayNode(nodeName)
	if err != nil {
		return config.ServerEntry{}, err
	}
	if strings.TrimSpace(node.PeerURL) == "" {
		return config.ServerEntry{}, fmt.Errorf(
			"remote node %q is registered but has no advertised peer URL; set node.external_url or CODEWIRE_EXTERNAL_URL on that node",
			nodeName,
		)
	}
	return config.ServerEntry{
		URL:   node.PeerURL,
		Token: "",
	}, nil
}

func lookupRelayNode(nodeName string) (relayNodeRecord, *config.Config, error) {
	cfg, err := config.LoadConfig(dataDir())
	if err != nil {
		return relayNodeRecord{}, nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg.RelayURL == nil || strings.TrimSpace(*cfg.RelayURL) == "" {
		return relayNodeRecord{}, nil, fmt.Errorf("relay is not configured")
	}
	if cfg.RelaySession == nil || strings.TrimSpace(*cfg.RelaySession) == "" {
		return relayNodeRecord{}, nil, fmt.Errorf("relay session is not configured")
	}

	relayURL := strings.TrimRight(strings.TrimSpace(*cfg.RelayURL), "/")
	requestURL := relayURL + "/api/v1/nodes"
	if cfg.RelayNetwork != nil && strings.TrimSpace(*cfg.RelayNetwork) != "" {
		requestURL += "?network_id=" + neturl.QueryEscape(strings.TrimSpace(*cfg.RelayNetwork))
	}

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return relayNodeRecord{}, nil, fmt.Errorf("building relay node discovery request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(*cfg.RelaySession))

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

func peerWebSocketURL(raw string) (string, error) {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parsing peer server URL %q: %w", raw, err)
	}

	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("peer server URL %q must use http, https, ws, or wss", raw)
	}

	switch {
	case u.Path == "", u.Path == "/":
		u.Path = "/peer"
	case strings.HasSuffix(u.Path, "/ws"):
		u.Path = strings.TrimSuffix(u.Path, "/ws") + "/peer"
	case !strings.HasSuffix(u.Path, "/peer"):
		u.Path = strings.TrimRight(u.Path, "/") + "/peer"
	}

	return u.String(), nil
}

func dialPeerClientForNode(ctx context.Context, nodeName string) (*peerclient.Client, func(), error) {
	if node, cfg, err := lookupRelayNode(nodeName); err == nil {
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
		return client, func() {
			_ = client.Close()
			_ = tailnetConn.Close()
		}, nil
	}

	entry, err := resolvePeerServer(nodeName)
	if err != nil {
		return nil, nil, err
	}
	peerURL, err := peerWebSocketURL(entry.URL)
	if err != nil {
		return nil, nil, err
	}

	token, err := resolvePeerAuthToken(ctx, entry)
	if err != nil {
		return nil, nil, err
	}

	client, ws, err := peerclient.DialWebSocket(ctx, peerURL, token)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to peer node %q: %w", nodeName, err)
	}
	return client, func() {
		_ = client.Close()
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}, nil
}

func resolvePeerAuthToken(ctx context.Context, entry config.ServerEntry) (string, error) {
	if strings.TrimSpace(tokenFlag) != "" {
		return strings.TrimSpace(tokenFlag), nil
	}
	if issued, err := issueRuntimeCredentialForPeer(ctx); err == nil && strings.TrimSpace(issued) != "" {
		return issued, nil
	}
	if strings.HasPrefix(strings.TrimSpace(entry.Token), networkauth.RuntimeTokenPrefix+".") {
		return strings.TrimSpace(entry.Token), nil
	}
	return "", fmt.Errorf("peer messaging requires a relay-issued runtime credential; configure relay access for this network or pass an explicit runtime credential with --token")
}

func issueRuntimeCredentialForPeer(ctx context.Context) (string, error) {
	cfg, err := config.LoadConfig(dataDir())
	if err != nil {
		return "", err
	}
	if cfg.RelayURL == nil || strings.TrimSpace(*cfg.RelayURL) == "" {
		return "", fmt.Errorf("relay is not configured")
	}
	if cfg.RelaySession == nil || strings.TrimSpace(*cfg.RelaySession) == "" {
		return "", fmt.Errorf("relay session is not configured")
	}

	issued, err := networkauth.IssueClientRuntimeCredential(ctx, http.DefaultClient, *cfg.RelayURL, *cfg.RelaySession, relayNetworkIDFromConfig(cfg))
	if err != nil {
		return "", err
	}
	return issued.Credential, nil
}

func relayNetworkIDFromConfig(cfg *config.Config) string {
	if cfg == nil || cfg.RelayNetwork == nil {
		return networkauth.DefaultNetworkID
	}
	return networkauth.ResolveNetworkID(*cfg.RelayNetwork)
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
