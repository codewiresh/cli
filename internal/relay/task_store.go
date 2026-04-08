package relay

import (
	"context"

	"github.com/codewiresh/codewire/internal/store"
)

type TaskFilter struct {
	NetworkID string
	NodeName  string
	SessionID *uint32
	State     string
}

type TaskWatchEvent struct {
	Seq   uint64
	Event TaskEvent
}

type TaskWatcher interface {
	Events() <-chan TaskWatchEvent
	Close() error
}

// TaskStore receives authenticated task reports from node websocket sessions
// and serves the relay's task snapshot/watch APIs.
type TaskStore interface {
	PublishNodeTaskReport(ctx context.Context, node store.NodeRecord, msg TaskReportMessage) (*LatestTaskValue, error)
	ListLatestTasks(ctx context.Context, filter TaskFilter) ([]LatestTaskValue, error)
	WatchTasks(ctx context.Context, filter TaskFilter, afterSeq uint64) (TaskWatcher, error)
	EarliestSeq(ctx context.Context, networkID string) (uint64, error)
}

type noopTaskStore struct{}

func (noopTaskStore) PublishNodeTaskReport(context.Context, store.NodeRecord, TaskReportMessage) (*LatestTaskValue, error) {
	return nil, nil
}

func (noopTaskStore) ListLatestTasks(context.Context, TaskFilter) ([]LatestTaskValue, error) {
	return []LatestTaskValue{}, nil
}

func (noopTaskStore) WatchTasks(context.Context, TaskFilter, uint64) (TaskWatcher, error) {
	return closedTaskWatcher{}, nil
}

func (noopTaskStore) EarliestSeq(context.Context, string) (uint64, error) {
	return 0, nil
}

func normalizeTaskStore(tasks TaskStore) TaskStore {
	if tasks == nil {
		return noopTaskStore{}
	}
	return tasks
}

type closedTaskWatcher struct{}

func (closedTaskWatcher) Events() <-chan TaskWatchEvent {
	ch := make(chan TaskWatchEvent)
	close(ch)
	return ch
}

func (closedTaskWatcher) Close() error {
	return nil
}
