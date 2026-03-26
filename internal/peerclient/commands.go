package peerclient

import (
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/codewiresh/codewire/internal/peer"
	"github.com/codewiresh/codewire/internal/protocol"
)

// Requester is the minimal client surface needed by command helpers.
type Requester interface {
	Do(ctx context.Context, req *peer.PeerRequest) (*peer.PeerResponse, error)
}

// RequestResult is the reply payload for a remote request operation.
type RequestResult struct {
	RequestID string
	ReplyBody string
	From      *peer.SessionLocator
}

// Msg sends a peer RPC direct message.
func Msg(ctx context.Context, conn Requester, from *peer.SessionLocator, senderCap string, to peer.SessionLocator, body, delivery string) (string, error) {
	if err := to.Validate(); err != nil {
		return "", err
	}
	resp, err := conn.Do(ctx, &peer.PeerRequest{
		OpID:      uuid.NewString(),
		Type:      "MsgSend",
		SenderCap: senderCap,
		From:      from,
		To:        &to,
		Body:      body,
		Delivery:  delivery,
	})
	if err != nil {
		return "", err
	}
	if resp.Type != "MsgSent" {
		return "", fmt.Errorf("unexpected response type %q", resp.Type)
	}
	return resp.MessageID, nil
}

// Inbox reads a remote inbox via peer RPC.
func Inbox(ctx context.Context, conn Requester, session peer.SessionLocator, tail int) ([]protocol.MessageResponse, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	t := uint(tail)
	resp, err := conn.Do(ctx, &peer.PeerRequest{
		OpID:    uuid.NewString(),
		Type:    "MsgRead",
		Session: &session,
		Tail:    &t,
	})
	if err != nil {
		return nil, err
	}
	if resp.Type != "MsgReadResult" {
		return nil, fmt.Errorf("unexpected response type %q", resp.Type)
	}
	return resp.Messages, nil
}

// Request sends a peer RPC request and waits for the reply.
func Request(ctx context.Context, conn Requester, from *peer.SessionLocator, senderCap string, to peer.SessionLocator, body string, timeout uint64, delivery string) (*RequestResult, error) {
	if err := to.Validate(); err != nil {
		return nil, err
	}
	resp, err := conn.Do(ctx, &peer.PeerRequest{
		OpID:      uuid.NewString(),
		Type:      "MsgRequest",
		SenderCap: senderCap,
		From:      from,
		To:        &to,
		Body:      body,
		TimeoutS:  &timeout,
		Delivery:  delivery,
	})
	if err != nil {
		return nil, err
	}
	if resp.Type != "MsgRequestResult" {
		return nil, fmt.Errorf("unexpected response type %q", resp.Type)
	}
	return &RequestResult{
		RequestID: resp.RequestID,
		ReplyBody: resp.ReplyBody,
		From:      resp.From,
	}, nil
}

// Reply sends a peer RPC reply for a pending request.
func Reply(ctx context.Context, conn Requester, from *peer.SessionLocator, senderCap, requestID, body string) error {
	resp, err := conn.Do(ctx, &peer.PeerRequest{
		OpID:      uuid.NewString(),
		Type:      "MsgReply",
		SenderCap: senderCap,
		From:      from,
		RequestID: requestID,
		Body:      body,
	})
	if err != nil {
		return err
	}
	if resp.Type != "MsgReplySent" {
		return fmt.Errorf("unexpected response type %q", resp.Type)
	}
	return nil
}

// Listen opens a streaming peer RPC listen operation and invokes onEvent for each event.
func Listen(ctx context.Context, client *Client, session *peer.SessionLocator, onEvent func(*protocol.SessionEvent) error) error {
	return listen(ctx, client, session, nil, onEvent)
}

// ListenWithReady opens a streaming peer RPC listen operation and calls onReady once
// the remote subscription has acknowledged the listen request.
func ListenWithReady(ctx context.Context, client *Client, session *peer.SessionLocator, onReady func() error, onEvent func(*protocol.SessionEvent) error) error {
	return listen(ctx, client, session, onReady, onEvent)
}

func listen(ctx context.Context, client *Client, session *peer.SessionLocator, onReady func() error, onEvent func(*protocol.SessionEvent) error) error {
	if client == nil || client.conn == nil {
		return fmt.Errorf("client connection is nil")
	}
	if session != nil {
		if err := session.Validate(); err != nil {
			return err
		}
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	req := &peer.PeerRequest{
		OpID:    uuid.NewString(),
		Type:    "MsgListen",
		Session: session,
	}
	if err := peer.WriteRequest(client.conn, req); err != nil {
		return err
	}

	resp, err := peer.ReadResponse(client.conn)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("connection closed before response")
	}
	if resp.OpID != req.OpID {
		return fmt.Errorf("response op_id = %q, want %q", resp.OpID, req.OpID)
	}
	if resp.Type == "Error" {
		return fmt.Errorf("%s", resp.Error)
	}
	if resp.Type != "MsgListenAck" {
		return fmt.Errorf("unexpected response type %q", resp.Type)
	}
	if onReady != nil {
		if err := onReady(); err != nil {
			return err
		}
	}

	for {
		resp, err := peer.ReadResponse(client.conn)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if resp == nil {
			return nil
		}
		if resp.OpID != req.OpID {
			return fmt.Errorf("response op_id = %q, want %q", resp.OpID, req.OpID)
		}
		switch resp.Type {
		case "Event":
			if resp.Event != nil && onEvent != nil {
				if err := onEvent(resp.Event); err != nil {
					return err
				}
			}
		case "Error":
			return fmt.Errorf("%s", resp.Error)
		}
	}
}
