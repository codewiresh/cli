package peer

import (
	"context"
	"net"

	"nhooyr.io/websocket"
)

// ServeWebSocket adapts a WebSocket to net.Conn and serves peer RPC on it.
func (s *Server) ServeWebSocket(ctx context.Context, ws *websocket.Conn) {
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer nc.Close()
	s.ServeConn(ctx, nc)
}

// DialWebSocket dials a peer WebSocket endpoint and adapts it to net.Conn.
func DialWebSocket(ctx context.Context, url string, opts *websocket.DialOptions) (net.Conn, *websocket.Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, nil, err
	}
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	return nc, ws, nil
}
