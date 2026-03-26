package relay_test

import (
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/relay"
)

func TestHubRegisterUnregister(t *testing.T) {
	h := relay.NewNodeHub()
	h.Register("network-a", "n1", nil) // nil sender for test
	if !h.Has("network-a", "n1") {
		t.Fatal("expected n1 registered")
	}
	h.Unregister("network-a", "n1")
	if h.Has("network-a", "n1") {
		t.Fatal("expected n1 unregistered")
	}
}

func TestHubSend(t *testing.T) {
	h := relay.NewNodeHub()
	ch := make(chan relay.HubMessage, 1)
	h.Register("network-a", "n1", ch)
	err := h.Send("network-a", "n1", relay.HubMessage{Type: "test"})
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
	err := h.Send("network-a", "missing", relay.HubMessage{Type: "x"})
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

func TestHubAllowsSameNodeNameAcrossNetworks(t *testing.T) {
	h := relay.NewNodeHub()
	chA := make(chan relay.HubMessage, 1)
	chB := make(chan relay.HubMessage, 1)

	h.Register("network-a", "n1", chA)
	h.Register("network-b", "n1", chB)

	if err := h.Send("network-a", "n1", relay.HubMessage{Type: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Send("network-b", "n1", relay.HubMessage{Type: "b"}); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-chA:
		if msg.Type != "a" {
			t.Fatalf("network-a got %q", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for network-a message")
	}

	select {
	case msg := <-chB:
		if msg.Type != "b" {
			t.Fatalf("network-b got %q", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for network-b message")
	}
}
