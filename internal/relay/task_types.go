package relay

// TaskReportMessage is sent from a node agent to the relay over the existing
// authenticated websocket connection.
type TaskReportMessage struct {
	Type        string `json:"type"` // "TaskReport"
	EventID     string `json:"event_id"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"` // "working", "complete", "blocked", "failed"
	Timestamp   string `json:"timestamp"`
}

// TaskEvent is the canonical relay-side task event stored in JetStream.
type TaskEvent struct {
	EventID     string `json:"event_id"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"`
	Timestamp   string `json:"timestamp"`
}

// LatestTaskValue is the latest known task state for one session.
type LatestTaskValue struct {
	EventID     string `json:"event_id"`
	StreamSeq   uint64 `json:"stream_seq"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"`
	Timestamp   string `json:"timestamp"`
}

// TaskAPIEvent is the relay HTTP/SSE payload for task watches.
type TaskAPIEvent struct {
	Seq         uint64 `json:"seq"`
	Type        string `json:"type"`
	EventID     string `json:"event_id,omitempty"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name,omitempty"`
	SessionID   uint32 `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary,omitempty"`
	State       string `json:"state,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}
