package relay

import (
	"context"
	"testing"

	"github.com/codewiresh/codewire/internal/store"
)

func TestDispatchNodeAgentMessagePublishesTaskReport(t *testing.T) {
	tasks := &recordingTaskStore{ch: make(chan struct{}, 1)}
	node := store.NodeRecord{
		NetworkID: "network-a",
		Name:      "builder",
	}

	err := dispatchNodeAgentMessage(context.Background(), tasks, node, []byte(`{"type":"TaskReport","event_id":"task_123","session_id":7,"session_name":"planner","summary":"ship relay ingest","state":"working","timestamp":"2026-04-08T15:04:05Z","network_id":"spoofed"}`))
	if err != nil {
		t.Fatalf("dispatchNodeAgentMessage: %v", err)
	}

	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	if tasks.calls != 1 {
		t.Fatalf("calls = %d, want 1", tasks.calls)
	}
	if tasks.node == nil || tasks.msg == nil {
		t.Fatal("expected recorded node and message")
	}
	if tasks.node.NetworkID != "network-a" {
		t.Fatalf("network id = %q", tasks.node.NetworkID)
	}
	if tasks.node.Name != "builder" {
		t.Fatalf("node name = %q", tasks.node.Name)
	}
	if tasks.msg.EventID != "task_123" {
		t.Fatalf("event id = %q", tasks.msg.EventID)
	}
	if tasks.msg.SessionID != 7 {
		t.Fatalf("session id = %d", tasks.msg.SessionID)
	}
	if tasks.msg.SessionName != "planner" {
		t.Fatalf("session name = %q", tasks.msg.SessionName)
	}
	if tasks.msg.Summary != "ship relay ingest" {
		t.Fatalf("summary = %q", tasks.msg.Summary)
	}
	if tasks.msg.State != "working" {
		t.Fatalf("state = %q", tasks.msg.State)
	}
}

func TestDispatchNodeAgentMessageIgnoresUnknownType(t *testing.T) {
	tasks := &recordingTaskStore{}
	node := store.NodeRecord{
		NetworkID: "network-a",
		Name:      "builder",
	}

	if err := dispatchNodeAgentMessage(context.Background(), tasks, node, []byte(`{"type":"UnknownType","value":"ignored"}`)); err != nil {
		t.Fatalf("dispatchNodeAgentMessage: %v", err)
	}

	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	if tasks.calls != 0 {
		t.Fatalf("calls = %d, want 0", tasks.calls)
	}
}
