package relay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

type recordingTextWriter struct {
	mu       sync.Mutex
	writes   [][]byte
	failOnce bool
}

func (w *recordingTextWriter) Write(_ context.Context, typ websocket.MessageType, p []byte) error {
	if typ != websocket.MessageText {
		return errors.New("unexpected message type")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failOnce {
		w.failOnce = false
		return errors.New("write failed")
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	w.writes = append(w.writes, cp)
	return nil
}

func (w *recordingTextWriter) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][]byte, len(w.writes))
	copy(out, w.writes)
	return out
}

func TestForwardAgentOutboundWritesMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := &recordingTextWriter{}
	outbound := make(chan []byte, 4)
	done := make(chan struct{})

	go func() {
		defer close(done)
		forwardAgentOutbound(ctx, writer, outbound)
	}()

	outbound <- []byte(`{"type":"TaskReport","session_id":7}`)
	outbound <- nil
	outbound <- []byte(`{"type":"TaskReport","session_id":8}`)

	deadline := time.After(2 * time.Second)
	for {
		writes := writer.snapshot()
		if len(writes) == 2 {
			if string(writes[0]) != `{"type":"TaskReport","session_id":7}` {
				t.Fatalf("first write = %q", writes[0])
			}
			if string(writes[1]) != `{"type":"TaskReport","session_id":8}` {
				t.Fatalf("second write = %q", writes[1])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for writes, got %d", len(writes))
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	<-done
}

func TestForwardAgentOutboundStopsOnWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := &recordingTextWriter{failOnce: true}
	outbound := make(chan []byte, 2)
	done := make(chan struct{})

	go func() {
		defer close(done)
		forwardAgentOutbound(ctx, writer, outbound)
	}()

	outbound <- []byte(`{"type":"TaskReport","session_id":7}`)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for writer loop to stop")
	}

	if got := len(writer.snapshot()); got != 0 {
		t.Fatalf("writes = %d, want 0", got)
	}
}
