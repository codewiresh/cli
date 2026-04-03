package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/protocol"
	"github.com/codewiresh/codewire/internal/session"
)

// Server handles peer messaging requests against a local SessionManager.
type Server struct {
	Sessions          *session.SessionManager
	NodeName          string
	AuthorizePeer     func(context.Context, string) (*AuthenticatedPeer, error)
	AuthorizeSender   func(context.Context, string, *SessionLocator, string) (*AuthorizedSender, error)
	AuthorizeDelivery func(context.Context, *AuthorizedSender, *SessionLocator, string) error
	AuthorizeObserver func(context.Context, *AuthenticatedPeer, string, *SessionLocator, string) error
	// RequireRemoteSenderAuth enforces capability-based auth for remote peer RPC.
	// When enabled, remote writes require a verified sender delegation and remote
	// inbox/listen operations are rejected until explicit read capabilities exist.
	RequireRemoteSenderAuth bool
}

// AuthenticatedPeer is the authenticated remote principal bound to a connection.
type AuthenticatedPeer struct {
	NetworkID   string
	SubjectKind string
	SubjectID   string
}

// AuthorizedSender is the verified remote sender identity for cross-node traffic.
type AuthorizedSender struct {
	DisplayName  string
	SessionID    *uint32
	SessionName  string
	SourceGroups []string
}

// Serve accepts connections until the listener is closed or ctx is canceled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Sessions == nil {
		return fmt.Errorf("sessions is required")
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return err
		}
		go s.ServeConn(ctx, conn)
	}
}

// ServeConn serves peer requests on a single connection.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	principal, err := s.authenticateConn(ctx, conn)
	if err != nil {
		return
	}
	for {
		req, err := ReadRequest(conn)
		if err != nil {
			return
		}
		if req == nil {
			return
		}
		if req.Type == "MsgListen" {
			s.handleMsgListen(ctx, principal, conn, req)
			return
		}
		resp := s.handle(ctx, principal, req)
		if writeErr := WriteResponse(conn, resp); writeErr != nil {
			return
		}
	}
}

func (s *Server) authenticateConn(ctx context.Context, conn net.Conn) (*AuthenticatedPeer, error) {
	authorizePeer := s.AuthorizePeer
	if authorizePeer == nil {
		return nil, nil
	}

	hello, err := ReadAuthHello(conn)
	if err != nil {
		_ = WriteAuthAck(conn, &AuthAck{Type: "Error", Error: "peer authentication failed"})
		return nil, err
	}
	if hello == nil {
		_ = WriteAuthAck(conn, &AuthAck{Type: "Error", Error: "peer authentication failed"})
		return nil, fmt.Errorf("peer authentication failed")
	}
	principal, err := authorizePeer(ctx, hello.RuntimeCredential)
	if err != nil {
		_ = WriteAuthAck(conn, &AuthAck{Type: "Error", Error: err.Error()})
		return nil, err
	}
	if principal == nil {
		_ = WriteAuthAck(conn, &AuthAck{Type: "Error", Error: "peer authentication failed"})
		return nil, fmt.Errorf("peer authentication failed")
	}
	if err := WriteAuthAck(conn, &AuthAck{
		Type:        "AuthAck",
		NetworkID:   principal.NetworkID,
		SubjectKind: principal.SubjectKind,
		SubjectID:   principal.SubjectID,
	}); err != nil {
		return nil, err
	}
	return principal, nil
}

func (s *Server) handle(ctx context.Context, principal *AuthenticatedPeer, req *PeerRequest) *PeerResponse {
	switch req.Type {
	case "MsgSend":
		return s.handleMsgSend(ctx, req)
	case "MsgRequest":
		return s.handleMsgRequest(ctx, req)
	case "MsgRead":
		return s.handleMsgRead(ctx, principal, req)
	case "MsgListen":
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: "MsgListen must be served as a stream"}
	case "MsgReply":
		return s.handleMsgReply(ctx, req)
	default:
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: fmt.Sprintf("unsupported request type %q", req.Type)}
	}
}

func (s *Server) handleMsgSend(ctx context.Context, req *PeerRequest) *PeerResponse {
	toID, err := s.resolveLocal(req.To)
	if err != nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
	}
	if s.RequireRemoteSenderAuth && req.From == nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: "remote send requires verified sender delegation"}
	}

	var msgID string
	if req.From != nil {
		authorized, err := s.authorizeSender(ctx, "msg", req)
		if err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
		if err := s.authorizeDelivery(ctx, authorized, req.To, "msg"); err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
		msgID, err = s.Sessions.SendRemoteMessage(authorized.DisplayName, toID, req.Body)
	} else {
		msgID, err = s.Sessions.SendMessage(0, toID, req.Body)
	}
	if err != nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
	}

	return &PeerResponse{
		OpID:      req.OpID,
		Type:      "MsgSent",
		MessageID: msgID,
	}
}

