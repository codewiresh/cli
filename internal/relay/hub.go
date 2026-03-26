package relay

import (
	"fmt"
	"sync"
)

func hubKey(networkID, name string) string {
	return networkID + ":" + name
}

// HubMessage is a control message sent to a connected node agent.
type HubMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
}

// NodeHub tracks connected node agents (in-memory).
type NodeHub struct {
	mu    sync.RWMutex
	nodes map[string]chan<- HubMessage
}

func NewNodeHub() *NodeHub {
	return &NodeHub{nodes: make(map[string]chan<- HubMessage)}
}

func (h *NodeHub) Register(networkID, name string, ch chan<- HubMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nodes[hubKey(networkID, name)] = ch
}

func (h *NodeHub) Unregister(networkID, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.nodes, hubKey(networkID, name))
}

func (h *NodeHub) Has(networkID, name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.nodes[hubKey(networkID, name)]
	return ok
}

// Send delivers a message to the named node. Returns error if node not connected.
func (h *NodeHub) Send(networkID, name string, msg HubMessage) error {
	h.mu.RLock()
	ch, ok := h.nodes[hubKey(networkID, name)]
	h.mu.RUnlock()
	if !ok {
		return fmt.Errorf("node %q not connected in network %q", name, networkID)
	}
	select {
	case ch <- msg:
		return nil
	default:
		return fmt.Errorf("node %q message buffer full in network %q", name, networkID)
	}
}
