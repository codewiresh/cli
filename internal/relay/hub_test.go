package relay_test

import (
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/relay"
)

func TestHubRegisterUnregister(t *testing.T) {
	h := relay.NewNodeHub()
	h.Register("fleet-a", "n1", nil) // nil sender for test
	if !h.Has("fleet-a", "n1") {
		t.Fatal("expected n1 registered")
	}
	h.Unregister("fleet-a", "n1")
	if h.Has("fleet-a", "n1") {
		t.Fatal("expected n1 unregistered")
	}
}

func TestHubSend(t *testing.T) {
	h := relay.NewNodeHub()
	ch := make(chan relay.HubMessage, 1)
	h.Register("fleet-a", "n1", ch)
	err := h.Send("fleet-a", "n1", relay.HubMessage{Type: "test"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ch:
		if msg.Type != "test" {
			t.Fatalf("wrong type: %s", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestHubSendUnknown(t *testing.T) {
	h := relay.NewNodeHub()
	err := h.Send("fleet-a", "missing", relay.HubMessage{Type: "x"})
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

func TestHubAllowsSameNodeNameAcrossFleets(t *testing.T) {
	h := relay.NewNodeHub()
	chA := make(chan relay.HubMessage, 1)
	chB := make(chan relay.HubMessage, 1)

	h.Register("fleet-a", "n1", chA)
	h.Register("fleet-b", "n1", chB)

	if err := h.Send("fleet-a", "n1", relay.HubMessage{Type: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Send("fleet-b", "n1", relay.HubMessage{Type: "b"}); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-chA:
		if msg.Type != "a" {
			t.Fatalf("fleet-a got %q", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for fleet-a message")
	}

	select {
	case msg := <-chB:
		if msg.Type != "b" {
			t.Fatalf("fleet-b got %q", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for fleet-b message")
	}
}
