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
	nc, ws, err := peer.DialWebSocket(ctx, url, &websocket.DialOptions{})
	if err != nil {
		return nil, nil, err
	}
	client := New(nc)
	if err := client.Authenticate(ctx, token); err != nil {
		_ = client.Close()
		_ = ws.Close(websocket.StatusPolicyViolation, err.Error())
		return nil, nil, err
	}
	return client, ws, nil
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

// Authenticate binds the connection to a verified runtime principal.
func (c *Client) Authenticate(_ context.Context, runtimeCredential string) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("client connection is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := peer.WriteAuthHello(c.conn, &peer.AuthHello{
		Type:              "AuthHello",
		RuntimeCredential: runtimeCredential,
	}); err != nil {
		return err
	}
	ack, err := peer.ReadAuthAck(c.conn)
	if err != nil {
		return err
	}
	if ack == nil {
		return fmt.Errorf("connection closed before auth ack")
	}
	if ack.Type == "Error" {
		return fmt.Errorf("%s", ack.Error)
	}
	return nil
}
