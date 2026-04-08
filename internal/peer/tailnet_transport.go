package peer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
	"tailscale.com/tailcfg"

	"github.com/codewiresh/codewire/internal/networkauth"
	tailnetlib "github.com/codewiresh/tailnet"
)

const TailnetPeerPort uint16 = 47319

type TailnetCoordinateRequest struct {
	Type       string           `json:"type"`
	Node       *tailnetlib.Node `json:"node,omitempty"`
	TargetNode string           `json:"target_node,omitempty"`
}

type TailnetCoordinateResponse struct {
	Type    string             `json:"type"`
	Nodes   []*tailnetlib.Node `json:"nodes,omitempty"`
	DERPMap *tailcfg.DERPMap   `json:"derp_map,omitempty"`
	Error   string             `json:"error,omitempty"`
}

// StablePrincipalUUID derives a deterministic UUID for one network principal.
func StablePrincipalUUID(networkID, subjectKind, subjectID string) uuid.UUID {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		networkauth.ResolveNetworkID(networkID),
		strings.TrimSpace(subjectKind),
		strings.TrimSpace(subjectID),
	}, "\x00")))
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	id, _ := uuid.FromBytes(sum[:16])
	return id
}

// TailnetPrefixForPrincipal returns the stable tailnet /128 for a principal.
func TailnetPrefixForPrincipal(networkID, subjectKind, subjectID string) netip.Prefix {
	return tailnetlib.CWServicePrefix.PrefixFromUUID(StablePrincipalUUID(networkID, subjectKind, subjectID))
}

// TailnetPrefixForNode returns the stable tailnet /128 for a node in a network.
func TailnetPrefixForNode(networkID, nodeName string) netip.Prefix {
	return TailnetPrefixForPrincipal(networkID, networkauth.SubjectKindNode, nodeName)
}

// NewDERPMapFromRelayURL derives a DERP map from the relay base URL.
func NewDERPMapFromRelayURL(relayURL string) (*tailcfg.DERPMap, error) {
	u, err := neturl.Parse(strings.TrimSpace(relayURL))
	if err != nil {
		return nil, fmt.Errorf("parsing relay URL %q: %w", relayURL, err)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("relay URL %q has no hostname", relayURL)
	}

	port := 443
	switch u.Scheme {
	case "http", "ws":
		port = 80
	case "https", "wss":
		port = 443
	default:
		return nil, fmt.Errorf("relay URL %q must use http, https, ws, or wss", relayURL)
	}
	if rawPort := u.Port(); rawPort != "" {
		if _, err := fmt.Sscanf(rawPort, "%d", &port); err != nil {
			return nil, fmt.Errorf("invalid relay port %q", rawPort)
		}
	}

	return tailnetlib.NewDERPMap(host, port, u.Scheme == "http" || u.Scheme == "ws"), nil
}