func (s *Server) handleMsgRead(ctx context.Context, principal *AuthenticatedPeer, req *PeerRequest) *PeerResponse {
	if s.RequireRemoteSenderAuth {
		if principal == nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: "peer authentication is required"}
		}
		if req.Session == nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: "remote inbox reads require a session"}
		}
		if err := s.authorizeObserver(ctx, principal, "msg.read", req.Session, req.ObserverCap); err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
	}
	sessionID, err := s.resolveLocal(req.Session)
	if err != nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
	}

	tail := 50
	if req.Tail != nil {
		tail = int(*req.Tail)
	}

	events, err := s.Sessions.ReadMessages(sessionID, tail)
	if err != nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
	}

	messages := make([]protocol.MessageResponse, 0, len(events))
	for _, e := range events {
		mr := eventToMessageResponse(e)
		if mr != nil {
			messages = append(messages, *mr)
		}
	}

	return &PeerResponse{
		OpID:     req.OpID,
		Type:     "MsgReadResult",
		Session:  req.Session,
		Messages: messages,
	}
}

func (s *Server) handleMsgRequest(ctx context.Context, req *PeerRequest) *PeerResponse {
	toID, err := s.resolveLocal(req.To)
	if err != nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
	}
	if s.RequireRemoteSenderAuth && req.From == nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: "remote request requires verified sender delegation"}
	}

	var (
		requestID string
		replyCh   <-chan session.ReplyData
		fromLabel string
	)
	if req.From != nil {
		authorized, err := s.authorizeSender(ctx, "request", req)
		if err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
		if err := s.authorizeDelivery(ctx, authorized, req.To, "request"); err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
		fromLabel = authorized.DisplayName
		requestID, replyCh, err = s.Sessions.SendRemoteRequest(authorized.DisplayName, toID, req.Body)
	} else {
		requestID, replyCh, err = s.Sessions.SendRequest(0, toID, req.Body)
	}
	if err != nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
	}

	if deliveryIncludesPTY(req.Delivery) {
		if ptyErr := s.Sessions.DeliverRequestPrompt(toID, requestID, fromLabel, 0, req.Body); ptyErr != nil {
			s.Sessions.CleanupRequest(requestID)
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: fmt.Sprintf("PTY injection failed: %v", ptyErr)}
		}
	}

	timeoutSecs := uint64(60)
	if req.TimeoutS != nil && *req.TimeoutS > 0 {
		timeoutSecs = *req.TimeoutS
	}
	timer := time.NewTimer(time.Duration(timeoutSecs) * time.Second)
	defer timer.Stop()

	select {
	case reply := <-replyCh:
		from := &SessionLocator{Name: reply.FromName}
		if reply.From != 0 {
			id := reply.From
			from.ID = &id
			if from.Name == "" {
				from.Name = ""
			}
		}
		if from.ID == nil && from.Name == "" {
			from = nil
		}
		return &PeerResponse{
			OpID:      req.OpID,
			Type:      "MsgRequestResult",
			RequestID: requestID,
			ReplyBody: reply.Body,
			From:      from,
		}
	case <-timer.C:
		s.Sessions.CleanupRequest(requestID)
		return &PeerResponse{
			OpID:  req.OpID,
			Type:  "Error",
			Error: fmt.Sprintf("request %s timed out after %ds", requestID, timeoutSecs),
		}
	}
}

func (s *Server) authorizeSender(ctx context.Context, verb string, req *PeerRequest) (*AuthorizedSender, error) {
	if req.From == nil {
		return nil, nil
	}
	if s.AuthorizeSender == nil {
		return nil, fmt.Errorf("remote session sender auth is not configured")
	}
	return s.AuthorizeSender(ctx, verb, req.From, req.SenderCap)
}

func (s *Server) authorizeDelivery(ctx context.Context, sender *AuthorizedSender, to *SessionLocator, verb string) error {
	if s.AuthorizeDelivery == nil {
		return nil
	}
	return s.AuthorizeDelivery(ctx, sender, to, verb)
}

func (s *Server) handleMsgReply(ctx context.Context, req *PeerRequest) *PeerResponse {
	if s.RequireRemoteSenderAuth && req.From == nil {
		return &PeerResponse{OpID: req.OpID, Type: "Error", Error: "remote reply requires verified sender delegation"}
	}
	if req.From != nil {
		authorized, err := s.authorizeSender(ctx, "reply", req)
		if err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
		if err := s.Sessions.SendRemoteReply(authorized.DisplayName, authorized.SessionID, req.RequestID, req.Body); err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
	} else {
		if err := s.Sessions.SendReply(0, req.RequestID, req.Body); err != nil {
			return &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()}
		}
	}
	return &PeerResponse{
		OpID:      req.OpID,
		Type:      "MsgReplySent",
		RequestID: req.RequestID,
	}
}

