package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codewiresh/codewire/internal/oauth"
	"github.com/codewiresh/codewire/internal/store"
)

const accessEventBufferSize = 256

type AccessCacheEvent struct {
	Seq       int64      `json:"seq"`
	Type      string     `json:"type"`
	NetworkID string     `json:"network_id"`
	GrantID   string     `json:"grant_id,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type accessEventSubscription struct {
	id        int64
	networkID string
	ch        chan AccessCacheEvent
}

type AccessEventHub struct {
	mu          sync.Mutex
	nextSeq     int64
	nextSubID   int64
	events      []AccessCacheEvent
	subscribers map[int64]accessEventSubscription
}

func NewAccessEventHub() *AccessEventHub {
	return &AccessEventHub{
		subscribers: make(map[int64]accessEventSubscription),
	}
}

func (h *AccessEventHub) PublishGrantRevoked(networkID, grantID string, revokedAt time.Time) {
	if h == nil {
		return
	}
	h.publish(AccessCacheEvent{
		Type:      "access.grant.revoked",
		NetworkID: strings.TrimSpace(networkID),
		GrantID:   strings.TrimSpace(grantID),
		RevokedAt: ptrTime(revokedAt.UTC()),
	})
}

func (h *AccessEventHub) publish(ev AccessCacheEvent) {
	h.mu.Lock()
	h.nextSeq++
	ev.Seq = h.nextSeq
	h.events = append(h.events, ev)
	if len(h.events) > accessEventBufferSize {
		h.events = append([]AccessCacheEvent(nil), h.events[len(h.events)-accessEventBufferSize:]...)
	}
	subs := make([]accessEventSubscription, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		if sub.networkID == ev.NetworkID {
			subs = append(subs, sub)
		}
	}
	h.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

func (h *AccessEventHub) Subscribe(networkID string, lastSeq int64) ([]AccessCacheEvent, <-chan AccessCacheEvent, func(), bool) {
	if h == nil {
		ch := make(chan AccessCacheEvent)
		close(ch)
		return nil, ch, func() {}, false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	reset := false
	if lastSeq > 0 && len(h.events) > 0 && h.events[0].Seq > lastSeq+1 {
		reset = true
	}

	replay := make([]AccessCacheEvent, 0, len(h.events))
	for _, ev := range h.events {
		if ev.NetworkID != networkID {
			continue
		}
		if ev.Seq > lastSeq {
			replay = append(replay, ev)
		}
	}

	h.nextSubID++
	subID := h.nextSubID
	ch := make(chan AccessCacheEvent, 16)
	h.subscribers[subID] = accessEventSubscription{
		id:        subID,
		networkID: networkID,
		ch:        ch,
	}

	cancel := func() {
		h.mu.Lock()
		sub, ok := h.subscribers[subID]
		if ok {
			delete(h.subscribers, subID)
		}
		h.mu.Unlock()
		if ok {
			close(sub.ch)
		}
	}

	return replay, ch, cancel, reset
}

func accessCacheEventsHandler(st store.Store, hub *AccessEventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		networkID, err := requiredNetworkID(r.URL.Query().Get("network_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok, err := requireMembership(r.Context(), st, networkID, identity); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		} else if !ok {
			writeMembershipRequired(w)
			return
		}

		lastSeq := int64(0)
		if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
			parsed, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil || parsed < 0 {
				http.Error(w, "invalid Last-Event-ID", http.StatusBadRequest)
				return
			}
			lastSeq = parsed
		}

		replay, events, cancel, reset := hub.Subscribe(networkID, lastSeq)
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		if reset {
			if err := writeAccessSSEEvent(w, flusher, "stream.reset", AccessCacheEvent{
				Type:      "stream.reset",
				NetworkID: networkID,
				Seq:       lastSeq,
			}); err != nil {
				return
			}
		}
		for _, ev := range replay {
			if err := writeAccessSSEEvent(w, flusher, ev.Type, ev); err != nil {
				return
			}
		}

		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if err := writeAccessSSEEvent(w, flusher, ev.Type, ev); err != nil {
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

func writeAccessSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, ev AccessCacheEvent) error {
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

func ptrTime(v time.Time) *time.Time {
	return &v
}
