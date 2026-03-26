package peer

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/protocol"
	"github.com/codewiresh/codewire/internal/session"
)

func launchNamedSleepSession(t *testing.T, sm *session.SessionManager, name string) uint32 {
	t.Helper()
	id, err := sm.Launch([]string{"sleep", "30"}, "/tmp", nil, nil, "")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if name != "" {
		if err := sm.SetName(id, name); err != nil {
			t.Fatalf("SetName: %v", err)
		}
	}
	t.Cleanup(func() { _ = sm.Kill(id) })
	return id
}

func TestServerMsgSendAndRead(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	recipientID := launchNamedSleepSession(t, sm, "coder")
	_ = recipientID

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &Server{
		Sessions: sm,
		NodeName: "node-b",
	}
	go srv.ServeConn(context.Background(), serverConn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx

	sendReq := &PeerRequest{
		OpID: "op-send",
		Type: "MsgSend",
		To:   &SessionLocator{Node: "node-b", Name: "coder"},
		Body: "hello over peer rpc",
	}
	if err := WriteRequest(clientConn, sendReq); err != nil {
		t.Fatalf("WriteRequest MsgSend: %v", err)
	}
	sendResp, err := ReadResponse(clientConn)
	if err != nil {
		t.Fatalf("ReadResponse MsgSend: %v", err)
	}
	if sendResp.Type != "MsgSent" || sendResp.MessageID == "" {
		t.Fatalf("unexpected send response: %+v", sendResp)
	}

	readReq := &PeerRequest{
		OpID:    "op-read",
		Type:    "MsgRead",
		Session: &SessionLocator{Node: "node-b", Name: "coder"},
	}
	if err := WriteRequest(clientConn, readReq); err != nil {
		t.Fatalf("WriteRequest MsgRead: %v", err)
	}
	readResp, err := ReadResponse(clientConn)
	if err != nil {
		t.Fatalf("ReadResponse MsgRead: %v", err)
	}
	if readResp.Type != "MsgReadResult" {
		t.Fatalf("unexpected read response: %+v", readResp)
	}
	if len(readResp.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(readResp.Messages))
	}
	if got := readResp.Messages[0].Body; got != "hello over peer rpc" {
		t.Fatalf("message body = %q, want %q", got, "hello over peer rpc")
	}
}

func TestServerRejectsWrongNode(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	launchNamedSleepSession(t, sm, "coder")

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &Server{
		Sessions: sm,
		NodeName: "node-b",
	}
	go srv.ServeConn(context.Background(), serverConn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx

	if err := WriteRequest(clientConn, &PeerRequest{
		OpID: "op-send",
		Type: "MsgSend",
		To:   &SessionLocator{Node: "node-a", Name: "coder"},
		Body: "hello",
	}); err != nil {
		t.Fatalf("WriteRequest MsgSend: %v", err)
	}
	resp, err := ReadResponse(clientConn)
	if err != nil {
		t.Fatalf("ReadResponse MsgSend: %v", err)
	}
	if resp.Type != "Error" {
		t.Fatalf("response type = %q, want Error", resp.Type)
	}
}

func TestServerMsgSendWithAuthorizedRemoteSender(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	recipientID := launchNamedSleepSession(t, sm, "coder")

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &Server{
		Sessions: sm,
		NodeName: "node-b",
		AuthorizeSender: func(_ context.Context, verb string, from *SessionLocator, senderCap string) (*AuthorizedSender, error) {
			if verb != "msg" {
				t.Fatalf("verb = %q", verb)
			}
			if from == nil || from.Node != "node-a" || from.Name != "planner" {
				t.Fatalf("from = %+v", from)
			}
			if senderCap != "valid-cap" {
				t.Fatalf("senderCap = %q", senderCap)
			}
			return &AuthorizedSender{DisplayName: "node-a:planner"}, nil
		},
	}
	go srv.ServeConn(context.Background(), serverConn)

	if err := WriteRequest(clientConn, &PeerRequest{
		OpID:      "op-send",
		Type:      "MsgSend",
		SenderCap: "valid-cap",
		From:      &SessionLocator{Node: "node-a", Name: "planner"},
		To:        &SessionLocator{Node: "node-b", Name: "coder"},
		Body:      "hello from delegated sender",
	}); err != nil {
		t.Fatalf("WriteRequest MsgSend: %v", err)
	}
	resp, err := ReadResponse(clientConn)
	if err != nil {
		t.Fatalf("ReadResponse MsgSend: %v", err)
	}
	if resp.Type != "MsgSent" {
		t.Fatalf("response type = %q", resp.Type)
	}

	events, err := sm.ReadMessages(recipientID, 10)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	messages := make([]protocol.MessageResponse, 0, len(events))
	for _, event := range events {
		if mr := eventToMessageResponse(event); mr != nil {
			messages = append(messages, *mr)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].FromName != "node-a:planner" {
		t.Fatalf("FromName = %q", messages[0].FromName)
	}
}
