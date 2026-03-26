package peerclient

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/codewiresh/codewire/internal/peer"
	"nhooyr.io/websocket"
)

// Client sends peer RPC requests over a net.Conn.
type Client struct {
	conn net.Conn
	mu   sync.Mutex
}

// New returns a client using conn.
func New(conn net.Conn) *Client {
	return &Client{conn: conn}
}

// DialWebSocket creates a peer client over a WebSocket endpoint.
func DialWebSocket(ctx context.Context, url, token string) (*Client, *websocket.Conn, error) {
	opts := &websocket.DialOptions{}
	if token != "" {
		opts.HTTPHeader = map[string][]string{
			"Authorization": {"Bearer " + token},
		}
	}
	nc, ws, err := peer.DialWebSocket(ctx, url, opts)
	if err != nil {
		return nil, nil, err
	}
	return New(nc), ws, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Do sends req and waits for one response.
func (c *Client) Do(_ context.Context, req *peer.PeerRequest) (*peer.PeerResponse, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("client connection is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := peer.WriteRequest(c.conn, req); err != nil {
		return nil, err
	}
	resp, err := peer.ReadResponse(c.conn)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("connection closed before response")
	}
	if resp.OpID != req.OpID {
		return nil, fmt.Errorf("response op_id = %q, want %q", resp.OpID, req.OpID)
	}
	if resp.Type == "Error" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}
