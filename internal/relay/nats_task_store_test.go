package relay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/codewiresh/codewire/internal/store"
)

type fakeTaskEventPublisher struct {
	subject string
	eventID string
	payload []byte
	seq     uint64
	err     error
	calls   int
}

func (p *fakeTaskEventPublisher) PublishTaskEvent(_ context.Context, subject, eventID string, payload []byte) (uint64, error) {
	p.calls++
	p.subject = subject
	p.eventID = eventID
	p.payload = append([]byte(nil), payload...)
	if p.err != nil {
		return 0, p.err
	}
	return p.seq, nil
}

type fakeTaskLatestWriter struct {
	key     string
	payload []byte
	err     error
	calls   int
}

func (w *fakeTaskLatestWriter) PutLatestTask(_ context.Context, key string, payload []byte) error {
	w.calls++
	w.key = key
	w.payload = append([]byte(nil), payload...)
	return w.err
}

func TestNATSTaskStorePublishNodeTaskReportPublishesAndUpdatesLatest(t *testing.T) {
	publisher := &fakeTaskEventPublisher{seq: 42}
	latest := &fakeTaskLatestWriter{}
	tasks := &NATSTaskStore{
		publisher:        publisher,
		latest:           latest,
		subjectRoot:      "tasks",
		eventsStreamName: "TASK_EVENTS",
		latestBucketName: "TASK_LATEST",
	}

	got, err := tasks.PublishNodeTaskReport(context.Background(), store.NodeRecord{
		NetworkID: "network_a",
		Name:      "builder-1",
	}, TaskReportMessage{
		EventID:     "task_123",
		SessionID:   7,
		SessionName: "planner",
		Summary:     "ship relay store",
		State:       "working",
		Timestamp:   "2026-04-08T17:04:05+02:00",
	})
	if err != nil {
		t.Fatalf("PublishNodeTaskReport: %v", err)
	}

	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
	if publisher.subject != "tasks.network_a.builder-1.s7" {
		t.Fatalf("subject = %q", publisher.subject)
	}
	if publisher.eventID != "task_123" {
		t.Fatalf("eventID = %q", publisher.eventID)
	}

	var event TaskEvent
	if err := json.Unmarshal(publisher.payload, &event); err != nil {
		t.Fatalf("unmarshal task event: %v", err)
	}
	if event.NetworkID != "network_a" || event.NodeName != "builder-1" {
		t.Fatalf("event routing = %#v", event)
	}
	if event.Timestamp != "2026-04-08T15:04:05Z" {
		t.Fatalf("event timestamp = %q", event.Timestamp)
	}

	if latest.calls != 1 {
		t.Fatalf("latest calls = %d, want 1", latest.calls)
	}
	if latest.key != "network_a.builder-1.s7" {
		t.Fatalf("key = %q", latest.key)
	}

	var latestValue LatestTaskValue
	if err := json.Unmarshal(latest.payload, &latestValue); err != nil {
		t.Fatalf("unmarshal latest task value: %v", err)
	}
	if latestValue.StreamSeq != 42 {
		t.Fatalf("stream seq = %d", latestValue.StreamSeq)
	}
	if got == nil || got.StreamSeq != 42 {
		t.Fatalf("returned latest = %#v", got)
	}
}

func TestNATSTaskStorePublishNodeTaskReportValidation(t *testing.T) {
	publisher := &fakeTaskEventPublisher{seq: 1}
	latest := &fakeTaskLatestWriter{}
	tasks := &NATSTaskStore{
		publisher:   publisher,
		latest:      latest,
		subjectRoot: "tasks",
	}

	_, err := tasks.PublishNodeTaskReport(context.Background(), store.NodeRecord{
		NetworkID: "network_a",
		Name:      "builder-1",
	}, TaskReportMessage{
		EventID:   "task_123",
		SessionID: 7,
		Summary:   "bad state",
		State:     "queued",
		Timestamp: "2026-04-08T15:04:05Z",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0", publisher.calls)
	}
	if latest.calls != 0 {
		t.Fatalf("latest calls = %d, want 0", latest.calls)
	}
}

func TestNATSTaskStorePublishNodeTaskReportLatestWriteError(t *testing.T) {
	publisher := &fakeTaskEventPublisher{seq: 12}
	latest := &fakeTaskLatestWriter{err: errors.New("kv down")}
	tasks := &NATSTaskStore{
		publisher:   publisher,
		latest:      latest,
		subjectRoot: "tasks",
	}

	_, err := tasks.PublishNodeTaskReport(context.Background(), store.NodeRecord{
		NetworkID: "network_a",
		Name:      "builder-1",
	}, TaskReportMessage{
		EventID:   "task_123",
		SessionID: 7,
		Summary:   "persist latest",
		State:     "working",
		Timestamp: "2026-04-08T15:04:05Z",
	})
	if err == nil || err.Error() != "update latest task value: kv down" {
		t.Fatalf("err = %v", err)
	}
}

func TestTaskStoreHelpersDefaultNames(t *testing.T) {
	cfg := RelayConfig{}
	if got := taskSubjectRoot(cfg); got != defaultTaskSubjectRoot {
		t.Fatalf("subject root = %q", got)
	}
	if got := taskEventsStreamName(cfg); got != defaultTaskEventsStream {
		t.Fatalf("events stream = %q", got)
	}
	if got := taskLatestBucketName(cfg); got != defaultTaskLatestBucket {
		t.Fatalf("latest bucket = %q", got)
	}
}

func TestSubjectFilterSupportsSessionAcrossNodes(t *testing.T) {
	sessionID := uint32(7)
	got := subjectFilter("tasks", TaskFilter{
		NetworkID: "network_a",
		SessionID: &sessionID,
	})
	if got != "tasks.network_a.*.s7" {
		t.Fatalf("subject filter = %q", got)
	}
}