// DialNetworkPeerTCP dials a node peer RPC listener over the network tailnet.
func DialNetworkPeerTCP(ctx context.Context, relayURL, runtimeCredential, targetNode string, port uint16) (net.Conn, *tailnetlib.Conn, error) {
	claims, err := networkauth.ParseRuntimeCredential(runtimeCredential)
	if err != nil {
		return nil, nil, err
	}

	derpMap, err := NewDERPMapFromRelayURL(relayURL)
	if err != nil {
		return nil, nil, err
	}

	localID := tailnetIDForDial(claims)
	localPrefix := tailnetlib.CWServicePrefix.PrefixFromUUID(localID)
	conn, err := tailnetlib.NewConn(&tailnetlib.Options{
		ID:        localID,
		Addresses: []netip.Prefix{localPrefix},
		DERPMap:   derpMap,
		Logger:    slog.Default(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("tailnet conn: %w", err)
	}

	peerReady, err := coordinateTailnet(ctx, relayURL, runtimeCredential, conn, strings.TrimSpace(targetNode))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	select {
	case <-peerReady:
	case <-time.After(defaultPeerTimeout):
		conn.Close()
		return nil, nil, fmt.Errorf("timeout waiting for node %q tailnet peer info", targetNode)
	case <-ctx.Done():
		conn.Close()
		return nil, nil, ctx.Err()
	}

	targetAddr := TailnetPrefixForNode(claims.NetworkID, targetNode).Addr()
	dialCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()

	tcpConn, err := conn.DialContextTCP(dialCtx, netip.AddrPortFrom(targetAddr, port))
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("tailnet TCP dial to node %q failed: %w", targetNode, err)
	}
	return tcpConn, conn, nil
}

// StartNodeTailnetListener starts a persistent tailnet listener serving HTTP.
// The handler should include both the /ws (frame protocol) and /peer (RPC) endpoints.
func StartNodeTailnetListener(ctx context.Context, relayURL, runtimeCredential string, handler http.Handler) (*tailnetlib.Conn, error) {
	if handler == nil {
		return nil, fmt.Errorf("handler is nil")
	}

	claims, err := networkauth.ParseRuntimeCredential(runtimeCredential)
	if err != nil {
		return nil, err
	}
	if claims.SubjectKind != networkauth.SubjectKindNode {
		return nil, fmt.Errorf("runtime credential subject_kind = %q, want %q", claims.SubjectKind, networkauth.SubjectKindNode)
	}

	derpMap, err := NewDERPMapFromRelayURL(relayURL)
	if err != nil {
		return nil, err
	}

	localID := StablePrincipalUUID(claims.NetworkID, claims.SubjectKind, claims.SubjectID)
	localPrefix := tailnetlib.CWServicePrefix.PrefixFromUUID(localID)
	conn, err := tailnetlib.NewConn(&tailnetlib.Options{
		ID:        localID,
		Addresses: []netip.Prefix{localPrefix},
		DERPMap:   derpMap,
		Logger:    slog.Default(),
	})
	if err != nil {
		return nil, fmt.Errorf("tailnet conn: %w", err)
	}

	ln, err := conn.Listen("tcp", fmt.Sprintf(":%d", TailnetPeerPort))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("tailnet listen: %w", err)
	}

	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("tailnet HTTP server error", "err", err)
		}
	}()

	if _, err := coordinateTailnet(ctx, relayURL, runtimeCredential, conn, ""); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

func coordinateTailnet(ctx context.Context, relayURL, runtimeCredential string, conn *tailnetlib.Conn, targetNode string) (<-chan struct{}, error) {
	wsURL, err := tailnetCoordinateURL(relayURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(targetNode) != "" {
		// Dialer connections use ?role=dial so the relay assigns a random
		// peer UUID instead of the stable node UUID (which is reserved for
		// the persistent listener).
		wsURL += "?role=dial"
	}

	wsConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{
			"Authorization": {"Bearer " + strings.TrimSpace(runtimeCredential)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("coordinator connect: %w", err)
	}

	peerReady := make(chan struct{}, 1)
	var writeMu sync.Mutex
	writeRequest := func(req TailnetCoordinateRequest) {
		payload, err := json.Marshal(req)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = wsConn.Write(ctx, websocket.MessageText, payload)
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				writeMu.Lock()
				err := wsConn.Ping(pingCtx)
				writeMu.Unlock()
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()

	conn.SetNodeCallback(func(node *tailnetlib.Node) {
		writeRequest(TailnetCoordinateRequest{
			Type: "node",
			Node: node,
		})
	})
	if strings.TrimSpace(targetNode) != "" {
		writeRequest(TailnetCoordinateRequest{
			Type:       "subscribe",
			TargetNode: strings.TrimSpace(targetNode),
		})
	}

	go func() {
		defer wsConn.Close(websocket.StatusNormalClosure, "")
		for {
			_, data, err := wsConn.Read(ctx)
			if err != nil {
				return
			}

			var resp TailnetCoordinateResponse
			if json.Unmarshal(data, &resp) != nil {
				continue
			}
			if resp.DERPMap != nil {
				conn.SetDERPMap(resp.DERPMap)
			}
			if resp.Type == "peer_update" && len(resp.Nodes) > 0 {
				if err := conn.UpdatePeers(resp.Nodes); err == nil {
					select {
					case peerReady <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	return peerReady, nil
}

func tailnetCoordinateURL(relayURL string) (string, error) {
	u, err := neturl.Parse(strings.TrimSpace(relayURL))
	if err != nil {
		return "", fmt.Errorf("parsing relay URL %q: %w", relayURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("relay URL %q must use http, https, ws, or wss", relayURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/tailnet/coordinate"
	return u.String(), nil
}

func tailnetIDForDial(_ *networkauth.RuntimeClaims) uuid.UUID {
	// Always use a random UUID for dial operations. The stable UUID is
	// reserved for the persistent node listener — using it here would
	// collide with the listener's coordinator registration.
	return uuid.New()
}