func (s *Server) handleMsgListen(ctx context.Context, principal *AuthenticatedPeer, conn net.Conn, req *PeerRequest) {
	if s.RequireRemoteSenderAuth {
		if principal == nil {
			_ = WriteResponse(conn, &PeerResponse{OpID: req.OpID, Type: "Error", Error: "peer authentication is required"})
			return
		}
		if req.Session == nil {
			_ = WriteResponse(conn, &PeerResponse{OpID: req.OpID, Type: "Error", Error: "remote message subscriptions require a session"})
			return
		}
		if err := s.authorizeObserver(ctx, principal, "msg.listen", req.Session, req.ObserverCap); err != nil {
			_ = WriteResponse(conn, &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()})
			return
		}
	}
	var sessionID *uint32
	if req.Session != nil {
		resolved, err := s.resolveLocal(req.Session)
		if err != nil {
			_ = WriteResponse(conn, &PeerResponse{OpID: req.OpID, Type: "Error", Error: err.Error()})
			return
		}
		sessionID = &resolved
	}

	eventTypes := []session.EventType{
		session.EventDirectMessage,
		session.EventRequest,
		session.EventReply,
	}
	sub := s.Sessions.Subscriptions.Subscribe(sessionID, nil, eventTypes)
	defer s.Sessions.Subscriptions.Unsubscribe(sub.ID)

	if err := WriteResponse(conn, &PeerResponse{OpID: req.OpID, Type: "MsgListenAck", Session: req.Session}); err != nil {
		return
	}

	for se := range sub.Ch {
		sessionIDValue := se.SessionID
		sessionLocator := &SessionLocator{Node: s.NodeName, ID: &sessionIDValue}
		resp := &PeerResponse{
			OpID:    req.OpID,
			Type:    "Event",
			Session: sessionLocator,
			Event: &protocol.SessionEvent{
				Timestamp: se.Event.Timestamp.Format(time.RFC3339Nano),
				EventType: string(se.Event.Type),
				Data:      se.Event.Data,
			},
		}
		if err := WriteResponse(conn, resp); err != nil {
			return
		}
	}
}

func (s *Server) authorizeObserver(ctx context.Context, principal *AuthenticatedPeer, verb string, session *SessionLocator, observerCap string) error {
	if s.AuthorizeObserver == nil {
		return fmt.Errorf("remote observer auth is not configured")
	}
	return s.AuthorizeObserver(ctx, principal, verb, session, observerCap)
}

func (s *Server) resolveLocal(locator *SessionLocator) (uint32, error) {
	if locator == nil {
		return 0, fmt.Errorf("missing session locator")
	}
	if locator.Node != "" && s.NodeName != "" && locator.Node != s.NodeName {
		return 0, fmt.Errorf("session targets node %q, not %q", locator.Node, s.NodeName)
	}
	if locator.ID != nil {
		return *locator.ID, nil
	}
	return s.Sessions.ResolveByName(strings.TrimPrefix(locator.Name, "@"))
}

func eventToMessageResponse(e session.Event) *protocol.MessageResponse {
	switch e.Type {
	case session.EventDirectMessage:
		var d session.DirectMessageData
		if json.Unmarshal(e.Data, &d) != nil {
			return nil
		}
		return &protocol.MessageResponse{
			MessageID: d.MessageID,
			Timestamp: e.Timestamp.Format(time.RFC3339Nano),
			From:      d.From,
			FromName:  d.FromName,
			To:        d.To,
			ToName:    d.ToName,
			Body:      d.Body,
			EventType: string(e.Type),
		}
	case session.EventRequest:
		var d session.RequestData
		if json.Unmarshal(e.Data, &d) != nil {
			return nil
		}
		return &protocol.MessageResponse{
			MessageID: d.RequestID,
			Timestamp: e.Timestamp.Format(time.RFC3339Nano),
			From:      d.From,
			FromName:  d.FromName,
			To:        d.To,
			ToName:    d.ToName,
			Body:      d.Body,
			EventType: string(e.Type),
			RequestID: d.RequestID,
		}
	case session.EventReply:
		var d session.ReplyData
		if json.Unmarshal(e.Data, &d) != nil {
			return nil
		}
		return &protocol.MessageResponse{
			MessageID: d.RequestID,
			Timestamp: e.Timestamp.Format(time.RFC3339Nano),
			From:      d.From,
			FromName:  d.FromName,
			Body:      d.Body,
			EventType: string(e.Type),
			RequestID: d.RequestID,
		}
	default:
		return nil
	}
}

func deliveryIncludesPTY(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "pty", "both":
		return true
	default:
		return false
	}
}
