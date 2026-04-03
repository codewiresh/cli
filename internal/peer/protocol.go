package peer

import (
	"encoding/json"
	"fmt"
	"io"

	baseprotocol "github.com/codewiresh/codewire/internal/protocol"
)

// SessionLocator identifies a session on a node.
type SessionLocator struct {
	Node string  `json:"node,omitempty"`
	ID   *uint32 `json:"id,omitempty"`
	Name string  `json:"name,omitempty"`
}

// Validate ensures the locator references exactly one local session identity.
func (l SessionLocator) Validate() error {
	if l.ID == nil && l.Name == "" {
		return fmt.Errorf("session locator requires id or name")
	}
	if l.ID != nil && l.Name != "" {
		return fmt.Errorf("session locator cannot include both id and name")
	}
	return nil
}

// PeerRequest is a peer-to-peer messaging request.
type PeerRequest struct {
	OpID        string          `json:"op_id"`
	Type        string          `json:"type"`
	SenderCap   string          `json:"sender_cap,omitempty"`
	ObserverCap string          `json:"observer_cap,omitempty"`
	From        *SessionLocator `json:"from,omitempty"`
	To          *SessionLocator `json:"to,omitempty"`
	Session     *SessionLocator `json:"session,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	Body        string          `json:"body,omitempty"`
	Tail        *uint           `json:"tail,omitempty"`
	Delivery    string          `json:"delivery,omitempty"`
	TimeoutS    *uint64         `json:"timeout_seconds,omitempty"`
}

// AuthHello is the first frame on authenticated peer connections.
type AuthHello struct {
	Type              string `json:"type"`
	RuntimeCredential string `json:"runtime_credential"`
}

func (h AuthHello) Validate() error {
	if h.Type != "AuthHello" {
		return fmt.Errorf("unexpected auth hello type %q", h.Type)
	}
	if h.RuntimeCredential == "" {
		return fmt.Errorf("runtime credential is required")
	}
	return nil
}

// AuthAck confirms the bound authenticated principal for a peer connection.
type AuthAck struct {
	Type        string `json:"type"`
	NetworkID   string `json:"network_id,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (a AuthAck) Validate() error {
	switch a.Type {
	case "AuthAck":
		if a.NetworkID == "" || a.SubjectKind == "" || a.SubjectID == "" {
			return fmt.Errorf("auth ack is incomplete")
		}
		return nil
	case "Error":
		if a.Error == "" {
			return fmt.Errorf("error auth ack requires message")
		}
		return nil
	default:
		return fmt.Errorf("unexpected auth ack type %q", a.Type)
	}
}

// Validate ensures the request is structurally valid.
func (r PeerRequest) Validate() error {
	if r.OpID == "" {
		return fmt.Errorf("missing op_id")
	}
	switch r.Type {
	case "MsgSend":
		if r.To == nil {
			return fmt.Errorf("MsgSend requires to")
		}
		if err := r.To.Validate(); err != nil {
			return fmt.Errorf("invalid to locator: %w", err)
		}
	case "MsgRequest":
		if r.To == nil {
			return fmt.Errorf("MsgRequest requires to")
		}
		if err := r.To.Validate(); err != nil {
			return fmt.Errorf("invalid to locator: %w", err)
		}
	case "MsgRead":
		if r.Session == nil {
			return fmt.Errorf("MsgRead requires session")
		}
		if err := r.Session.Validate(); err != nil {
			return fmt.Errorf("invalid session locator: %w", err)
		}
	case "MsgListen":
		if r.Session != nil {
			if err := r.Session.Validate(); err != nil {
				return fmt.Errorf("invalid session locator: %w", err)
			}
		}
	case "MsgReply":
		if r.RequestID == "" {
			return fmt.Errorf("MsgReply requires request_id")
		}
	default:
		return fmt.Errorf("unsupported request type %q", r.Type)
	}
	if r.From != nil {
		if err := r.From.Validate(); err != nil {
			return fmt.Errorf("invalid from locator: %w", err)
		}
	}
	return nil
}

