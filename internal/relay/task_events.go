package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/oauth"
	"github.com/codewiresh/codewire/internal/store"
)

func taskListHandler(st store.Store, tasks TaskStore) http.HandlerFunc {
	tasks = normalizeTaskStore(tasks)
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		filter, err := taskFilterFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok, err := requireMembership(r.Context(), st, filter.NetworkID, identity); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		} else if !ok {
			writeMembershipRequired(w)
			return
		}

		values, err := tasks.ListLatestTasks(r.Context(), filter)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(values)
	}
}

func taskEventsHandler(st store.Store, tasks TaskStore) http.HandlerFunc {
	tasks = normalizeTaskStore(tasks)
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		filter, err := taskFilterFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok, err := requireMembership(r.Context(), st, filter.NetworkID, identity); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		} else if !ok {
			writeMembershipRequired(w)
			return
		}

		lastSeq, err := parseTaskLastEventID(r.Header.Get("Last-Event-ID"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		earliest, err := tasks.EarliestSeq(r.Context(), filter.NetworkID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		afterSeq := lastSeq
		if lastSeq > 0 && earliest > lastSeq+1 {
			reset := TaskAPIEvent{
				Seq:       earliest,
				Type:      "stream.reset",
				NetworkID: filter.NetworkID,
			}
			if err := writeTaskSSEEvent(w, flusher, "stream.reset", reset); err != nil {
				return
			}
			afterSeq = earliest - 1
		}

		watcher, err := tasks.WatchTasks(r.Context(), filter, afterSeq)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer watcher.Close()

		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-watcher.Events():
				if !ok {
					return
				}
				if err := writeTaskSSEEvent(w, flusher, "task.report", taskAPIEventFromWatch(ev)); err != nil {
					return
				}
			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func taskFilterFromRequest(r *http.Request) (TaskFilter, error) {
	networkID, err := requiredNetworkID(r.URL.Query().Get("network_id"))
	if err != nil {
		return TaskFilter{}, err
	}

	filter := TaskFilter{
		NetworkID: networkID,
		NodeName:  strings.TrimSpace(r.URL.Query().Get("node")),
		State:     strings.TrimSpace(r.URL.Query().Get("state")),
	}
	if filter.State != "" && !isValidTaskState(filter.State) {
		return TaskFilter{}, fmt.Errorf("invalid state %q", filter.State)
	}

	if raw := strings.TrimSpace(r.URL.Query().Get("session_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || parsed == 0 {
			return TaskFilter{}, fmt.Errorf("invalid session_id")
		}
		sessionID := uint32(parsed)
		filter.SessionID = &sessionID
	}
	return filter, nil
}

func parseTaskLastEventID(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid Last-Event-ID")
	}
	return parsed, nil
}

func taskAPIEventFromWatch(ev TaskWatchEvent) TaskAPIEvent {
	return TaskAPIEvent{
		Seq:         ev.Seq,
		Type:        "task.report",
		EventID:     ev.Event.EventID,
		NetworkID:   ev.Event.NetworkID,
		NodeName:    ev.Event.NodeName,
		SessionID:   ev.Event.SessionID,
		SessionName: ev.Event.SessionName,
		Summary:     ev.Event.Summary,
		State:       ev.Event.State,
		Timestamp:   ev.Event.Timestamp,
	}
}

func writeTaskSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, ev TaskAPIEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\n", ev.Seq); err != nil {
		return err
	}
	if strings.TrimSpace(eventType) != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", strings.TrimSpace(eventType)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
