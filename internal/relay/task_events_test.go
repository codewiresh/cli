package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/oauth"
	"github.com/codewiresh/codewire/internal/store"
)

type fakeTaskWatcher struct {
	ch chan TaskWatchEvent
}

func (w *fakeTaskWatcher) Events() <-chan TaskWatchEvent {
	return w.ch
}

func (w *fakeTaskWatcher) Close() error {
	return nil
}

type fakeTaskAPIStore struct {
	latest      []LatestTaskValue
	listFilter  TaskFilter
	watchFilter TaskFilter
	watchAfter  uint64
	earliest    uint64
	watcher     TaskWatcher
}

func (s *fakeTaskAPIStore) PublishNodeTaskReport(context.Context, store.NodeRecord, TaskReportMessage) (*LatestTaskValue, error) {
	return nil, nil
}

func (s *fakeTaskAPIStore) ListLatestTasks(_ context.Context, filter TaskFilter) ([]LatestTaskValue, error) {
	s.listFilter = filter
	return s.latest, nil
}

func (s *fakeTaskAPIStore) WatchTasks(_ context.Context, filter TaskFilter, afterSeq uint64) (TaskWatcher, error) {
	s.watchFilter = filter
	s.watchAfter = afterSeq
	if s.watcher == nil {
		return closedTaskWatcher{}, nil
	}
	return s.watcher, nil
}

func (s *fakeTaskAPIStore) EarliestSeq(context.Context, string) (uint64, error) {
	return s.earliest, nil
}

func addMembership(t *testing.T, st store.Store, networkID string) {
	t.Helper()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: networkID,
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert: %v", err)
	}
}

func authRequest(req *http.Request) *http.Request {
	return req.WithContext(oauth.WithAuth(req.Context(), &oauth.AuthIdentity{
		UserID:   101,
		Username: "owner",
	}))
}

func TestTaskListHandlerReturnsSnapshot(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()
	addMembership(t, st, "project_alpha")

	tasks := &fakeTaskAPIStore{
		latest: []LatestTaskValue{{
			EventID:     "task_123",
			StreamSeq:   10,
			NetworkID:   "project_alpha",
			NodeName:    "builder",
			SessionID:   7,
			SessionName: "planner",
			Summary:     "ship task api",
			State:       "working",
			Timestamp:   "2026-04-08T15:04:05Z",
		}},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?network_id=project_alpha&node=builder&session_id=7&state=working", nil)
	rec := httptest.NewRecorder()
	taskListHandler(st, tasks).ServeHTTP(rec, authRequest(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if tasks.listFilter.NetworkID != "project_alpha" || tasks.listFilter.NodeName != "builder" {
		t.Fatalf("filter = %#v", tasks.listFilter)
	}
	if tasks.listFilter.SessionID == nil || *tasks.listFilter.SessionID != 7 {
		t.Fatalf("session filter = %#v", tasks.listFilter.SessionID)
	}
	if tasks.listFilter.State != "working" {
		t.Fatalf("state filter = %q", tasks.listFilter.State)
	}

	var got []LatestTaskValue
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].EventID != "task_123" {
		t.Fatalf("response = %#v", got)
	}
}

func TestTaskListHandlerRequiresMembership(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?network_id=project_alpha", nil)
	rec := httptest.NewRecorder()
	taskListHandler(st, &fakeTaskAPIStore{}).ServeHTTP(rec, authRequest(req))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestTaskEventsHandlerEmitsResetAndReplay(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()
	addMembership(t, st, "project_alpha")

	watcher := &fakeTaskWatcher{ch: make(chan TaskWatchEvent, 1)}
	watcher.ch <- TaskWatchEvent{
		Seq: 10,
		Event: TaskEvent{
			EventID:     "task_123",
			NetworkID:   "project_alpha",
			NodeName:    "builder",
			SessionID:   7,
			SessionName: "planner",
			Summary:     "ship task api",
			State:       "working",
			Timestamp:   "2026-04-08T15:04:05Z",
		},
	}
	close(watcher.ch)

	tasks := &fakeTaskAPIStore{
		earliest: 10,
		watcher:  watcher,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/events?network_id=project_alpha&node=builder", nil)
	req.Header.Set("Last-Event-ID", "3")
	rec := httptest.NewRecorder()
	taskEventsHandler(st, tasks).ServeHTTP(rec, authRequest(req))

	if tasks.watchAfter != 9 {
		t.Fatalf("watchAfter = %d, want 9", tasks.watchAfter)
	}
	if tasks.watchFilter.NodeName != "builder" || tasks.watchFilter.NetworkID != "project_alpha" {
		t.Fatalf("watchFilter = %#v", tasks.watchFilter)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: stream.reset") {
		t.Fatalf("expected stream.reset in %q", body)
	}
	if !strings.Contains(body, "event: task.report") {
		t.Fatalf("expected task.report in %q", body)
	}
	if !strings.Contains(body, `"event_id":"task_123"`) {
		t.Fatalf("expected task event payload in %q", body)
	}
}

func TestTaskEventsHandlerRejectsBadCursor(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()
	addMembership(t, st, "project_alpha")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/events?network_id=project_alpha", nil)
	req.Header.Set("Last-Event-ID", "nope")
	rec := httptest.NewRecorder()
	taskEventsHandler(st, &fakeTaskAPIStore{}).ServeHTTP(rec, authRequest(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
