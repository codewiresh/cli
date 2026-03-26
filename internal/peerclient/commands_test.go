package peerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/peer"
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

func TestMsgAndInbox(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	launchNamedSleepSession(t, sm, "coder")

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &peer.Server{
		Sessions: sm,
		NodeName: "node-b",
	}
	go srv.ServeConn(context.Background(), serverConn)

	client := New(clientConn)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgID, err := Msg(ctx, client, nil, "", peer.SessionLocator{Node: "node-b", Name: "coder"}, "hello", "inbox")
	if err != nil {
		t.Fatalf("Msg: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message id")
	}

	messages, err := Inbox(ctx, client, peer.SessionLocator{Node: "node-b", Name: "coder"}, 10)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].Body != "hello" {
		t.Fatalf("message body = %q, want hello", messages[0].Body)
	}
}

func TestRequest(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	recipientID := launchNamedSleepSession(t, sm, "coder")

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &peer.Server{
		Sessions: sm,
		NodeName: "node-b",
	}
	go srv.ServeConn(context.Background(), serverConn)

	client := New(clientConn)
	defer client.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		events, err := sm.ReadMessages(recipientID, 10)
		if err != nil || len(events) == 0 {
			return
		}
		for _, event := range events {
			if event.Type != session.EventRequest {
				continue
			}
			var req session.RequestData
			if err := json.Unmarshal(event.Data, &req); err != nil {
				return
			}
			_ = sm.SendReply(recipientID, req.RequestID, "approved")
			return
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := Request(ctx, client, nil, "", peer.SessionLocator{Node: "node-b", Name: "coder"}, "deploy?", 5, "inbox")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if result.RequestID == "" {
		t.Fatal("expected non-empty request id")
	}
	if result.ReplyBody != "approved" {
		t.Fatalf("ReplyBody = %q, want approved", result.ReplyBody)
	}
}

func TestListen(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	launchNamedSleepSession(t, sm, "coder")

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &peer.Server{
		Sessions: sm,
		NodeName: "node-b",
	}
	go srv.ServeConn(context.Background(), serverConn)

	client := New(clientConn)
	defer client.Close()

	stopErr := errors.New("stop")
	done := make(chan error, 1)
	ready := make(chan struct{}, 1)
	go func() {
		done <- ListenWithReady(context.Background(), client, &peer.SessionLocator{Node: "node-b", Name: "coder"}, func() error {
			close(ready)
			return nil
		}, func(event *protocol.SessionEvent) error {
			if event == nil || event.EventType != "direct.message" {
				return fmt.Errorf("unexpected event: %+v", event)
			}
			return stopErr
		})
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for listen readiness")
	}
	serverConn2, clientConn2 := net.Pipe()
	defer clientConn2.Close()
	go srv.ServeConn(context.Background(), serverConn2)
	client2 := New(clientConn2)
	defer client2.Close()
	if _, err := Msg(context.Background(), client2, nil, "", peer.SessionLocator{Node: "node-b", Name: "coder"}, "hello listen", "inbox"); err != nil {
		t.Fatalf("Msg: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, stopErr) {
			t.Fatalf("Listen error = %v, want stopErr", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for listen event")
	}
}

func TestReply(t *testing.T) {
	sm, err := session.NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	recipientID := launchNamedSleepSession(t, sm, "coder")

	requestID, replyCh, err := sm.SendRequest(0, recipientID, "ship it?")
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	srv := &peer.Server{
		Sessions: sm,
		NodeName: "node-b",
		AuthorizeSender: func(_ context.Context, verb string, from *peer.SessionLocator, senderCap string) (*peer.AuthorizedSender, error) {
			if verb != "reply" {
				t.Fatalf("verb = %q", verb)
			}
			if from == nil || from.Node != "node-b" || from.Name != "coder" {
				t.Fatalf("from = %+v", from)
			}
			if senderCap != "reply-cap" {
				t.Fatalf("senderCap = %q", senderCap)
			}
			id := recipientID
			return &peer.AuthorizedSender{DisplayName: "node-b:coder", SessionID: &id, SessionName: "coder"}, nil
		},
	}
	go srv.ServeConn(context.Background(), serverConn)

	client := New(clientConn)
	defer client.Close()

	if err := Reply(context.Background(), client, &peer.SessionLocator{Node: "node-b", Name: "coder"}, "reply-cap", requestID, "done"); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	select {
	case reply := <-replyCh:
		if reply.Body != "done" {
			t.Fatalf("reply body = %q, want done", reply.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reply")
	}
}
