package relay

import (
	"context"
	"testing"
	"time"

	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/codewiresh/codewire/internal/store"
)

func TestNATSTaskStoreIntegrationWithJetStream(t *testing.T) {
	srv := startTestJetStreamServer(t)
	if srv == nil {
		t.Skip("jetstream server unavailable in sandbox")
	}
	defer func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	}()

	tasks, err := NewNATSTaskStore(context.Background(), RelayConfig{
		NATSURL: srv.ClientURL(),
	})
	if err != nil {
		t.Fatalf("NewNATSTaskStore: %v", err)
	}
	defer tasks.Close()

	node := store.NodeRecord{
		NetworkID: "project_alpha",
		Name:      "builder",
	}
	first, err := tasks.PublishNodeTaskReport(context.Background(), node, TaskReportMessage{
		EventID:     "task_123",
		SessionID:   7,
		SessionName: "planner",
		Summary:     "ship jetstream test",
		State:       "working",
		Timestamp:   "2026-04-08T15:04:05Z",
	})
	if err != nil {
		t.Fatalf("PublishNodeTaskReport(first): %v", err)
	}
	if first == nil || first.StreamSeq == 0 {
		t.Fatalf("first latest = %#v", first)
	}

	listed, err := tasks.ListLatestTasks(context.Background(), TaskFilter{NetworkID: "project_alpha"})
	if err != nil {
		t.Fatalf("ListLatestTasks: %v", err)
	}
	if len(listed) != 1 || listed[0].EventID != "task_123" {
		t.Fatalf("listed = %#v", listed)
	}

	watcher, err := tasks.WatchTasks(context.Background(), TaskFilter{NetworkID: "project_alpha"}, 0)
	if err != nil {
		t.Fatalf("WatchTasks: %v", err)
	}
	defer watcher.Close()

	select {
	case ev := <-watcher.Events():
		if ev.Event.EventID != "task_123" || ev.Event.Summary != "ship jetstream test" {
			t.Fatalf("first watch event = %#v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for replay event")
	}

	_, err = tasks.PublishNodeTaskReport(context.Background(), node, TaskReportMessage{
		EventID:     "task_124",
		SessionID:   7,
		SessionName: "planner",
		Summary:     "ship live event",
		State:       "complete",
		Timestamp:   "2026-04-08T15:05:05Z",
	})
	if err != nil {
		t.Fatalf("PublishNodeTaskReport(second): %v", err)
	}

	select {
	case ev := <-watcher.Events():
		if ev.Event.EventID != "task_124" || ev.Event.State != "complete" {
			t.Fatalf("second watch event = %#v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for live event")
	}

	earliest, err := tasks.EarliestSeq(context.Background(), "project_alpha")
	if err != nil {
		t.Fatalf("EarliestSeq: %v", err)
	}
	if earliest == 0 {
		t.Fatalf("earliest = %d, want > 0", earliest)
	}
}

func startTestJetStreamServer(t *testing.T) *natssrv.Server {
	t.Helper()

	opts := &natssrv.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := natssrv.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(3 * time.Second) {
		srv.Shutdown()
		srv.WaitForShutdown()
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(srv.ClientURL(), nats.Timeout(time.Second))
		if err == nil {
			nc.Close()
			return srv
		}
		time.Sleep(100 * time.Millisecond)
	}
	srv.Shutdown()
	srv.WaitForShutdown()
	return nil
}
