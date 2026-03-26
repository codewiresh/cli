package peer

import (
	"bytes"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()

	toID := uint32(17)
	req := &PeerRequest{
		OpID: "op-1",
		Type: "MsgSend",
		To:   &SessionLocator{Node: "dev-2", ID: &toID},
		Body: "hello",
	}

	var buf bytes.Buffer
	if err := WriteRequest(&buf, req); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	got, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got.OpID != req.OpID || got.Type != req.Type || got.To == nil || got.To.ID == nil || *got.To.ID != toID {
		t.Fatalf("unexpected request: %+v", got)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	t.Parallel()

	resp := &PeerResponse{
		OpID:      "op-1",
		Type:      "MsgSent",
		MessageID: "msg-1",
	}

	var buf bytes.Buffer
	if err := WriteResponse(&buf, resp); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}

	got, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if got.OpID != resp.OpID || got.Type != resp.Type || got.MessageID != resp.MessageID {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestRequestValidateMissingOpID(t *testing.T) {
	t.Parallel()

	req := &PeerRequest{
		Type: "MsgRead",
		Session: &SessionLocator{
			Name: "coder",
		},
	}
	if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "missing op_id") {
		t.Fatalf("Validate error = %v, want missing op_id", err)
	}
}

func TestLocatorRejectsIDAndName(t *testing.T) {
	t.Parallel()

	id := uint32(1)
	loc := SessionLocator{ID: &id, Name: "coder"}
	if err := loc.Validate(); err == nil || !strings.Contains(err.Error(), "both id and name") {
		t.Fatalf("Validate error = %v, want both id and name", err)
	}
}