// PeerResponse is a peer-to-peer messaging response.
type PeerResponse struct {
	OpID      string                         `json:"op_id"`
	Type      string                         `json:"type"`
	MessageID string                         `json:"message_id,omitempty"`
	RequestID string                         `json:"request_id,omitempty"`
	ReplyBody string                         `json:"reply_body,omitempty"`
	From      *SessionLocator                `json:"from,omitempty"`
	Session   *SessionLocator                `json:"session,omitempty"`
	Event     *baseprotocol.SessionEvent     `json:"event,omitempty"`
	Messages  []baseprotocol.MessageResponse `json:"messages,omitempty"`
	Error     string                         `json:"error,omitempty"`
}

// Validate ensures the response is structurally valid.
func (r PeerResponse) Validate() error {
	if r.OpID == "" {
		return fmt.Errorf("missing op_id")
	}
	switch r.Type {
	case "MsgSent", "MsgReadResult", "MsgRequestResult", "MsgReplySent", "MsgListenAck", "Event", "Error":
		return nil
	default:
		return fmt.Errorf("unsupported response type %q", r.Type)
	}
}

func readJSONFrame[T any](r io.Reader) (*T, error) {
	frame, err := baseprotocol.ReadFrame(r)
	if err != nil {
		return nil, err
	}
	if frame == nil {
		return nil, nil
	}
	if frame.Type != baseprotocol.FrameControl {
		return nil, fmt.Errorf("expected control frame, got type 0x%02x", frame.Type)
	}
	var v T
	if err := json.Unmarshal(frame.Payload, &v); err != nil {
		return nil, fmt.Errorf("parsing frame payload: %w", err)
	}
	return &v, nil
}

func writeJSONFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return baseprotocol.WriteFrame(w, &baseprotocol.Frame{
		Type:    baseprotocol.FrameControl,
		Payload: payload,
	})
}

// ReadRequest reads a peer request from r.
func ReadRequest(r io.Reader) (*PeerRequest, error) {
	req, err := readJSONFrame[PeerRequest](r)
	if err != nil || req == nil {
		return req, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return req, nil
}

// WriteRequest writes a peer request to w.
func WriteRequest(w io.Writer, req *PeerRequest) error {
	if req == nil {
		return fmt.Errorf("nil request")
	}
	if err := req.Validate(); err != nil {
		return err
	}
	return writeJSONFrame(w, req)
}

// ReadResponse reads a peer response from r.
func ReadResponse(r io.Reader) (*PeerResponse, error) {
	resp, err := readJSONFrame[PeerResponse](r)
	if err != nil || resp == nil {
		return resp, err
	}
	if err := resp.Validate(); err != nil {
		return nil, err
	}
	return resp, nil
}

// WriteResponse writes a peer response to w.
func WriteResponse(w io.Writer, resp *PeerResponse) error {
	if resp == nil {
		return fmt.Errorf("nil response")
	}
	if err := resp.Validate(); err != nil {
		return err
	}
	return writeJSONFrame(w, resp)
}

func ReadAuthHello(r io.Reader) (*AuthHello, error) {
	hello, err := readJSONFrame[AuthHello](r)
	if err != nil || hello == nil {
		return hello, err
	}
	if err := hello.Validate(); err != nil {
		return nil, err
	}
	return hello, nil
}

func WriteAuthHello(w io.Writer, hello *AuthHello) error {
	if hello == nil {
		return fmt.Errorf("nil auth hello")
	}
	if err := hello.Validate(); err != nil {
		return err
	}
	return writeJSONFrame(w, hello)
}

func ReadAuthAck(r io.Reader) (*AuthAck, error) {
	ack, err := readJSONFrame[AuthAck](r)
	if err != nil || ack == nil {
		return ack, err
	}
	if err := ack.Validate(); err != nil {
		return nil, err
	}
	return ack, nil
}

func WriteAuthAck(w io.Writer, ack *AuthAck) error {
	if ack == nil {
		return fmt.Errorf("nil auth ack")
	}
	if err := ack.Validate(); err != nil {
		return err
	}
	return writeJSONFrame(w, ack)
}
