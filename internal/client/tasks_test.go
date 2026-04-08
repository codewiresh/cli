package client

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestListTasksUsesRelayAuthAndParsesResponse(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
relay_url = "https://relay.example"
relay_selected_network = "project_alpha"
`)

	oldClient := relayHTTPClient
	t.Cleanup(func() { relayHTTPClient = oldClient })
	relayHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://relay.example/api/v1/tasks?network_id=project_alpha&node=builder&state=working" {
				t.Fatalf("url = %q", req.URL.String())
			}
			if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("authorization = %q", got)
			}
			return jsonResponse(http.StatusOK, `[{"event_id":"task_123","stream_seq":12,"network_id":"project_alpha","node_name":"builder","session_id":7,"session_name":"planner","summary":"ship client","state":"working","timestamp":"2026-04-08T15:04:05Z"}]`), nil
		}),
	}

	got, err := ListTasks(dir, RelayAuthOptions{AuthToken: "test-token"}, WatchTasksOptions{
		NodeName: "builder",
		State:    "working",
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 || got[0].EventID != "task_123" {
		t.Fatalf("tasks = %#v", got)
	}
}

func TestWatchTasksParsesStreamAndPersistsCursor(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
relay_url = "https://relay.example"
relay_selected_network = "project_alpha"
`)

	oldClient := relayHTTPClient
	t.Cleanup(func() { relayHTTPClient = oldClient })
	relayHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Last-Event-ID"); got != "" {
				t.Fatalf("initial Last-Event-ID = %q", got)
			}
			body := "id: 10\n" +
				"event: task.report\n" +
				"data: {\"seq\":10,\"type\":\"task.report\",\"event_id\":\"task_123\",\"network_id\":\"project_alpha\",\"node_name\":\"builder\",\"session_id\":7,\"summary\":\"ship client\",\"state\":\"working\",\"timestamp\":\"2026-04-08T15:04:05Z\"}\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			}, nil
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan TaskEvent, 1)
	done := make(chan error, 1)
	go func() {
		done <- WatchTasks(ctx, dir, RelayAuthOptions{AuthToken: "test-token"}, WatchTasksOptions{}, out)
	}()

	select {
	case ev := <-out:
		if ev.EventID != "task_123" || ev.Seq != 10 {
			t.Fatalf("event = %#v", ev)
		}
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task event")
	}

	if err := <-done; err != nil {
		t.Fatalf("WatchTasks: %v", err)
	}
	last, err := taskLastEventID(dir, "project_alpha")
	if err != nil {
		t.Fatalf("taskLastEventID: %v", err)
	}
	if last != "10" {
		t.Fatalf("last event id = %q", last)
	}
}

func TestTaskEventsURLIncludesSessionAndState(t *testing.T) {
	sessionID := uint32(7)
	got := taskEventsURL("https://relay.example/", WatchTasksOptions{
		NetworkID: "project_alpha",
		NodeName:  "builder",
		SessionID: &sessionID,
		State:     "working",
	})
	if got != "https://relay.example/api/v1/tasks/events?network_id=project_alpha&node=builder&session_id=7&state=working" {
		t.Fatalf("taskEventsURL = %q", got)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(dir+"/config.toml", []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
