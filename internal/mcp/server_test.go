package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/codewiresh/codewire/internal/protocol"
)

func TestToolReportTaskValidation(t *testing.T) {
	_, err := toolReportTask(t.TempDir(), map[string]interface{}{
		"summary": "indexing tests",
		"state":   "working",
	})
	if err == nil || !strings.Contains(err.Error(), "missing session_id") {
		t.Fatalf("expected missing session_id error, got %v", err)
	}

	_, err = toolReportTask(t.TempDir(), map[string]interface{}{
		"session_id": float64(7),
		"state":      "working",
	})
	if err == nil || !strings.Contains(err.Error(), "missing summary") {
		t.Fatalf("expected missing summary error, got %v", err)
	}

	_, err = toolReportTask(t.TempDir(), map[string]interface{}{
		"session_id": float64(7),
		"summary":    "indexing tests",
	})
	if err == nil || !strings.Contains(err.Error(), "missing state") {
		t.Fatalf("expected missing state error, got %v", err)
	}
}

func TestToolReportTaskSendsNodeRequest(t *testing.T) {
	orig := nodeRequestFunc
	defer func() { nodeRequestFunc = orig }()

	var gotReq *protocol.Request
	nodeRequestFunc = func(dataDir string, req *protocol.Request) (*protocol.Response, error) {
		gotReq = req
		sessionID := uint32(7)
		return &protocol.Response{Type: "TaskReported", ID: &sessionID}, nil
	}

	got, err := toolReportTask(t.TempDir(), map[string]interface{}{
		"session_id": float64(7),
		"summary":    "indexing tests",
		"state":      "working",
	})
	if err != nil {
		t.Fatalf("toolReportTask: %v", err)
	}
	if got != "Reported task for session 7" {
		t.Fatalf("result = %q", got)
	}
	if gotReq == nil {
		t.Fatal("expected node request")
	}
	if gotReq.Type != "ReportTask" {
		t.Fatalf("Type = %q", gotReq.Type)
	}
	if gotReq.ID == nil || *gotReq.ID != 7 {
		t.Fatalf("ID = %v", gotReq.ID)
	}
	if gotReq.Summary != "indexing tests" {
		t.Fatalf("Summary = %q", gotReq.Summary)
	}
	if gotReq.State != "working" {
		t.Fatalf("State = %q", gotReq.State)
	}
}

func TestHandleToolCallDispatchesReportTask(t *testing.T) {
	orig := nodeRequestFunc
	defer func() { nodeRequestFunc = orig }()

	var gotReq *protocol.Request
	nodeRequestFunc = func(dataDir string, req *protocol.Request) (*protocol.Response, error) {
		gotReq = req
		sessionID := uint32(9)
		return &protocol.Response{Type: "TaskReported", ID: &sessionID}, nil
	}

	params := json.RawMessage(`{"name":"codewire_report_task","arguments":{"session_id":9,"summary":"ship task events","state":"working"}}`)
	got, err := handleToolCall(t.TempDir(), params)
	if err != nil {
		t.Fatalf("handleToolCall: %v", err)
	}
	if got != "Reported task for session 9" {
		t.Fatalf("result = %q", got)
	}
	if gotReq == nil {
		t.Fatal("expected dispatched node request")
	}
	if gotReq.Type != "ReportTask" {
		t.Fatalf("Type = %q", gotReq.Type)
	}
}
